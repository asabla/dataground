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

func TestDurableExactRevisionReadScopesIdentityAndRetainsAudit(t *testing.T) {
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
	domainID, otherDomain, serviceID, revisionID := identity.New("iso"), identity.New("iso"), identity.New("svc"), identity.New("rev")
	for _, scope := range []string{domainID, otherDomain} {
		if _, err := pool.Exec(ctx, `INSERT INTO agent_services (isolation_domain_id,id,name,created_at,updated_at,created_by) VALUES ($1,$2,'exact-read',clock_timestamp(),clock_timestamp(),'operator')`, scope, serviceID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO service_revisions (isolation_domain_id,id,service_id,revision_number,state,runtime_profile,required_capabilities,input_schema,output_schema,created_at,updated_at,created_by) VALUES ($1,$2,$3,1,'draft','reference/v1',ARRAY['tool'],'{"type":"object","required":["prompt"]}','{"type":"object"}',clock_timestamp(),clock_timestamp(),'operator')`, scope, revisionID, serviceID); err != nil {
			t.Fatal(err)
		}
	}
	repository := persistence.NewRepository(pool)
	draft, err := repository.GetServiceRevision(ctx, domainID, revisionID)
	if err != nil || draft.Metadata.IsolationDomainID != domainID || draft.State != "draft" || draft.ServiceID != serviceID || !reflect.DeepEqual(draft.RequiredCapabilities, []string{"tool"}) || draft.InputSchema["type"] != "object" || draft.OutputSchema["type"] != "object" {
		t.Fatalf("draft read: %#v %v", draft, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE service_revisions SET state='published',published_at=clock_timestamp(),version=2,generation=2,updated_at=clock_timestamp() WHERE isolation_domain_id=$1 AND id=$2`, domainID, revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO service_revisions (isolation_domain_id,id,service_id,revision_number,state,runtime_profile,required_capabilities,created_at,updated_at,created_by) VALUES ($1,$2,$3,2,'draft','reference/v1',ARRAY[]::text[],clock_timestamp(),clock_timestamp(),'operator')`, domainID, identity.New("rev"), serviceID); err != nil {
		t.Fatal(err)
	}
	restarted := persistence.NewRepository(pool)
	published, err := restarted.GetServiceRevision(ctx, domainID, revisionID)
	if err != nil || published.State != "published" || published.PublishedAt == nil || published.RevisionNumber != 1 || !reflect.DeepEqual(published.InputSchema, draft.InputSchema) {
		t.Fatal("exact read changed definition or selected newest")
	}
	other, err := restarted.GetServiceRevision(ctx, otherDomain, revisionID)
	if err != nil || other.State != "draft" || other.Metadata.IsolationDomainID != otherDomain {
		t.Fatal("colliding revision identity crossed domains")
	}
	_, err = restarted.GetServiceRevision(ctx, identity.New("iso"), revisionID)
	var missing *persistence.DomainError
	if !errors.As(err, &missing) || missing.Code != "RESOURCE_NOT_FOUND" {
		t.Fatal("missing scope returned a revision")
	}
	if _, err := restarted.GetServiceRevision(ctx, "", revisionID); err == nil {
		t.Fatal("empty scope accepted")
	}
	if _, err := pool.Exec(ctx, `UPDATE service_revisions SET state='retired',version=3,generation=3,updated_at=clock_timestamp() WHERE isolation_domain_id=$1 AND id=$2`, domainID, revisionID); err != nil {
		t.Fatal(err)
	}
	const token = "development-exact-revision-read-test-token-thirty-two-bytes"
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
	path := "/v1/isolation-domains/" + domainID + "/service-revisions/" + revisionID
	response := read(path, http.StatusOK)
	var retired domain.ServiceRevision
	if err := json.Unmarshal(response.Body.Bytes(), &retired); err != nil {
		t.Fatal(err)
	}
	if retired.State != "retired" || retired.Metadata.Version != 3 || retired.Metadata.ID != revisionID || retired.PublishedAt == nil || !reflect.DeepEqual(retired.InputSchema, published.InputSchema) {
		t.Fatal("retired read lost definition or identity")
	}
	read("/v1/isolation-domains/"+otherDomain+"/service-revisions/"+revisionID, http.StatusForbidden)
	read("/v1/isolation-domains/"+domainID+"/service-revisions/"+identity.New("rev"), http.StatusNotFound)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_authorization_decisions WHERE isolation_domain_id=$1 AND action='readServiceRevision' AND resource_id=$2 AND outcome='allowed'`, domainID, revisionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("read decision count=%d err=%v", count, err)
	}
	migration, err := os.ReadFile("migrations/00049_revision_read.sql")
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
	if _, err := tx.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "cannot remove revision read authorization evidence") {
		t.Fatalf("downgrade lost read evidence: %v", err)
	}
}
