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

func TestDurableInvocationDiscoveryIsScopedStableAndRestartable(t *testing.T) {
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
	createdAt := time.Date(2026, 8, 1, 12, 0, 0, 123456000, time.UTC)
	ids := []string{"inv_00000000000000000003", "inv_00000000000000000002", "inv_00000000000000000001"}
	for _, scope := range [][2]string{{domainID, serviceID}, {otherDomain, serviceID}, {domainID, otherService}} {
		if _, err := pool.Exec(ctx, `INSERT INTO agent_services (isolation_domain_id,id,name,created_at,updated_at,created_by) VALUES ($1,$2,'history',$3,$3,'operator')`, scope[0], scope[1], createdAt); err != nil {
			t.Fatal(err)
		}
		revision := revisionID
		if scope[1] == otherService {
			revision = identity.New("rev")
		}
		if _, err := pool.Exec(ctx, `INSERT INTO service_revisions (isolation_domain_id,id,service_id,revision_number,state,runtime_profile,required_capabilities,created_at,updated_at,created_by) VALUES ($1,$2,$3,1,'published','reference/v1',ARRAY[]::text[],$4,$4,'operator')`, scope[0], revision, scope[1], createdAt); err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			invocationID := id
			if scope[1] == otherService {
				invocationID = identity.New("inv")
			}
			if _, err := pool.Exec(ctx, `INSERT INTO invocations (isolation_domain_id,id,service_id,revision_id,alias,state,input,result,correlation_id,operation_id,created_at,updated_at,created_by) VALUES ($1,$2,$3,$4,'stable','accepted','{"prompt":"private-content"}','{"text":"private-result"}',$5,$6,$7,$7,'operator')`, scope[0], invocationID, scope[1], revision, identity.New("cor"), identity.New("op"), createdAt); err != nil {
				t.Fatal(err)
			}
		}
	}
	repository := persistence.NewRepository(pool)
	first, err := repository.ListInvocations(ctx, domainID, serviceID, nil, "", 1)
	if err != nil || len(first.Items) != 1 || first.Items[0].Metadata.ID != ids[0] || !first.HasMore {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	// Mutable lifecycle changes do not alter the creation-based page boundary.
	if _, err := pool.Exec(ctx, `UPDATE invocations SET state='cancelled',completed_at=clock_timestamp() WHERE isolation_domain_id=$1 AND id=$2`, domainID, ids[1]); err != nil {
		t.Fatal(err)
	}
	restarted := persistence.NewRepository(pool)
	second, err := restarted.ListInvocations(ctx, domainID, serviceID, &first.Items[0].Metadata.CreatedAt, first.Items[0].Metadata.ID, 2)
	if err != nil || len(second.Items) != 2 || second.HasMore || second.Items[0].Metadata.ID != ids[1] || second.Items[0].State != "cancelled" || second.Items[0].CompletedAt == nil || second.Items[1].Metadata.ID != ids[2] {
		t.Fatalf("continuation = %#v, %v", second, err)
	}
	for _, item := range append(first.Items, second.Items...) {
		if item.Metadata.IsolationDomainID != domainID || item.ServiceID != serviceID {
			t.Fatal("history crossed scope")
		}
	}
	_, err = restarted.ListInvocations(ctx, identity.New("iso"), serviceID, nil, "", 1)
	var missing *persistence.DomainError
	if !errors.As(err, &missing) || missing.Code != "RESOURCE_NOT_FOUND" {
		t.Fatalf("missing scope = %v", err)
	}
	for _, limit := range []int{0, 101} {
		if _, err := restarted.ListInvocations(ctx, domainID, serviceID, nil, "", limit); err == nil {
			t.Fatal("unbounded listing accepted")
		}
	}
	if _, err := restarted.ListInvocations(ctx, domainID, serviceID, &createdAt, "", 1); err == nil {
		t.Fatal("incomplete cursor accepted")
	}

	const token = "development-history-test-token-with-thirty-two-bytes"
	actorID := identity.New("usr")
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: actorID, IsolationDomainID: domainID})
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := authz.NewDevelopmentCedarAuthorizer(actorID, domainID)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewDurableHandler(restarted, authenticator, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/isolation-domains/" + domainID + "/agent-services/" + serviceID + "/invocations?limit=1"
	read := func(path string) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("durable history status = %d", response.Code)
		}
		if strings.Contains(response.Body.String(), "private-") {
			t.Fatal("durable history disclosed invocation content")
		}
		var page struct {
			Items      []domain.InvocationSummary `json:"items"`
			NextCursor string                     `json:"nextCursor"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].Metadata.IsolationDomainID != domainID {
			t.Fatal("durable HTTP history was not bounded and scoped")
		}
		return page.NextCursor
	}
	cursor := read(path)
	if cursor == "" {
		t.Fatal("durable API omitted continuation")
	}
	read(path + "&cursor=" + cursor)

	// Verify the new audit action is accepted by the actual schema without
	// retaining fixture evidence that would intentionally prohibit downgrade.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO api_authorization_decisions (isolation_domain_id,principal_id,principal_kind,action,resource_type,resource_id,outcome,policy_set_id,policy_digest,correlation_id) VALUES ($1,$2,'human','listInvocations','DataGround::AgentService',$3,'allowed','history-test',$4,$5)`, domainID, actorID, serviceID, "sha256:"+strings.Repeat("a", 64), identity.New("cor")); err != nil {
		t.Fatalf("schema rejected discovery audit: %v", err)
	}
	migration, err := os.ReadFile("migrations/00045_invocation_discovery.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, down, found := strings.Cut(string(migration), "-- dataground:down")
	if !found {
		t.Fatal("discovery migration has no downgrade")
	}
	if _, err := tx.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "cannot remove invocation discovery authorization evidence") {
		t.Fatalf("downgrade did not preserve discovery audit: %v", err)
	}
}
