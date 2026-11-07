package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testWorkspaceRetentionReadRoute(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var scope, invocation, artifact string
	if err := pool.QueryRow(ctx, `SELECT completion.isolation_domain_id,operation.invocation_id,completion.artifact_id FROM invocation_workspace_change_completions completion JOIN invocation_execution_operations operation ON operation.isolation_domain_id=completion.isolation_domain_id AND operation.id=completion.operation_id LIMIT 1`).Scan(&scope, &invocation, &artifact); err != nil {
		t.Fatal(err)
	}
	const token = "development-retention-metadata-token-thirty-two-bytes"
	actor := identity.New("usr")
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: actor, IsolationDomainID: scope})
	if err != nil {
		t.Fatal(err)
	}
	policy := fmt.Sprintf(`permit(principal == DataGround::User::"%s", action == DataGround::Action::"readWorkspaceRetention", resource) when { context.isolationDomainID == "%s" };`, actor, scope)
	authorizer, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{PolicySetID: "retention-metadata-test", Schema: authz.CanonicalAPICedarSchema(), Policies: []byte(policy)})
	if err != nil {
		t.Fatal(err)
	}
	repo := persistence.NewRepository(pool)
	audited, err := authz.NewAuditedAuthorizer(authorizer, repo)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewDurableHandler(repo, authenticator, audited)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE api_authorization_decisions`); err != nil {
			t.Error(err)
		}
	}()
	read := func(path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("retention route status=%d body=%s", response.Code, response.Body.String())
		}
		return response
	}
	prefix := "/v1/isolation-domains/" + scope
	response := read(prefix+"/invocations/"+invocation+"/workspace-retention", http.StatusOK)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 7 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("retention metadata leaked fields or cacheability")
	}
	for _, field := range []string{"schemaVersion", "isolationDomainId", "invocationId", "state", "recovery", "artifactId", "recordedAt"} {
		if fields[field] == nil {
			t.Fatal("missing public field", field)
		}
	}
	read(prefix+"/invocations/"+identity.New("inv")+"/workspace-retention", http.StatusNotFound)
	read("/v1/isolation-domains/"+identity.New("iso")+"/invocations/"+invocation+"/workspace-retention", http.StatusForbidden)
	read(prefix+"/invocations/"+invocation+"/artifacts/"+artifact+"/content", http.StatusForbidden)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_authorization_decisions WHERE isolation_domain_id=$1 AND action='readWorkspaceRetention' AND resource_id=$2 AND outcome='allowed'`, scope, invocation).Scan(&count); err != nil || count != 1 {
		t.Fatal("retention read lost audit", err)
	}
	migration, err := os.ReadFile("migrations/00087_workspace_retention_read.sql")
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
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "cannot remove workspace retention read authorization evidence") {
		t.Fatal("downgrade discarded retention read audit", err)
	}
}

func assertWorkspaceRetention(t *testing.T, ctx context.Context, repo *persistence.Repository, claim persistence.OperationClaim, state, recovery string) {
	t.Helper()
	value, err := repo.ReadWorkspaceRetention(ctx, claim.IsolationDomainID, claim.ResourceID)
	if err != nil || value.SchemaVersion != "dataground.invocation-workspace-retention/v1" || value.IsolationDomainID != claim.IsolationDomainID || value.InvocationID != claim.ResourceID || value.State != state || value.Recovery != recovery {
		t.Fatalf("retention metadata: %#v, %v", value, err)
	}
	if state == "recorded" {
		entry, err := repo.GetWorkspaceChangeExport(ctx, claim.IsolationDomainID, claim.ID)
		if err != nil || !entry.Ready || value.ArtifactID != entry.Record.ArtifactRecord().ID || value.RecordedAt == nil || value.RecordedAt.IsZero() {
			t.Fatal("recorded retention lost its artifact receipt", err)
		}
	} else if value.ArtifactID != "" || value.RecordedAt != nil {
		t.Fatal("unfinished retention claims a completed artifact")
	}
	if _, err := repo.ReadWorkspaceRetention(ctx, identity.New("iso"), claim.ResourceID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("retention metadata crossed scope", err)
	}
	if _, err := repo.ReadWorkspaceRetention(ctx, claim.IsolationDomainID, identity.New("inv")); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("missing invocation returned retention metadata", err)
	}
}
