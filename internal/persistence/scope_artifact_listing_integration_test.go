package persistence_test

import (
	"context"
	"encoding/json"
	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestScopeArtifactDiscoveryIsDurableScopedAndSeparatelyAuthorized(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	scope, invocation := fixture.target.IsolationDomainID, fixture.target.InvocationID
	t.Cleanup(func() {
		if _, err := fixture.pool.Exec(context.Background(), `TRUNCATE api_authorization_decisions`); err != nil {
			t.Error(err)
		}
	})
	empty, err := fixture.repository.ListScopeArtifacts(ctx, scope, "", nil, "", 1)
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.HasMore {
		t.Fatal("empty page failed", err)
	}
	otherRun := identity.New("inv")
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO invocations (isolation_domain_id,id,service_id,revision_id,alias,state,input,correlation_id,operation_id,created_at,updated_at,created_by) SELECT isolation_domain_id,$3,service_id,revision_id,alias,state,input,correlation_id,$4,created_at,updated_at,created_by FROM invocations WHERE isolation_domain_id=$1 AND id=$2`, scope, invocation, otherRun, identity.New("op")); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	insert := func(target, run, id, name string) {
		t.Helper()
		if _, err := fixture.pool.Exec(ctx, `INSERT INTO artifacts (isolation_domain_id,id,invocation_id,name,kind,media_type,size_bytes,digest,state,sensitive,created_at,updated_at,created_by) VALUES ($1,$2,$3,$4,'file','text/plain',32,$5,'available',true,$6,$6,'operator')`, target, id, run, name, "sha256:"+strings.Repeat("a", 64), created); err != nil {
			t.Fatal(err)
		}
	}
	firstID, secondID := "art_00000000000000000002", "art_00000000000000000001"
	insert(scope, invocation, firstID, "First output")
	insert(scope, otherRun, secondID, "Second output")
	otherScope := identity.New("iso")
	for _, query := range []string{
		`INSERT INTO agent_services (isolation_domain_id,id,name,created_at,updated_at,created_by) SELECT $2,id,'Foreign service',created_at,updated_at,created_by FROM agent_services WHERE isolation_domain_id=$1`,
		`INSERT INTO service_revisions (isolation_domain_id,id,service_id,revision_number,state,runtime_profile,required_capabilities,created_at,updated_at,created_by) SELECT $2,id,service_id,revision_number,state,runtime_profile,required_capabilities,created_at,updated_at,created_by FROM service_revisions WHERE isolation_domain_id=$1`,
		`INSERT INTO invocations (isolation_domain_id,id,service_id,revision_id,alias,state,input,correlation_id,operation_id,created_at,updated_at,created_by) SELECT $2,id,service_id,revision_id,alias,state,input,correlation_id,'op_'||md5($2||id),created_at,updated_at,created_by FROM invocations WHERE isolation_domain_id=$1`,
	} {
		if _, err := fixture.pool.Exec(ctx, query, scope, otherScope); err != nil {
			t.Fatal(err)
		}
	}
	insert(otherScope, invocation, firstID, "Foreign output")
	first, err := fixture.repository.ListScopeArtifacts(ctx, scope, "available", nil, "", 1)
	if err != nil || len(first.Items) != 1 || first.Items[0].Metadata.ID != firstID || !first.HasMore || first.Items[0].ServiceName == "" || first.Items[0].ServiceName == "Foreign service" {
		t.Fatalf("first page: %#v %v", first, err)
	}
	restarted := persistence.NewRepository(fixture.pool)
	tail, err := restarted.ListScopeArtifacts(ctx, scope, "available", &first.Items[0].Metadata.CreatedAt, firstID, 1)
	if err != nil || len(tail.Items) != 1 || tail.Items[0].InvocationID != otherRun || tail.Items[0].Metadata.ID != secondID || tail.HasMore {
		t.Fatalf("continued sources: %#v %v", tail, err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE artifacts SET state='deleted',version=version+1 WHERE isolation_domain_id=$1 AND id=$2`, scope, secondID); err != nil {
		t.Fatal(err)
	}
	deleted, err := restarted.ListScopeArtifacts(ctx, scope, "deleted", nil, "", 10)
	if err != nil || len(deleted.Items) != 1 || deleted.Items[0].Metadata.ID != secondID {
		t.Fatal("current retention state not reflected", err)
	}
	for _, limit := range []int{0, 101} {
		if _, err := restarted.ListScopeArtifacts(ctx, scope, "", nil, "", limit); err == nil {
			t.Fatal("unbounded page accepted")
		}
	}
	if _, err := restarted.ListScopeArtifacts(ctx, scope, "", &created, "", 1); err == nil {
		t.Fatal("partial cursor accepted")
	}
	absent, err := restarted.ListScopeArtifacts(ctx, identity.New("iso"), "", nil, "", 1)
	if err != nil || len(absent.Items) != 0 {
		t.Fatal("empty scope failed", err)
	}
	const token = "scope-artifact-discovery-test-token-only"
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: identity.New("usr"), IsolationDomainID: scope})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{PolicySetID: "scope-artifact-discovery", Schema: authz.CanonicalAPICedarSchema(), Policies: []byte(`permit(principal, action == DataGround::Action::"listScopeArtifacts", resource);`)})
	if err != nil {
		t.Fatal(err)
	}
	audited, err := authz.NewAuditedAuthorizer(policy, restarted)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewDurableHandler(restarted, authenticator, audited)
	if err != nil {
		t.Fatal(err)
	}
	read := func(path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("response %d: %s", response.Code, response.Body.String())
		}
		return response
	}
	path := "/v1/isolation-domains/" + scope + "/artifacts"
	response := read(path+"?limit=1", 200)
	var page struct {
		Items      []domain.ScopeArtifactSummary `json:"items"`
		NextCursor string                        `json:"nextCursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.NextCursor == "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invalid HTTP page")
	}
	read("/v1/isolation-domains/"+scope+"/invocations/"+invocation+"/artifacts/"+firstID, 403)
	read("/v1/isolation-domains/"+scope+"/invocations/"+invocation+"/artifacts/"+firstID+"/content", 403)
	read("/v1/isolation-domains/"+scope+"/invocations/"+invocation, 403)
	read("/v1/isolation-domains/"+scope+"/agent-services/"+first.Items[0].ServiceID, 403)
	read(path+"?state=deleted&cursor="+page.NextCursor, 400)
	read(strings.Replace(path, scope, otherScope, 1), 403)
	var count int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM api_authorization_decisions WHERE isolation_domain_id=$1 AND action='listScopeArtifacts' AND resource_id=$1 AND outcome='allowed'`, scope).Scan(&count); err != nil || count != 2 {
		t.Fatalf("missing metadata audit: %d %v", count, err)
	}
	migration, err := os.ReadFile("migrations/00079_scope_artifact_discovery.sql")
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
	if _, err := tx.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "cannot remove scope artifact discovery authorization evidence") {
		t.Fatalf("downgrade lost evidence: %v", err)
	}
}
