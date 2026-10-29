package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
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

func TestArtifactDiscoveryIsScopedDurableBoundedAndAudited(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	scope, invocation := fixture.target.IsolationDomainID, fixture.target.InvocationID
	t.Cleanup(func() {
		if _, err := fixture.pool.Exec(context.Background(), `TRUNCATE api_authorization_decisions`); err != nil {
			t.Error(err)
		}
	})
	empty, err := fixture.repository.ListInvocationArtifacts(ctx, scope, invocation, nil, "", 1)
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.HasMore {
		t.Fatalf("empty page: %#v %v", empty, err)
	}
	created := time.Date(2026, 8, 1, 12, 0, 0, 123456000, time.UTC)
	ids := []string{"art_00000000000000000003", "art_00000000000000000002", "art_00000000000000000001"}
	insert := func(scope, invocation, id, name string, at time.Time) {
		t.Helper()
		_, err := fixture.pool.Exec(ctx, `INSERT INTO artifacts (isolation_domain_id,id,invocation_id,name,kind,media_type,size_bytes,digest,state,sensitive,created_at,updated_at,created_by)
          VALUES ($1,$2,$3,$4,'file','text/plain',32,$5,'available',true,$6,$6,'operator')`, scope, id, invocation, name, "sha256:"+strings.Repeat("a", 64), at)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids {
		insert(scope, invocation, id, "Analysis.txt", created)
	}
	otherRun := identity.New("inv")
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO invocations (isolation_domain_id,id,service_id,revision_id,alias,state,input,correlation_id,operation_id,created_at,updated_at,created_by)
       SELECT isolation_domain_id,$3,service_id,revision_id,alias,state,input,correlation_id,$4,created_at,updated_at,created_by FROM invocations WHERE isolation_domain_id=$1 AND id=$2`, scope, invocation, otherRun, identity.New("op")); err != nil {
		t.Fatal(err)
	}
	insert(scope, otherRun, identity.New("art"), "Hidden other run", created)
	otherScope := identity.New("iso")
	for _, table := range []string{"agent_services", "service_revisions", "invocations"} {
		// Copy only the controlled fixture, including colliding resource IDs.
		var query string
		switch table {
		case "agent_services":
			query = `INSERT INTO agent_services (isolation_domain_id,id,name,created_at,updated_at,created_by) SELECT $2,id,name,created_at,updated_at,created_by FROM agent_services WHERE isolation_domain_id=$1`
		case "service_revisions":
			query = `INSERT INTO service_revisions (isolation_domain_id,id,service_id,revision_number,state,runtime_profile,required_capabilities,created_at,updated_at,created_by) SELECT $2,id,service_id,revision_number,state,runtime_profile,required_capabilities,created_at,updated_at,created_by FROM service_revisions WHERE isolation_domain_id=$1`
		case "invocations":
			query = `INSERT INTO invocations (isolation_domain_id,id,service_id,revision_id,alias,state,input,correlation_id,operation_id,created_at,updated_at,created_by) SELECT $2,id,service_id,revision_id,alias,state,input,correlation_id,'op_'||md5($2||id),created_at,updated_at,created_by FROM invocations WHERE isolation_domain_id=$1`
		}
		if _, err := fixture.pool.Exec(ctx, query, scope, otherScope); err != nil {
			t.Fatal(err)
		}
	}
	insert(otherScope, invocation, ids[0], "Hidden other scope", created)
	first, err := fixture.repository.ListInvocationArtifacts(ctx, scope, invocation, nil, "", 1)
	if err != nil || len(first.Items) != 1 || first.Items[0].Metadata.ID != ids[0] || !first.HasMore {
		t.Fatalf("first page: %#v %v", first, err)
	}
	insert(scope, invocation, identity.New("art"), "New arrival", created.Add(time.Second))
	if _, err := fixture.pool.Exec(ctx, `UPDATE artifacts SET state='deleted',version=version+1 WHERE isolation_domain_id=$1 AND id=$2`, scope, ids[1]); err != nil {
		t.Fatal(err)
	}
	restarted := persistence.NewRepository(fixture.pool)
	tail, err := restarted.ListInvocationArtifacts(ctx, scope, invocation, &first.Items[0].Metadata.CreatedAt, first.Items[0].Metadata.ID, 2)
	if err != nil || len(tail.Items) != 2 || tail.HasMore || tail.Items[0].Metadata.ID != ids[1] || tail.Items[0].State != "deleted" || tail.Items[1].Metadata.ID != ids[2] {
		t.Fatalf("continuation: %#v %v", tail, err)
	}
	for _, item := range tail.Items {
		exact, err := restarted.GetArtifact(ctx, scope, invocation, item.Metadata.ID)
		if err != nil || !reflect.DeepEqual(exact, item) {
			t.Fatalf("list differs from exact metadata: %v", err)
		}
	}
	other, err := restarted.ListInvocationArtifacts(ctx, otherScope, invocation, nil, "", 10)
	if err != nil || len(other.Items) != 1 || other.Items[0].Name != "Hidden other scope" {
		t.Fatal("colliding artifact crossed isolation")
	}
	for _, target := range [][2]string{{identity.New("iso"), invocation}, {scope, identity.New("inv")}} {
		_, err := restarted.ListInvocationArtifacts(ctx, target[0], target[1], nil, "", 1)
		var missing *persistence.DomainError
		if !errors.As(err, &missing) || missing.Code != "RESOURCE_NOT_FOUND" {
			t.Fatalf("missing target: %v", err)
		}
	}
	for _, limit := range []int{0, 101} {
		if _, err := restarted.ListInvocationArtifacts(ctx, scope, invocation, nil, "", limit); err == nil {
			t.Fatal("unbounded page accepted")
		}
	}
	if _, err := restarted.ListInvocationArtifacts(ctx, scope, invocation, &created, "", 1); err == nil {
		t.Fatal("partial boundary accepted")
	}
	const token = "artifact-discovery-development-test-token-only"
	actor := identity.New("usr")
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: actor, IsolationDomainID: scope})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{PolicySetID: "artifact-discovery", Schema: authz.CanonicalAPICedarSchema(), Policies: []byte(`permit(principal, action == DataGround::Action::"listInvocationArtifacts", resource);`)})
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
	path := "/v1/isolation-domains/" + scope + "/invocations/" + invocation + "/artifacts"
	response := read(path+"?limit=1", http.StatusOK)
	var page struct {
		Items      []domain.ArtifactDescriptor `json:"items"`
		NextCursor string                      `json:"nextCursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.NextCursor == "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invalid HTTP metadata page")
	}
	read(path+"/"+ids[0], http.StatusForbidden)
	read(path+"/"+ids[0]+"/content", http.StatusForbidden)
	read(strings.Replace(path, invocation, otherRun, 1)+"?cursor="+page.NextCursor, http.StatusBadRequest)
	read(strings.Replace(path, scope, otherScope, 1), http.StatusForbidden)
	var count int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM api_authorization_decisions WHERE isolation_domain_id=$1 AND action='listInvocationArtifacts' AND resource_id=$2 AND outcome='allowed'`, scope, invocation).Scan(&count); err != nil || count != 1 {
		t.Fatalf("missing audit: %d %v", count, err)
	}
	migration, err := os.ReadFile("migrations/00077_invocation_artifact_discovery.sql")
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
	if _, err := tx.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "cannot remove artifact discovery authorization evidence") {
		t.Fatalf("downgrade discarded evidence: %v", err)
	}
}
