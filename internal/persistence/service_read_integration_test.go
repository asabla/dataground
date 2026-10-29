package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
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

func TestDurableExactServiceReadScopesIdentityAndRetainsAudit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url := testDatabaseURL(t)
	database, err := persistence.OpenSQL(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 0); err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		t.Fatal(err)
	}
	pool, err := persistence.OpenPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	domainID, otherDomain, serviceID := identity.New("iso"), identity.New("iso"), identity.New("svc")
	for _, scope := range []string{domainID, otherDomain} {
		if _, err := pool.Exec(ctx, `INSERT INTO agent_services (isolation_domain_id,id,name,created_at,updated_at,created_by) VALUES ($1,$2,$1,clock_timestamp(),clock_timestamp(),'operator')`, scope, serviceID); err != nil {
			t.Fatal(err)
		}
	}
	repository := persistence.NewRepository(pool)
	service, err := repository.GetService(ctx, domainID, serviceID)
	if err != nil || service.Metadata.IsolationDomainID != domainID || service.Name != domainID {
		t.Fatalf("exact service read: %v", err)
	}
	restarted := persistence.NewRepository(pool)
	other, err := restarted.GetService(ctx, otherDomain, serviceID)
	if err != nil || other.Metadata.IsolationDomainID != otherDomain || other.Name != otherDomain {
		t.Fatal("colliding service crossed scope")
	}
	_, err = restarted.GetService(ctx, identity.New("iso"), serviceID)
	var missing *persistence.DomainError
	if !errors.As(err, &missing) || missing.Code != "RESOURCE_NOT_FOUND" {
		t.Fatal("missing scope returned service")
	}
	if _, err := restarted.GetService(ctx, "", serviceID); err == nil {
		t.Fatal("empty scope accepted")
	}
	const token = "development-exact-service-read-test-token-thirty-two-bytes"
	actorID := identity.New("usr")
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: actorID, IsolationDomainID: domainID})
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := authz.NewDevelopmentCedarAuthorizer(actorID, domainID)
	if err != nil {
		t.Fatal(err)
	}
	audited, err := authz.NewAuditedAuthorizer(authorizer, restarted)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewDurableHandler(restarted, authenticator, audited)
	if err != nil {
		t.Fatal(err)
	}
	// Only the disposable fixture removes its retained decisions after proving downgrade protection.
	defer func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE api_authorization_decisions`); err != nil {
			t.Error(err)
		}
	}()
	read := func(path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("read status=%d body=%s", response.Code, response.Body.String())
		}
		return response
	}
	path := "/v1/isolation-domains/" + domainID + "/agent-services/" + serviceID
	response := read(path, http.StatusOK)
	var actual domain.AgentService
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Metadata.ID != serviceID || actual.Metadata.IsolationDomainID != domainID || actual.Name != domainID {
		t.Fatal("exact service response lost scope")
	}
	read("/v1/isolation-domains/"+otherDomain+"/agent-services/"+serviceID, http.StatusForbidden)
	read("/v1/isolation-domains/"+domainID+"/agent-services/"+identity.New("svc"), http.StatusNotFound)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_authorization_decisions WHERE isolation_domain_id=$1 AND action='readAgentService' AND resource_id=$2 AND outcome='allowed'`, domainID, serviceID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("read decision count=%d err=%v", count, err)
	}
	migration, err := os.ReadFile("migrations/00076_service_read.sql")
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
	if _, err := tx.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "cannot remove service read authorization evidence") {
		t.Fatalf("downgrade lost read evidence: %v", err)
	}
}
