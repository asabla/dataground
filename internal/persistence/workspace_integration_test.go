package persistence_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
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

func TestDurableWorkspacesPreserveIdentityAuditAndConcurrentReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	t.Cleanup(func() {
		defer pool.Close()
		if _, err := pool.Exec(context.Background(), `TRUNCATE invocation_workspace_completions, invocation_workspace_preparations, workspace_source_grants, workspace_source_use_decisions, workspace_source_completions, workspace_source_installations, workspaces, api_authorization_decisions`); err != nil {
			t.Error(err)
		}
	})
	repo := persistence.NewRepository(pool)
	scope := identity.New("iso")
	other := identity.New("iso")
	id := identity.New("wsp")
	idem := testIdempotency(scope, "workspace-concurrent")
	input := persistence.CreateWorkspaceInput{ID: id, Name: "Research", Description: "private metadata", ActorID: "workspace-test", CorrelationID: identity.New("cor")}
	var group sync.WaitGroup
	results := make(chan persistence.CommandResult, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			r, err := repo.CreateWorkspace(ctx, idem, input)
			results <- r
			failures <- err
		}()
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var body string
	created := 0
	for result := range results {
		if body == "" {
			body = string(result.Body)
		}
		if string(result.Body) != body || result.Status != 201 {
			t.Fatal("concurrent response changed")
		}
		if !result.Replayed {
			created++
		}
	}
	if created != 1 {
		t.Fatal("idempotency did not serialize creation", created)
	}
	var value domain.Workspace
	if json.Unmarshal([]byte(body), &value) != nil {
		t.Fatal("invalid response")
	}
	restarted := persistence.NewRepository(pool)
	got, err := restarted.GetWorkspace(ctx, scope, id)
	encoded, _ := json.Marshal(got)
	if err != nil || string(encoded) != body {
		t.Fatal("restart changed workspace identity", err)
	}
	if _, err := repo.GetWorkspace(ctx, other, id); err == nil {
		t.Fatal("cross-domain read accepted")
	}
	changed := idem
	changed.RequestDigest = sha256.Sum256([]byte("changed"))
	if _, err := repo.CreateWorkspace(ctx, changed, input); err == nil {
		t.Fatal("changed replay accepted")
	}
	otherInput := input
	otherInput.Name = "Foreign"
	if _, err := repo.CreateWorkspace(ctx, testIdempotency(other, "workspace-collision"), otherInput); err != nil {
		t.Fatal("scoped ID collision rejected", err)
	}
	otherWorkspace, err := repo.GetWorkspace(ctx, other, id)
	if err != nil || otherWorkspace.Name != "Foreign" {
		t.Fatal("scoped ID collision leaked")
	}
	var audit, outbox int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND resource_type='workspace' AND resource_id=$2),(SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND aggregate_type='workspace' AND aggregate_id=$2)`, scope, id).Scan(&audit, &outbox); err != nil || audit != 1 || outbox != 1 {
		t.Fatal("atomic receipts duplicated or absent", audit, outbox, err)
	}
	// Equal timestamps must still produce stable, complete keyset pages.
	for _, suffix := range []string{"00000000000000000001", "00000000000000000002"} {
		v := input
		v.ID = "wsp_" + suffix
		if _, err := repo.CreateWorkspace(ctx, testIdempotency(scope, suffix), v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE workspaces SET created_at='2026-01-01T00:00:00Z' WHERE isolation_domain_id=$1`, scope); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var before *time.Time
	beforeID := ""
	for {
		page, err := repo.ListWorkspaces(ctx, scope, before, beforeID, 1)
		if err != nil || len(page.Items) != 1 {
			t.Fatal("page failed", err)
		}
		v := page.Items[0]
		if seen[v.Metadata.ID] || v.Metadata.IsolationDomainID != scope {
			t.Fatal("page collision")
		}
		seen[v.Metadata.ID] = true
		if !page.HasMore {
			break
		}
		stamp := v.Metadata.CreatedAt
		before = &stamp
		beforeID = v.Metadata.ID
	}
	if len(seen) != 3 {
		t.Fatal("page lost workspace")
	}
	const actor = "usr_00000000000000000001"
	const token = "workspace-integration-token-with-thirty-two-bytes"
	authentication, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: actor, IsolationDomainID: scope})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := authz.NewDevelopmentCedarAuthorizer(actor, scope)
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := authz.NewAuditedAuthorizer(policy, repo)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewDurableHandler(repo, authentication, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/v1/isolation-domains/" + scope + "/workspaces"
	made := call("POST", path, "workspace-http-create", `{"name":"HTTP workspace"}`)
	if made.Code != 201 {
		t.Fatal(made.Code, made.Body.String())
	}
	var fromHTTP domain.Workspace
	_ = json.Unmarshal(made.Body.Bytes(), &fromHTTP)
	for _, target := range []string{path, path + "/" + fromHTTP.Metadata.ID} {
		if r := call("GET", target, "", ""); r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	if r := call("POST", path, "workspace-http-create", `{"name":"HTTP workspace"}`); r.Body.String() != made.Body.String() {
		t.Fatal("HTTP replay changed")
	}
	if r := call("POST", path, "workspace-http-invalid", `{"name":"x","description":"\u0000"}`); r.Code != 400 {
		t.Fatal("invalid PostgreSQL metadata admitted", r.Code)
	}
	var actions int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT action) FROM api_authorization_decisions WHERE isolation_domain_id=$1 AND action IN ('createWorkspace','listWorkspaces','readWorkspace') AND outcome='allowed'`, scope).Scan(&actions); err != nil || actions != 3 {
		t.Fatal("workspace decisions were not durably audited", actions, err)
	}
	database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 63); err == nil {
		t.Fatal("downgrade discarded workspace resources or evidence")
	}
	if _, err := restarted.GetWorkspace(ctx, scope, id); err != nil {
		t.Fatal("failed downgrade changed resources", err)
	}

	// A receipt failure after the resource insert must roll back the entire command.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_workspace_audit_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='workspace.created' THEN RAISE EXCEPTION 'fixture audit outage'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_workspace_audit_fixture BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION reject_workspace_audit_fixture()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS reject_workspace_audit_fixture ON audit_records; DROP FUNCTION IF EXISTS reject_workspace_audit_fixture()`); err != nil {
			t.Error(err)
		}
	})
	failed := input
	failed.ID = identity.New("wsp")
	failedKey := testIdempotency(scope, "workspace-rollback")
	if _, err := repo.CreateWorkspace(ctx, failedKey, failed); err == nil {
		t.Fatal("audit outage accepted creation")
	}
	var remains int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM workspaces WHERE isolation_domain_id=$1 AND id=$2)+(SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND aggregate_id=$2)+(SELECT count(*) FROM idempotency_records WHERE isolation_domain_id=$1 AND idempotency_key=$3)`, scope, failed.ID, failedKey.Key).Scan(&remains); err != nil || remains != 0 {
		t.Fatal("failed creation left partial state", remains, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM workspaces`); err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateDownTo(ctx, database, 63); err == nil {
		t.Fatal("downgrade removed authorization evidence after resource removal")
	}
}
