package persistence_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

// Only the native transport is scripted. Both concurrent requests go through
// real claims, HTTP authorization, the durable ledger and audited effect checks.
func TestApprovalBridgeDeliversIndependentHTTPDecisionsUnderOriginalClaim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixtureWithReservation(t, ctx, false)
	authorizer, _ := installQuestionAuthorizationFixture(t, ctx, fixture, `permit(principal,action,resource);`)
	t.Cleanup(func() {
		if _, err := fixture.pool.Exec(context.Background(), `TRUNCATE invocation_runtime_approvals,api_authorization_decisions,invocation_authorization_decisions`); err != nil {
			t.Error(err)
		}
	})
	actor := identity.New("usr")
	const token = "approval-bridge-disposable-development-token-thirty-two-bytes"
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: actor, IsolationDomainID: fixture.target.IsolationDomainID})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := authz.NewDevelopmentCedarAuthorizer(actor, fixture.target.IsolationDomainID)
	if err != nil {
		t.Fatal(err)
	}
	audited, err := authz.NewAuditedAuthorizer(policy, fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewDurableHandler(fixture.repository, authenticator, audited)
	if err != nil {
		t.Fatal(err)
	}
	turn := &approvalBridgeTurn{events: make(chan dgruntime.Event, 3), done: make(chan struct{}), active: map[string]bool{"approval-1": true, "approval-2": true}, decisions: map[string]dgruntime.ApprovalDecision{}}
	transport := &approvalBridgeTransport{questionBridgeTransport: questionBridgeTransport{scope: fixture.target.IsolationDomainID}, turn: turn}
	driver, err := reconcile.NewInvocationRuntimeDriver(fixture.repository, authorizer, reconcile.InvocationRuntimeRequestBuilderFunc(func(persistence.InvocationRuntimeTarget) (dgruntime.StartRequest, error) {
		return dgruntime.StartRequest{Prompt: "Scripted approval fixture", ApprovalMode: dgruntime.ApprovalInteractive, SandboxMode: dgruntime.SandboxReadOnly}, nil
	}), transport, transport, transport, transport, reconcile.InvocationRuntimeDriverConfig{LeaseDuration: time.Minute, RenewInterval: time.Second, Readiness: func(context.Context) error { return nil }, ApprovalStore: fixture.repository, ApprovalAuthorizer: authorizer})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		_, err := driver.ApplyClaimed(ctx, fixture.claim, fixture.effect)
		finished <- err
	}()
	defer func() {
		cancel()
		turn.Close()
		select {
		case <-joined:
		case <-time.After(time.Second):
			t.Error("approval driver did not stop")
		}
	}()
	request := func(method, path, body, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	// Resolve the second request first. A blocked event loop would never expose
	// it until the first controller had answered.
	for _, sequence := range []int{2, 1} {
		id := identity.Derived("apr", fixture.target.IsolationDomainID+":"+fixture.target.OperationID+":"+strconv.Itoa(sequence))
		path := "/v1/isolation-domains/" + fixture.target.IsolationDomainID + "/invocations/" + fixture.target.InvocationID + "/approvals/" + id
		for {
			response := request(http.MethodGet, path, "", "")
			if response.Code == http.StatusOK {
				break
			}
			if response.Code != http.StatusNotFound {
				t.Fatalf("approval read: %d", response.Code)
			}
			select {
			case err := <-finished:
				t.Fatalf("runtime exited before concurrent approval: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(5 * time.Millisecond):
			}
		}
		decision := "approve"
		if sequence == 1 {
			decision = "deny"
		}
		response := request(http.MethodPost, path, `{"expectedVersion":1,"decision":"`+decision+`"}`, "approval-bridge-decision-"+strconv.Itoa(sequence))
		if response.Code != http.StatusOK {
			t.Fatalf("approval receipt: %d %s", response.Code, response.Body.String())
		}
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for sequence := 1; sequence <= 2; sequence++ {
		id := identity.Derived("apr", fixture.target.IsolationDomainID+":"+fixture.target.OperationID+":"+strconv.Itoa(sequence))
		value, err := fixture.repository.GetInvocationRuntimeApproval(ctx, fixture.target.IsolationDomainID, id)
		decision := "approve"
		if sequence == 1 {
			decision = "deny"
		}
		if err != nil || value.State != "delivered" || value.Version != 4 || value.ResolvedBy != actor || value.Decision != decision || value.EffectiveDecision != decision {
			t.Fatalf("independent approval outcome: %s %v", value.State, err)
		}
	}
	turn.mu.Lock()
	calls, first, second := turn.calls, turn.decisions["approval-1"], turn.decisions["approval-2"]
	turn.mu.Unlock()
	if calls != 2 || first != dgruntime.ApprovalDeny || second != dgruntime.ApprovalApprove {
		t.Fatal("native routing substituted or repeated decisions")
	}
	var decisions, leaks int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM invocation_authorization_decisions WHERE isolation_domain_id=$1 AND operation_id=$2 AND actor_id=$3 AND action='approve' AND outcome='allowed'`, fixture.target.IsolationDomainID, fixture.target.OperationID, actor).Scan(&decisions); err != nil || decisions != 4 {
		t.Fatalf("entry/effect decision audit: %d %v", decisions, err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM invocation_events WHERE isolation_domain_id=$1 AND invocation_id=$2 AND (payload::text LIKE '%approval-1%' OR payload::text LIKE '%approval-2%')`, fixture.target.IsolationDomainID, fixture.target.InvocationID).Scan(&leaks); err != nil || leaks != 0 {
		t.Fatal("adapter handles reached public events")
	}
}

type approvalBridgeTransport struct {
	questionBridgeTransport
	turn *approvalBridgeTurn
}

func (transport *approvalBridgeTransport) New(execution.RuntimeSession) (reconcile.InvocationRuntimeAdapter, error) {
	return transport, nil
}
func (transport *approvalBridgeTransport) Start(_ context.Context, request dgruntime.StartRequest) (dgruntime.Turn, error) {
	if request.ApprovalMode != dgruntime.ApprovalInteractive {
		return nil, dgruntime.ErrApprovalMode
	}
	for sequence := 1; sequence <= 2; sequence++ {
		transport.turn.events <- dgruntime.Event{Sequence: uint64(sequence), Type: "interaction.approval.requested", Payload: map[string]any{"approvalId": "approval-" + strconv.Itoa(sequence), "action": "process.execute"}}
	}
	transport.turn.events <- dgruntime.Event{Sequence: 3, Type: "output.text.delta", Payload: map[string]any{"text": "Approval bridge complete."}}
	return transport.turn, nil
}

type approvalBridgeTurn struct {
	mu        sync.Mutex
	once      sync.Once
	events    chan dgruntime.Event
	done      chan struct{}
	active    map[string]bool
	decisions map[string]dgruntime.ApprovalDecision
	calls     int
}

func (turn *approvalBridgeTurn) Events() <-chan dgruntime.Event { return turn.events }
func (turn *approvalBridgeTurn) ApprovalPending(ctx context.Context, id string) (bool, error) {
	turn.mu.Lock()
	defer turn.mu.Unlock()
	return turn.active[id], ctx.Err()
}
func (turn *approvalBridgeTurn) ResolveApproval(ctx context.Context, id string, decision dgruntime.ApprovalDecision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	turn.mu.Lock()
	defer turn.mu.Unlock()
	if !turn.active[id] {
		return dgruntime.ErrApprovalNotFound
	}
	turn.calls++
	turn.decisions[id] = decision
	delete(turn.active, id)
	if len(turn.active) == 0 {
		turn.once.Do(func() { close(turn.done) })
	}
	return nil
}
func (turn *approvalBridgeTurn) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-turn.done:
		return nil
	}
}
func (turn *approvalBridgeTurn) Interrupt(context.Context) error { return turn.Close() }
func (turn *approvalBridgeTurn) Close() error {
	turn.mu.Lock()
	defer turn.mu.Unlock()
	clear(turn.active)
	turn.once.Do(func() { close(turn.done) })
	return nil
}
