package persistence_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPublicWorkspaceRetentionRecovery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, previous persistence.OperationClaim) (*persistence.OperationClaim, func()) {
	t.Helper()
	repo := persistence.NewRepository(pool)
	if err := repo.Fail(ctx, previous, persistence.OperationFailureDeadline); err != nil {
		t.Fatal(err)
	}
	failed, err := repo.GetInvocation(ctx, previous.IsolationDomainID, previous.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	const token = "public-retention-recovery-test-token-thirty-two-bytes"
	handler := func(actor, action string) http.Handler {
		t.Helper()
		authentication, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: actor, IsolationDomainID: previous.IsolationDomainID})
		if err != nil {
			t.Fatal(err)
		}
		policy := fmt.Sprintf(`permit(principal == DataGround::User::"%s",action in [DataGround::Action::"%s",DataGround::Action::"readWorkspaceRetention"],resource) when { context.isolationDomainID == "%s" };`, actor, action, previous.IsolationDomainID)
		authorizer, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{PolicySetID: "public-retention-recovery-test", Schema: authz.CanonicalAPICedarSchema(), Policies: []byte(policy)})
		if err != nil {
			t.Fatal(err)
		}
		audited, err := authz.NewAuditedAuthorizer(authorizer, repo)
		if err != nil {
			t.Fatal(err)
		}
		value, err := api.NewDurableHandler(repo, authentication, audited)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	allowed := handler("recovery-operator", "requestWorkspaceRetentionRecovery")
	metadataOnly := handler("recovery-operator", "readWorkspaceRetention")
	otherActor := handler("other-recovery-operator", "requestWorkspaceRetentionRecovery")
	path := "/v1/isolation-domains/" + previous.IsolationDomainID + "/invocations/" + previous.ResourceID + "/workspace-retention/recoveries"
	body := fmt.Sprintf(`{"expectedVersion":%d}`, failed.Metadata.Version)
	for _, check := range []struct {
		handler http.Handler
		access  string
	}{{allowed, "allowed"}, {metadataOnly, "denied"}} {
		request := httptest.NewRequest(http.MethodGet, strings.TrimSuffix(path, "/recoveries"), nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		check.handler.ServeHTTP(response, request)
		var status domain.WorkspaceRetention
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || response.Code != 200 || status.RecoveryAccess != check.access {
			t.Fatal("recovery permission was not scoped to the current principal", err)
		}
	}

	request := func(h http.Handler, p, b, key string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, p, strings.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, test := range []struct {
		h               http.Handler
		path, body, key string
		auth            bool
		status          int
	}{
		{allowed, path, body, "public-recovery", false, 401},
		{metadataOnly, path, "invalid", "public-recovery", true, 403},
		{allowed, strings.Replace(path, previous.IsolationDomainID, identity.New("iso"), 1), body, "public-recovery", true, 403},
		{allowed, path, `{"expectedVersion":0}`, "public-recovery", true, 400},
		{allowed, path, `{"expectedVersion":1,"ownerBinding":"private"}`, "public-recovery", true, 400},
		{allowed, path, body, "", true, 400},
		{allowed, path, fmt.Sprintf(`{"expectedVersion":%d}`, failed.Metadata.Version-1), "stale-recovery", true, 409},
	} {
		if result := request(test.h, test.path, test.body, test.key, test.auth); result.Code != test.status {
			t.Fatalf("recovery boundary status=%d expected=%d", result.Code, test.status)
		}
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_recovery_permission_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='requestWorkspaceRetentionRecovery' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_recovery_permission_audit BEFORE INSERT ON api_authorization_decisions FOR EACH ROW EXECUTE FUNCTION reject_recovery_permission_audit()`); err != nil {
		t.Fatal(err)
	}
	permissionRequest := httptest.NewRequest(http.MethodGet, strings.TrimSuffix(path, "/recoveries"), nil)
	permissionRequest.Header.Set("Authorization", "Bearer "+token)
	permissionResponse := httptest.NewRecorder()
	allowed.ServeHTTP(permissionResponse, permissionRequest)
	var unavailable domain.WorkspaceRetention
	if err := json.Unmarshal(permissionResponse.Body.Bytes(), &unavailable); err != nil || permissionResponse.Code != 200 || unavailable.RecoveryAccess != "unavailable" {
		t.Fatal("missing permission audit exposed a recovery action", err)
	}
	if result := request(allowed, path, body, "public-recovery", true); result.Code != 503 {
		t.Fatal("missing authorization audit accepted recovery", result.Code)
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_recovery_permission_audit() CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_public_recovery_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='workspace.retention-recovery.accepted' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_public_recovery_audit BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION reject_public_recovery_audit()`); err != nil {
		t.Fatal(err)
	}
	if result := request(allowed, path, body, "public-recovery", true); result.Code != 500 {
		t.Fatal("failed mutation audit accepted recovery", result.Code)
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_public_recovery_audit() CASCADE`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspace_retention_recoveries WHERE isolation_domain_id=$1`, previous.IsolationDomainID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed recovery retained receipt", err)
	}
	var group sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 6)
	for range 6 {
		group.Add(1)
		go func() { defer group.Done(); results <- request(allowed, path, body, "public-recovery", true) }()
	}
	group.Wait()
	close(results)
	var first []byte
	for result := range results {
		if result.Code != 202 || result.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("public recovery was not accepted", result.Code)
		}
		if first == nil {
			first = append([]byte(nil), result.Body.Bytes()...)
		} else if !bytes.Equal(first, result.Body.Bytes()) {
			t.Fatal("exact public replay changed its receipt")
		}
	}
	var receipt domain.WorkspaceRetentionRecoveryReceipt
	if err := json.Unmarshal(first, &receipt); err != nil || receipt.InvocationID != previous.ResourceID || receipt.IsolationDomainID != previous.IsolationDomainID || receipt.InvocationVersion != int64(failed.Metadata.Version)+1 || receipt.State != "requested" || receipt.SchemaVersion != "dataground.workspace-retention-recovery-receipt/v1" || !receipt.DeadlineAt.After(time.Now()) || receipt.DeadlineAt.After(time.Now().Add(5*time.Minute)) {
		t.Fatal("invalid public recovery receipt", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(first, &fields); err != nil || len(fields) != 7 {
		t.Fatal("public receipt leaked additional fields", err)
	}
	for _, text := range []string{"ownerBinding", "input", "result", "sandbox", "runtimeToken"} {
		if strings.Contains(string(first), text) {
			t.Fatal("private recovery detail exposed")
		}
	}
	for _, result := range []*httptest.ResponseRecorder{
		request(allowed, path, fmt.Sprintf(`{"expectedVersion":%d}`, failed.Metadata.Version+1), "public-recovery", true),
		request(otherActor, path, body, "public-recovery", true),
		request(allowed, path, body, "different-request", true),
	} {
		if result.Code != 409 {
			t.Fatal("changed public request reused acceptance", result.Code)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspace_retention_recoveries WHERE isolation_domain_id=$1`, previous.IsolationDomainID).Scan(&count); err != nil || count != 1 {
		t.Fatal("public replay duplicated recovery", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND action='workspace.retention-recovery.accepted' AND actor_id='recovery-operator' AND correlation_id=$2`, previous.IsolationDomainID, receipt.CorrelationID).Scan(&count); err != nil || count != 1 {
		t.Fatal("public recovery lost validated attribution", err)
	}
	migration, err := os.ReadFile("migrations/00088_workspace_retention_command.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, down, found := strings.Cut(string(migration), "-- dataground:down")
	if !found {
		t.Fatal("missing downgrade")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, down)
	_ = tx.Rollback(ctx)
	if err == nil || !strings.Contains(err.Error(), "cannot remove workspace retention command authorization evidence") {
		t.Fatal("downgrade lost public recovery audit", err)
	}
	claim, err := repo.ClaimNextInIsolationDomain(ctx, previous.Kind, previous.IsolationDomainID, "public-retention-owner", time.Minute)
	if err != nil || claim == nil || claim.Command != "cancel" || claim.ActorID != "recovery-operator" || claim.CorrelationID != receipt.CorrelationID || claim.FencingToken <= previous.FencingToken || !claim.DeadlineAt.Equal(receipt.DeadlineAt) {
		t.Fatal("public recovery lost cancellation binding", err)
	}
	return claim, func() {
		result := request(allowed, path, body, "public-recovery", true)
		if result.Code != 202 || !bytes.Equal(first, result.Body.Bytes()) {
			t.Fatal("terminal replay changed acceptance")
		}
		value, err := repo.GetInvocation(ctx, previous.IsolationDomainID, previous.ResourceID)
		if err != nil || value.State != "cancelled" {
			t.Fatal("historical replay reopened cancellation", err)
		}
		if _, err := pool.Exec(ctx, `TRUNCATE api_authorization_decisions`); err != nil {
			t.Fatal(err)
		}
	}
}
