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
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceServicesAreDurableScopedAndSeparatelyAuthorized(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	t.Cleanup(func() {
		defer pool.Close()
		if _, err := pool.Exec(context.Background(), `TRUNCATE workspaces,api_authorization_decisions CASCADE`); err != nil {
			t.Error(err)
		}
	})
	repo := persistence.NewRepository(pool)
	scope, other, workspace := identity.New("iso"), identity.New("iso"), identity.New("wsp")
	for _, target := range []string{scope, other} {
		if _, err := repo.CreateWorkspace(ctx, testIdempotency(target, "workspace"), persistence.CreateWorkspaceInput{ID: workspace, Name: "Research", ActorID: "fixture", CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
	}
	input := persistence.CreateServiceInput{WorkspaceID: workspace, ID: identity.New("svc"), Name: "Analysis", ActorID: "fixture", CorrelationID: identity.New("cor")}
	key := testIdempotency(scope, "service")
	created, err := repo.CreateService(ctx, key, input)
	if err != nil {
		t.Fatal(err)
	}
	restarted := persistence.NewRepository(pool)
	replay, err := restarted.CreateService(ctx, key, input)
	if err != nil || !replay.Replayed || string(replay.Body) != string(created.Body) {
		t.Fatal("restart replay changed", err)
	}
	exact, err := restarted.GetService(ctx, scope, input.ID)
	if err != nil || exact.WorkspaceID != workspace {
		t.Fatal("exact read lost relationship", err)
	}
	foreign := input
	foreign.Name = "Foreign"
	if _, err := repo.CreateService(ctx, testIdempotency(other, "service"), foreign); err != nil {
		t.Fatal("scoped identity collision rejected", err)
	}
	second := input
	second.ID = identity.New("svc")
	if _, err := repo.CreateService(ctx, testIdempotency(scope, "second"), second); err != nil {
		t.Fatal(err)
	}
	unassigned := input
	unassigned.ID = identity.New("svc")
	unassigned.WorkspaceID = ""
	if _, err := repo.CreateService(ctx, testIdempotency(scope, "unassigned"), unassigned); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_services SET created_at='2026-01-01T00:00:00Z' WHERE isolation_domain_id=$1`, scope); err != nil {
		t.Fatal(err)
	}
	first, err := repo.ListWorkspaceServices(ctx, scope, workspace, nil, "", 1)
	if err != nil || len(first.Items) != 1 || !first.HasMore || first.Items[0].Name == "Foreign" {
		t.Fatal("first page failed", err)
	}
	last := first.Items[0]
	tail, err := restarted.ListWorkspaceServices(ctx, scope, workspace, &last.Metadata.CreatedAt, last.Metadata.ID, 1)
	if err != nil || len(tail.Items) != 1 || tail.HasMore || tail.Items[0].Metadata.ID == last.Metadata.ID || tail.Items[0].WorkspaceID != workspace {
		t.Fatal("continuation failed", err)
	}
	missing := input
	missing.ID = identity.New("svc")
	missing.WorkspaceID = identity.New("wsp")
	if _, err := repo.CreateWorkspace(ctx, testIdempotency(other, "foreign-only"), persistence.CreateWorkspaceInput{ID: missing.WorkspaceID, Name: "Foreign only", ActorID: "fixture", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateService(ctx, testIdempotency(scope, "missing"), missing); err == nil {
		t.Fatal("missing workspace accepted")
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_services SET workspace_id=$3 WHERE isolation_domain_id=$1 AND id=$2`, scope, input.ID, missing.WorkspaceID); err == nil {
		t.Fatal("foreign key allowed absent workspace")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM workspaces WHERE isolation_domain_id=$1 AND id=$2`, scope, workspace); err == nil {
		t.Fatal("workspace deletion orphaned services")
	}
	const token = "workspace-service-test-token-with-thirty-two-bytes"
	authentication, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: identity.New("usr"), IsolationDomainID: scope})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{PolicySetID: "workspace-services", Schema: authz.CanonicalAPICedarSchema(), Policies: []byte(`permit(principal, action in [DataGround::Action::"createWorkspaceAgentService",DataGround::Action::"listWorkspaceAgentServices"], resource);`)})
	if err != nil {
		t.Fatal(err)
	}
	audited, err := authz.NewAuditedAuthorizer(policy, restarted)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewDurableHandler(restarted, authentication, audited)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, key, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
		return w
	}
	root := "/v1/isolation-domains/" + scope
	path := root + "/workspaces/" + workspace + "/agent-services"
	response := call("POST", path, "http-service", `{"name":"HTTP analysis"}`, 201)
	var service domain.AgentService
	if json.Unmarshal(response.Body.Bytes(), &service) != nil || service.WorkspaceID != workspace {
		t.Fatal("HTTP response lost relationship")
	}
	if call("POST", path, "http-service", `{"name":"HTTP analysis"}`, 201).Body.String() != response.Body.String() {
		t.Fatal("HTTP replay changed")
	}
	call("GET", path, "", "", 200)
	call("POST", path, "oversized-description", `{"name":"Invalid","description":"`+strings.Repeat("x", 2049)+`"}`, 400)
	call("GET", root+"/agent-services/"+service.Metadata.ID, "", "", 403)
	call("GET", root+"/agent-services", "", "", 403)
	call("POST", root+"/agent-services", "denied", `{"name":"Denied"}`, 403)
	call("POST", root+"/workspaces/"+missing.WorkspaceID+"/agent-services", "http-missing", `{"name":"Absent"}`, 404)
	var actions int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT action) FROM api_authorization_decisions WHERE isolation_domain_id=$1 AND action IN ('createWorkspaceAgentService','listWorkspaceAgentServices') AND outcome='allowed'`, scope).Scan(&actions); err != nil || actions != 2 {
		t.Fatal("authorization evidence missing", actions, err)
	}
	database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 79); err == nil {
		t.Fatal("downgrade discarded relationships")
	}
	if _, err := repo.GetService(ctx, scope, input.ID); err != nil {
		t.Fatal("failed downgrade changed service", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE workspaces CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateDownTo(ctx, database, 79); err == nil {
		t.Fatal("downgrade discarded authorization evidence")
	}
}
