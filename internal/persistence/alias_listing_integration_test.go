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

func TestDurableAliasDiscoveryIsScopedAndContinuesAfterWithdrawal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL := testDatabaseURL(t)
	database, err := persistence.OpenSQL(ctx, databaseURL)
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
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	domainID, otherDomain := identity.New("iso"), identity.New("iso")
	serviceID, otherService := identity.New("svc"), identity.New("svc")
	revisionID := identity.New("rev")
	createdAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for _, scope := range [][2]string{{domainID, serviceID}, {otherDomain, serviceID}, {domainID, otherService}} {
		if _, err := pool.Exec(ctx, `INSERT INTO agent_services (isolation_domain_id,id,name,created_at,updated_at,created_by) VALUES ($1,$2,'routes',$3,$3,'operator')`, scope[0], scope[1], createdAt); err != nil {
			t.Fatal(err)
		}
		revision := revisionID
		if scope[1] == otherService {
			revision = identity.New("rev")
		}
		if _, err := pool.Exec(ctx, `INSERT INTO service_revisions (isolation_domain_id,id,service_id,revision_number,state,runtime_profile,required_capabilities,created_at,updated_at,created_by) VALUES ($1,$2,$3,1,'published','reference/v1',ARRAY[]::text[],$4,$4,'operator')`, scope[0], revision, scope[1], createdAt); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"z", "a0", "a-1", "a"} {
			if _, err := pool.Exec(ctx, `INSERT INTO service_aliases (isolation_domain_id,id,service_id,name,revision_id,created_at,updated_at,created_by) VALUES ($1,$2,$3,$4,$5,$6,$6,'operator')`, scope[0], identity.New("als"), scope[1], name, revision, createdAt); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Cleanup is confined to the disposable integration database; production downgrade must retain evidence.
	defer func() {
		if _, err := pool.Exec(context.Background(), `UPDATE service_aliases SET withdrawn_at=NULL; TRUNCATE api_authorization_decisions`); err != nil {
			t.Error(err)
		}
	}()
	repository := persistence.NewRepository(pool)
	first, err := repository.ListServiceAliases(ctx, domainID, serviceID, "", 2)
	if err != nil || len(first.Items) != 2 || first.Items[0].Name != "a" || first.Items[1].Name != "a-1" || !first.HasMore {
		t.Fatalf("first page: %#v, %v", first, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE service_aliases SET withdrawn_at=clock_timestamp(),version=version+1,generation=generation+1 WHERE isolation_domain_id=$1 AND service_id=$2 AND name IN ('a-1','z')`, domainID, serviceID); err != nil {
		t.Fatal(err)
	}
	restarted := persistence.NewRepository(pool)
	second, err := restarted.ListServiceAliases(ctx, domainID, serviceID, first.Items[1].Name, 2)
	if err != nil || len(second.Items) != 1 || second.Items[0].Name != "a0" || second.HasMore {
		t.Fatalf("continuation: %#v, %v", second, err)
	}
	for _, item := range append(first.Items, second.Items...) {
		if item.ServiceID != serviceID || item.Metadata.IsolationDomainID != domainID || item.WithdrawnAt != nil {
			t.Fatal("discovery crossed scope or included withdrawn alias")
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE service_aliases SET withdrawn_at=NULL,version=version+1,generation=generation+1 WHERE isolation_domain_id=$1 AND service_id=$2 AND name='a-1'`, domainID, serviceID); err != nil {
		t.Fatal(err)
	}
	refreshed, err := restarted.ListServiceAliases(ctx, domainID, serviceID, "", 100)
	if err != nil || len(refreshed.Items) != 3 || refreshed.Items[1].Name != "a-1" || refreshed.Items[1].Metadata.Version != 3 {
		t.Fatalf("recreated route not discoverable: %#v, %v", refreshed, err)
	}
	_, err = restarted.ListServiceAliases(ctx, identity.New("iso"), serviceID, "", 1)
	var missing *persistence.DomainError
	if !errors.As(err, &missing) || missing.Code != "RESOURCE_NOT_FOUND" {
		t.Fatalf("missing scope: %v", err)
	}
	for _, limit := range []int{0, 101} {
		if _, err := restarted.ListServiceAliases(ctx, domainID, serviceID, "", limit); err == nil {
			t.Fatal("unbounded listing accepted")
		}
	}
	for _, name := range []string{"Bad", "a-", strings.Repeat("a", 64)} {
		if _, err := restarted.ListServiceAliases(ctx, domainID, serviceID, name, 1); err == nil {
			t.Fatal("invalid boundary accepted")
		}
	}

	const token = "development-alias-list-test-token-with-thirty-two-bytes"
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
	type page struct {
		Items      []domain.ServiceAlias `json:"items"`
		NextCursor string                `json:"nextCursor"`
	}
	path := "/v1/isolation-domains/" + domainID + "/agent-services/" + serviceID + "/aliases"
	read := func(path string, status int) page {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("HTTP status=%d body=%s", response.Code, response.Body.String())
		}
		var result page
		if status == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	firstHTTP := read(path+"?limit=1", http.StatusOK)
	if len(firstHTTP.Items) != 1 || firstHTTP.Items[0].Name != "a" || firstHTTP.NextCursor == "" {
		t.Fatal("HTTP first page invalid")
	}
	nextHTTP := read(path+"?limit=2&cursor="+firstHTTP.NextCursor, http.StatusOK)
	if len(nextHTTP.Items) != 2 || nextHTTP.Items[0].Name != "a-1" || nextHTTP.Items[1].Name != "a0" || nextHTTP.NextCursor != "" {
		t.Fatal("HTTP continuation invalid")
	}
	read(path+"?limit=%ZZ", http.StatusBadRequest)
	read("/v1/isolation-domains/"+domainID+"/agent-services/"+otherService+"/aliases?cursor="+firstHTTP.NextCursor, http.StatusBadRequest)
	read("/v1/isolation-domains/"+otherDomain+"/agent-services/"+serviceID+"/aliases", http.StatusForbidden)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_authorization_decisions WHERE isolation_domain_id=$1 AND action='listServiceAliases' AND resource_id=$2 AND outcome='allowed'`, domainID, serviceID).Scan(&count); err != nil || count != 3 {
		t.Fatalf("list audit count=%d err=%v", count, err)
	}
	migration, err := os.ReadFile("migrations/00048_alias_discovery.sql")
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
	if _, err := tx.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "cannot remove alias discovery authorization evidence") {
		t.Fatalf("downgrade lost audit: %v", err)
	}
}
