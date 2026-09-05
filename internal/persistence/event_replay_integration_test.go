package persistence_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestDurableBoundedReplayContinuesAfterRestartWithoutSkippingEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	scope, serviceID, revisionID, invocationID, actor := identity.New("iso"), identity.New("svc"), identity.New("rev"), identity.New("inv"), identity.New("usr")
	if _, err := repository.CreateService(ctx, testIdempotency(scope, "replay-service"), persistence.CreateServiceInput{ID: serviceID, Name: "replay fixture", ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateRevision(ctx, testIdempotency(scope, "replay-revision"), persistence.CreateRevisionInput{ID: revisionID, ServiceID: serviceID, RuntimeProfile: "reference/v1", ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE service_revisions SET state='published' WHERE isolation_domain_id=$1 AND id=$2`, scope, revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AssignAlias(ctx, testIdempotency(scope, "replay-route"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: serviceID, RevisionID: revisionID, Name: "stable", ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptInvocation(ctx, testIdempotency(scope, "replay-invocation"), persistence.AcceptInvocationInput{ID: invocationID, ServiceID: serviceID, Alias: "stable", Input: map[string]any{}, ActorID: actor, CorrelationID: identity.New("cor"), Deadline: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// Seed a retained normalized journal larger than the Workbench's former
	// single-response record limit without executing a native turn.
	if _, err := pool.Exec(ctx, `
  INSERT INTO invocation_events (isolation_domain_id,invocation_id,id,sequence,schema_version,event_type,occurred_at,recorded_at,correlation_id,actor_id,service_id,revision_id,payload,source_kind,source_sequence)
  SELECT $1,$2,'evt_'||lpad(i::text,20,'0'),i,'dataground.event/v1','output.text.delta',clock_timestamp(),clock_timestamp(),'cor_replay',$3,$4,$5,'{"text":"retained journal"}'::jsonb,'runtime',i
  FROM generate_series(2,505) AS i
 `, scope, invocationID, actor, serviceID, revisionID); err != nil {
		t.Fatal(err)
	}
	const token = "bounded-event-replay-test-token-with-at-least-thirty-two-bytes"
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: actor, IsolationDomainID: scope})
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := authz.NewDevelopmentCedarAuthorizer(actor, scope)
	if err != nil {
		t.Fatal(err)
	}
	read := func(domainID, id string, cursor uint64, query string, authorized bool) *httptest.ResponseRecorder {
		t.Helper()
		// Each page uses a new repository/API instance to prove continuation does
		// not depend on a process-local cursor or retained session state.
		handler, err := api.NewDurableHandler(persistence.NewRepository(pool), authenticator, authorizer)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/isolation-domains/%s/invocations/%s/events%s", domainID, id, query), nil)
		if authorized {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		request.Header.Set("Last-Event-ID", strconv.FormatUint(cursor, 10))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	cursor := uint64(0)
	ids := map[string]bool{}
	for _, count := range []int{200, 200, 105} {
		response := read(scope, invocationID, cursor, "?limit=200", true)
		if response.Code != http.StatusOK || response.Body.Len() > 1<<20 {
			t.Fatalf("bounded response: %d %s", response.Code, response.Body.String())
		}
		frames := strings.Split(strings.TrimSpace(response.Body.String()), "\n\n")
		if len(frames) != count {
			t.Fatalf("page records: %d want %d", len(frames), count)
		}
		for _, frame := range frames {
			lines := strings.Split(frame, "\n")
			if len(lines) != 3 {
				t.Fatal("incomplete SSE frame")
			}
			var event domain.EventEnvelope
			if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[2], "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			if event.Sequence != cursor+1 || ids[event.ID] || event.IsolationDomainID != scope || event.InvocationID != invocationID {
				t.Fatalf("replay gap, duplicate or scope mismatch: %+v", event)
			}
			if (event.Sequence == 1 && event.Source != "platform") || (event.Sequence > 1 && event.Source != "runtime") {
				t.Fatal("journal source changed")
			}
			cursor = event.Sequence
			ids[event.ID] = true
		}
		if response.Header().Get("X-DataGround-Has-More") != strconv.FormatBool(cursor < 505) {
			t.Fatal("page continuation marker was wrong")
		}
	}
	empty := read(scope, invocationID, cursor, "?limit=200", true)
	if empty.Code != http.StatusOK || empty.Body.Len() != 0 || empty.Header().Get("X-DataGround-Has-More") != "false" {
		t.Fatal("empty continuation changed cursor semantics")
	}
	legacy := read(scope, invocationID, 0, "", true)
	if legacy.Code != http.StatusOK || strings.Count(legacy.Body.String(), "\nevent:") != 505 || legacy.Header().Get("X-DataGround-Has-More") != "" {
		t.Fatal("legacy unpaged replay was truncated")
	}
	if denied := read(scope, invocationID, 0, "?limit=200", false); denied.Code == http.StatusOK || strings.Contains(denied.Body.String(), "retained journal") {
		t.Fatal("page bypassed authentication")
	}
	if denied := read(identity.New("iso"), invocationID, 0, "?limit=200", true); denied.Code == http.StatusOK || strings.Contains(denied.Body.String(), "retained journal") {
		t.Fatal("page crossed isolation domains")
	}
	if missing := read(scope, identity.New("inv"), 0, "?limit=200", true); missing.Code != http.StatusNotFound {
		t.Fatalf("missing invocation page: %d", missing.Code)
	}
	for _, query := range []string{"?limit=0", "?limit=501", "?limit=1&limit=2"} {
		if response := read(scope, invocationID, 0, query, true); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid page query: %s %d", query, response.Code)
		}
	}
	for _, limit := range []int{0, 502} {
		if _, err := repository.ListEventsBounded(ctx, scope, invocationID, 0, limit); err == nil {
			t.Fatal("unbounded record request accepted")
		}
	}
}
