package persistence_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestSavedResourcesDurableIsolationRecoveryAndAudit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newRuntimeQuestionFixture(t, ctx)
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(), `TRUNCATE saved_resources,workspaces,api_authorization_decisions CASCADE`); err != nil {
			t.Error(err)
		}
	})
	scope, service, invocation := f.target.IsolationDomainID, f.target.ServiceID, f.target.InvocationID
	workspace, artifact := identity.New("wsp"), identity.New("art")
	if _, err := f.pool.Exec(ctx, `INSERT INTO workspaces (isolation_domain_id,id,name,created_at,updated_at,created_by) VALUES ($1,$2,'Saved research',now(),now(),'private actor')`, scope, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE agent_services SET workspace_id=$2 WHERE isolation_domain_id=$1`, scope, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO artifacts (isolation_domain_id,id,invocation_id,name,kind,media_type,size_bytes,digest,state,sensitive,created_at,updated_at,created_by) VALUES ($1,$2,$3,'Saved output','file','text/plain',32,$4,'available',true,now(),now(),'private actor')`, scope, artifact, invocation, "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	repo := persistence.NewRepository(f.pool)
	owner := persistence.SavedResourceOwner{Kind: authn.PrincipalHuman, ID: "usr_saved_test"}
	input := persistence.SetSavedResourceInput{Owner: owner, Kind: "workspace", ResourceID: workspace, Saved: true, CorrelationID: identity.New("cor")}
	idem := testIdempotency(scope, "saved-concurrent")
	var group sync.WaitGroup
	results := make(chan persistence.CommandResult, 8)
	failures := make(chan error, 8)
	for range 8 {
		group.Go(func() { result, err := repo.SetSavedResource(ctx, idem, input); results <- result; failures <- err })
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first string
	created := 0
	for result := range results {
		if first == "" {
			first = string(result.Body)
		}
		if result.Status != 200 || string(result.Body) != first {
			t.Fatal("concurrent replay changed")
		}
		if !result.Replayed {
			created++
		}
	}
	if created != 1 {
		t.Fatal("duplicate command effects", created)
	}
	var audit, outbox int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND resource_type='saved-resource'),(SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND aggregate_type='saved-resource')`, scope).Scan(&audit, &outbox); err != nil || audit != 1 || outbox != 1 {
		t.Fatal("atomic receipts missing or duplicated", audit, outbox, err)
	}
	for kind, id := range map[string]string{"agent-service": service, "invocation": invocation, "artifact": artifact} {
		in := input
		in.Kind = kind
		in.ResourceID = id
		in.CorrelationID = identity.New("cor")
		if _, err := repo.SetSavedResource(ctx, testIdempotency(scope, "saved-"+kind), in); err != nil {
			t.Fatal(err)
		}
	}
	// Equal timestamps exercise the stable keyset tie breaker through restart.
	if _, err := f.pool.Exec(ctx, `UPDATE saved_resources SET updated_at='2026-10-29T08:00:00.000001Z' WHERE isolation_domain_id=$1`, scope); err != nil {
		t.Fatal(err)
	}
	repo = persistence.NewRepository(f.pool)
	var before *time.Time
	var beforeID string
	seen := map[string]bool{}
	for {
		page, err := repo.ListSavedResources(ctx, scope, owner, before, beforeID, 1)
		if err != nil || len(page.Items) != 1 {
			t.Fatal("saved page failed", page, err)
		}
		item := page.Items[0]
		if seen[item.ResourceID] || item.Resource == nil || item.Resource.Metadata.IsolationDomainID != scope {
			t.Fatal("invalid saved page", item)
		}
		seen[item.ResourceID] = true
		if item.Kind == "artifact" && (item.Resource.InvocationID != invocation || item.Resource.ServiceID != service || item.Resource.WorkspaceID != workspace) {
			t.Fatal("source linkage missing")
		}
		encoded, _ := json.Marshal(item)
		if strings.Contains(string(encoded), "private actor") || strings.Contains(string(encoded), "prompt") {
			t.Fatal("private content projected")
		}
		if !page.HasMore {
			break
		}
		if len(seen) > 4 {
			t.Fatal("cursor loop")
		}
		before = &item.SavedAt
		beforeID = item.ResourceID
	}
	if len(seen) != 4 {
		t.Fatal("saved resources lost")
	}
	for _, other := range []persistence.SavedResourceOwner{{Kind: owner.Kind, ID: "usr_other"}, {Kind: authn.PrincipalService, ID: owner.ID}} {
		page, err := repo.ListSavedResources(ctx, scope, other, nil, "", 10)
		if err != nil || len(page.Items) != 0 {
			t.Fatal("foreign principal list", err)
		}
	}
	foreign := identity.New("iso")
	if page, err := repo.ListSavedResources(ctx, foreign, owner, nil, "", 10); err != nil || len(page.Items) != 0 {
		t.Fatal("foreign scope list", err)
	}
	if _, err := repo.SetSavedResource(ctx, testIdempotency(foreign, "saved-foreign"), input); err == nil {
		t.Fatal("foreign target saved")
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO workspaces (isolation_domain_id,id,name,created_at,updated_at,created_by) VALUES ($1,$2,'Foreign saved research',now(),now(),'foreign actor')`, foreign, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetSavedResource(ctx, testIdempotency(foreign, "saved-scoped-collision"), input); err != nil {
		t.Fatal("scoped ID collision rejected", err)
	}
	if page, err := repo.ListSavedResources(ctx, foreign, owner, nil, "", 10); err != nil || len(page.Items) != 1 || page.Items[0].Resource.Name != "Foreign saved research" {
		t.Fatal("scoped ID collision resolved the wrong target", err)
	}
	beforeRepeat, err := repo.ListSavedResources(ctx, scope, owner, nil, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	repeated := input
	repeated.CorrelationID = identity.New("cor")
	if _, err := repo.SetSavedResource(ctx, testIdempotency(scope, "saved-repeat-new-key"), repeated); err != nil {
		t.Fatal(err)
	}
	afterRepeat, err := repo.ListSavedResources(ctx, scope, owner, nil, "", 10)
	beforeJSON, _ := json.Marshal(beforeRepeat)
	afterJSON, _ := json.Marshal(afterRepeat)
	if err != nil || string(beforeJSON) != string(afterJSON) {
		t.Fatal("repeated save moved its position", err)
	}
	for _, other := range []persistence.SavedResourceOwner{{Kind: owner.Kind, ID: "usr_other"}, {Kind: authn.PrincipalService, ID: owner.ID}} {
		in := input
		in.Owner = other
		in.CorrelationID = identity.New("cor")
		if _, err := repo.SetSavedResource(ctx, testIdempotency(scope, "saved-owner-"+string(other.Kind)+other.ID), in); err != nil {
			t.Fatal(err)
		}
	}
	remove := input
	remove.Saved = false
	remove.CorrelationID = identity.New("cor")
	if _, err := repo.SetSavedResource(ctx, testIdempotency(scope, "saved-remove"), remove); err != nil {
		t.Fatal(err)
	}
	if replay, err := repo.SetSavedResource(ctx, idem, input); err != nil || string(replay.Body) != first || !replay.Replayed {
		t.Fatal("historical replay changed", err)
	}
	page, err := repo.ListSavedResources(ctx, scope, owner, nil, "", 10)
	if err != nil || len(page.Items) != 3 {
		t.Fatal("historical replay restored resource", err)
	}
	// The reference does not become a cached copy of metadata after deletion.
	if _, err := f.pool.Exec(ctx, `DELETE FROM artifacts WHERE isolation_domain_id=$1 AND id=$2`, scope, artifact); err != nil {
		t.Fatal(err)
	}
	page, err = repo.ListSavedResources(ctx, scope, owner, nil, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range page.Items {
		if item.ResourceID == artifact {
			found = true
			if item.Resource != nil {
				t.Fatal("stale metadata retained")
			}
		}
	}
	if !found {
		t.Fatal("unavailable reference cannot be removed")
	}
	in := remove
	in.Kind = "artifact"
	in.ResourceID = artifact
	in.CorrelationID = identity.New("cor")
	if _, err := repo.SetSavedResource(ctx, testIdempotency(scope, "remove-unavailable"), in); err != nil {
		t.Fatal("unavailable removal failed", err)
	}
	// A failed audit must roll back both saved state and idempotency completion.
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION reject_saved_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.resource_type='saved-resource' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_saved_audit BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION reject_saved_audit()`); err != nil {
		t.Fatal(err)
	}
	defer f.pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_saved_audit() CASCADE`)
	input.CorrelationID = identity.New("cor")
	retry := testIdempotency(scope, "saved-audit-retry")
	if _, err := repo.SetSavedResource(ctx, retry, input); err == nil {
		t.Fatal("audit failure accepted")
	}
	page, err = repo.ListSavedResources(ctx, scope, owner, nil, "", 10)
	if err != nil || len(page.Items) != 2 {
		t.Fatal("audit failure changed saved list", err)
	}
	if _, err := f.pool.Exec(ctx, `DROP FUNCTION reject_saved_audit() CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetSavedResource(ctx, retry, input); err != nil {
		t.Fatal("exact retry failed", err)
	}
	// Real API policy grants only the private collection, not target content.
	const token = "saved-resource-http-token-local-only"
	authentication, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: owner.ID, IsolationDomainID: scope})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{PolicySetID: "saved-resources", Schema: authz.CanonicalAPICedarSchema(), Policies: []byte(`permit(principal, action in [DataGround::Action::"listSavedResources",DataGround::Action::"setSavedResource"], resource);`)})
	if err != nil {
		t.Fatal(err)
	}
	audited, err := authz.NewAuditedAuthorizer(policy, repo)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewDurableHandler(repo, authentication, audited)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, key, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("HTTP %d: %s", res.Code, res.Body.String())
		}
		return res
	}
	root := "/v1/isolation-domains/" + scope
	path := root + "/saved-resources"
	response := call("GET", path+"?limit=1", "", "", 200)
	var listed struct {
		Items      []domain.SavedResource `json:"items"`
		NextCursor string                 `json:"nextCursor"`
	}
	if json.Unmarshal(response.Body.Bytes(), &listed) != nil || len(listed.Items) != 1 || listed.NextCursor == "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invalid API saved page")
	}
	call("GET", path+"?limit=1&cursor="+listed.NextCursor, "", "", 200)
	call("GET", root+"/workspaces/"+workspace, "", "", 403)
	call("GET", root+"/search?q=research", "", "", 403)
	body := fmt.Sprintf(`{"kind":"workspace","resourceId":%q,"saved":false}`, workspace)
	command := call("POST", path, "saved-http-remove", body, 200)
	if replay := call("POST", path, "saved-http-remove", body, 200); replay.Body.String() != command.Body.String() {
		t.Fatal("HTTP replay changed")
	}
	call("POST", path, "saved-http-remove", strings.Replace(body, "false", "true", 1), 409)
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM api_authorization_decisions WHERE isolation_domain_id=$1 AND action IN ('listSavedResources','setSavedResource') AND resource_id=$1 AND outcome='allowed'`, scope).Scan(&count); err != nil || count != 5 {
		t.Fatal("authorization evidence missing", count, err)
	}
	migration, err := os.ReadFile("migrations/00082_saved_resources.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, down, found := strings.Cut(string(migration), "-- dataground:down")
	if !found {
		t.Fatal("missing downgrade")
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "cannot remove saved resources or authorization evidence") {
		t.Fatal("unsafe downgrade", err)
	}
}
