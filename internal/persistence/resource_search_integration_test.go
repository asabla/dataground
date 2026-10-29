package persistence_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestResourceSearchIsScopedDurableAndSeparatelyAuthorized(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	scope, service, invocation := fixture.target.IsolationDomainID, fixture.target.ServiceID, fixture.target.InvocationID
	t.Cleanup(func() {
		if _, err := fixture.pool.Exec(context.Background(), `TRUNCATE workspaces,api_authorization_decisions CASCADE`); err != nil {
			t.Error(err)
		}
	})
	workspace, artifact := identity.New("wsp"), identity.New("art")
	created := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO workspaces (isolation_domain_id,id,name,description,created_at,updated_at,created_by) VALUES ($1,$2,'Research workspace','Budget_%',$3,$3,'private actor')`, scope, workspace, created); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE agent_services SET name='Research service',description='Budget_%',workspace_id=$2,created_at=$3 WHERE isolation_domain_id=$1`, scope, workspace, created); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE invocations SET created_at=$2,input='{"prompt":"private prompt"}' WHERE isolation_domain_id=$1`, scope, created); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO artifacts (isolation_domain_id,id,invocation_id,name,kind,media_type,size_bytes,digest,state,sensitive,created_at,updated_at,created_by) VALUES ($1,$2,$3,'Research output','file','text/plain',32,$4,'available',true,$5,$5,'private actor')`, scope, artifact, invocation, "sha256:"+strings.Repeat("a", 64), created); err != nil {
		t.Fatal(err)
	}
	// A newer nonmatch must not hide older results behind the page limit.
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO agent_services (isolation_domain_id,id,name,created_at,updated_at,created_by) VALUES ($1,$2,'Unrelated',$3,$3,'creator')`, scope, identity.New("svc"), created.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	other := identity.New("iso")
	for _, query := range []string{
		`INSERT INTO workspaces (isolation_domain_id,id,name,created_at,updated_at,created_by) SELECT $2,id,'Foreign research',created_at,updated_at,created_by FROM workspaces WHERE isolation_domain_id=$1`,
		`INSERT INTO agent_services (isolation_domain_id,id,name,workspace_id,created_at,updated_at,created_by) SELECT $2,id,'Foreign research',workspace_id,created_at,updated_at,created_by FROM agent_services WHERE isolation_domain_id=$1`,
		`INSERT INTO service_revisions (isolation_domain_id,id,service_id,revision_number,state,runtime_profile,required_capabilities,created_at,updated_at,created_by) SELECT $2,id,service_id,revision_number,state,runtime_profile,required_capabilities,created_at,updated_at,created_by FROM service_revisions WHERE isolation_domain_id=$1`,
		`INSERT INTO invocations (isolation_domain_id,id,service_id,revision_id,alias,state,input,correlation_id,operation_id,created_at,updated_at,created_by) SELECT $2,id,service_id,revision_id,alias,state,input,correlation_id,'op_'||md5($2||id),created_at,updated_at,created_by FROM invocations WHERE isolation_domain_id=$1`,
		`INSERT INTO artifacts (isolation_domain_id,id,invocation_id,name,kind,media_type,size_bytes,digest,state,sensitive,created_at,updated_at,created_by) SELECT $2,id,invocation_id,'Foreign research output',kind,media_type,size_bytes,digest,state,sensitive,created_at,updated_at,created_by FROM artifacts WHERE isolation_domain_id=$1`,
	} {
		if _, err := fixture.pool.Exec(ctx, query, scope, other); err != nil {
			t.Fatal(err)
		}
	}
	repo := persistence.NewRepository(fixture.pool)
	var before *time.Time
	var id string
	var items []domain.ResourceSearchResult
	for {
		page, err := repo.SearchResources(ctx, scope, "RESEARCH", "", before, id, 1)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("page: %#v %v", page, err)
		}
		item := page.Items[0]
		items = append(items, item)
		if item.Metadata.IsolationDomainID != scope || strings.Contains(item.Name, "Foreign") {
			t.Fatal("foreign result", item)
		}
		if !page.HasMore {
			break
		}
		if len(items) > 4 {
			t.Fatal("pagination did not advance")
		}
		before = &item.Metadata.CreatedAt
		id = item.Metadata.ID
	}
	if len(items) != 4 {
		t.Fatalf("result count: %d", len(items))
	}
	for i, kind := range []string{"workspace", "agent-service", "invocation", "artifact"} {
		if items[i].Kind != kind {
			t.Fatalf("creation tie order: %#v", items)
		}
		page, err := repo.SearchResources(ctx, scope, "research", kind, nil, "", 10)
		if err != nil || len(page.Items) != 1 || page.Items[0].Kind != kind {
			t.Fatal("kind filter failed", kind, err)
		}
	}
	if items[2].ServiceID != service || items[2].WorkspaceID != workspace || items[3].InvocationID != invocation || items[3].ServiceID != service {
		t.Fatal("navigation identity lost")
	}
	for _, test := range []struct {
		query string
		count int
	}{{"Budget_%", 2}, {"not_%", 0}, {artifact, 1}, {"private prompt", 0}} {
		page, err := repo.SearchResources(ctx, scope, test.query, "", nil, "", 50)
		if err != nil || len(page.Items) != test.count {
			t.Fatal("literal query", test.query, page, err)
		}
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE artifacts SET state='deleted',version=version+1 WHERE isolation_domain_id=$1 AND id=$2`, scope, artifact); err != nil {
		t.Fatal(err)
	}
	current, err := repo.SearchResources(ctx, scope, artifact, "artifact", nil, "", 1)
	if err != nil || current.Items[0].State != "deleted" {
		t.Fatal("current state missing", err)
	}
	for _, limit := range []int{0, 101} {
		if _, err := repo.SearchResources(ctx, scope, "x", "", nil, "", limit); err == nil {
			t.Fatal("unbounded request accepted")
		}
	}
	for _, kind := range []string{"future"} {
		if _, err := repo.SearchResources(ctx, scope, "x", kind, nil, "", 1); err == nil {
			t.Fatal("unknown kind accepted")
		}
	}
	if _, err := repo.SearchResources(ctx, scope, "", "", nil, "", 1); err == nil {
		t.Fatal("empty search accepted")
	}
	if _, err := repo.SearchResources(ctx, scope, "x", "", &created, "", 1); err == nil {
		t.Fatal("partial cursor accepted")
	}
	empty, err := repo.SearchResources(ctx, identity.New("iso"), "research", "", nil, "", 1)
	if err != nil || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatal("empty scope", err)
	}
	const token = "resource-search-test-token-local-only"
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: identity.New("usr"), IsolationDomainID: scope})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{PolicySetID: "resource-search", Schema: authz.CanonicalAPICedarSchema(), Policies: []byte(`permit(principal, action == DataGround::Action::"searchResources", resource);`)})
	if err != nil {
		t.Fatal(err)
	}
	audited, err := authz.NewAuditedAuthorizer(policy, repo)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewDurableHandler(repo, authenticator, audited)
	if err != nil {
		t.Fatal(err)
	}
	read := func(path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("response: %d %s", res.Code, res.Body.String())
		}
		return res
	}
	path := "/v1/isolation-domains/" + scope
	first := read(path+"/search?q=research&limit=1", 200)
	var page struct {
		Items      []domain.ResourceSearchResult `json:"items"`
		NextCursor string                        `json:"nextCursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.NextCursor == "" || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invalid HTTP page")
	}
	read(path+"/search?q=RESEARCH&limit=1&cursor="+page.NextCursor, 200)
	read(path+"/search?q=changed&cursor="+page.NextCursor, 400)
	read(strings.Replace(path, scope, other, 1)+"/search?q=research", 403)
	for _, suffix := range []string{"/workspaces/" + workspace, "/agent-services/" + service, "/invocations/" + invocation, "/invocations/" + invocation + "/artifacts/" + artifact, "/invocations/" + invocation + "/artifacts/" + artifact + "/content"} {
		read(path+suffix, 403)
	}
	var count int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM api_authorization_decisions WHERE isolation_domain_id=$1 AND action='searchResources' AND resource_id=$1 AND outcome='allowed'`, scope).Scan(&count); err != nil || count != 3 {
		t.Fatalf("search entry audit: %d %v", count, err)
	}
	migration, err := os.ReadFile("migrations/00081_resource_search.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, down, found := strings.Cut(string(migration), "-- dataground:down")
	if !found {
		t.Fatal("missing downgrade")
	}
	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "cannot remove resource search authorization evidence") {
		t.Fatalf("downgrade lost evidence: %v", err)
	}
}
