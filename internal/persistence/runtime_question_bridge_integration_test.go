package persistence_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

// Only the runtime transport is scripted. The driver, claims, question ledger,
// HTTP boundary, policy resolution, both decision streams and receipts are real.
func TestQuestionBridgeRecordsHTTPAnswerAndDeliversUnderTheOriginalClaim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixtureWithReservation(t, ctx, false)
	authorizer, _ := installQuestionAuthorizationFixture(t, ctx, fixture, `permit(principal, action, resource);`)
	t.Cleanup(func() {
		if _, err := fixture.pool.Exec(context.Background(), `TRUNCATE api_authorization_decisions, invocation_authorization_decisions`); err != nil {
			t.Error(err)
		}
	})
	actor := identity.New("usr")
	const token = "question-bridge-disposable-development-token-thirty-two-bytes"
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: actor, IsolationDomainID: fixture.target.IsolationDomainID})
	if err != nil {
		t.Fatal(err)
	}
	apiPolicy, err := authz.NewDevelopmentCedarAuthorizer(actor, fixture.target.IsolationDomainID)
	if err != nil {
		t.Fatal(err)
	}
	apiAuthorizer, err := authz.NewAuditedAuthorizer(apiPolicy, fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewDurableHandler(fixture.repository, authenticator, apiAuthorizer)
	if err != nil {
		t.Fatal(err)
	}
	turn := &questionBridgeTurn{events: make(chan dgruntime.Event, 2), done: make(chan struct{}), active: true}
	transport := &questionBridgeTransport{scope: fixture.target.IsolationDomainID, turn: turn}
	driver, err := reconcile.NewInvocationRuntimeDriver(fixture.repository, authorizer, reconcile.InvocationRuntimeRequestBuilderFunc(func(persistence.InvocationRuntimeTarget) (dgruntime.StartRequest, error) {
		return dgruntime.StartRequest{Prompt: "Scripted bridge fixture", QuestionMode: dgruntime.QuestionInteractive, QuestionTimeout: 10 * time.Second, ApprovalMode: dgruntime.ApprovalLocked, SandboxMode: dgruntime.SandboxReadOnly}, nil
	}), transport, transport, transport, transport, reconcile.InvocationRuntimeDriverConfig{LeaseDuration: time.Minute, RenewInterval: time.Second, Readiness: func(context.Context) error { return nil }, QuestionStore: fixture.repository, QuestionAuthorizer: authorizer})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	driverDone := make(chan struct{})
	go func() {
		defer close(driverDone)
		_, err := driver.ApplyClaimed(ctx, fixture.claim, fixture.effect)
		finished <- err
	}()
	defer func() {
		cancel()
		turn.Close()
		select {
		case <-driverDone:
		case <-time.After(time.Second):
			t.Error("question driver did not stop")
		}
	}()
	questionID := identity.Derived("qst", fixture.target.IsolationDomainID+":"+fixture.target.OperationID+":1")
	path := "/v1/isolation-domains/" + fixture.target.IsolationDomainID + "/invocations/" + fixture.target.InvocationID + "/questions/" + questionID
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "question-bridge-answer-0001")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	for {
		response := request(http.MethodGet, path, "")
		if response.Code == 200 {
			break
		}
		if response.Code != 404 {
			t.Fatalf("question bridge read: %d %s", response.Code, response.Body.String())
		}
		select {
		case err := <-finished:
			t.Fatalf("runtime exited before its question: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	response := request(http.MethodPost, path+"/answers", `{"expectedVersion":1,"answers":[{"questionId":"item_1","text":"private answer sentinel"}]}`)
	if response.Code != 200 || strings.Contains(response.Body.String(), "private answer sentinel") {
		t.Fatalf("question bridge answer receipt: %d", response.Code)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	value, err := fixture.repository.GetInvocationRuntimeQuestion(ctx, fixture.target.IsolationDomainID, fixture.target.InvocationID, questionID)
	if err != nil || value.State != "delivered" || value.Version != 4 || value.AnsweredBy != actor {
		t.Fatalf("question bridge final state: %s %v", value.State, err)
	}
	turn.mu.Lock()
	answerCalls, answerID, answerText := turn.answerCalls, turn.answerID, turn.answerText
	turn.mu.Unlock()
	if answerCalls != 1 || answerID != "question-1" || answerText != "private answer sentinel" {
		t.Fatal("native bridge lost the exact answer or repeated it")
	}
	var entry, effect int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE phase='entry'), count(*) FILTER (WHERE phase='effect') FROM invocation_question_authorization_decisions WHERE isolation_domain_id=$1 AND question_id=$2 AND actor_id=$3 AND outcome='allowed'`, value.IsolationDomainID, value.ID, actor).Scan(&entry, &effect); err != nil {
		t.Fatal(err)
	}
	if entry != 1 || effect != 1 {
		t.Fatalf("actual controller question decisions: %d/%d", entry, effect)
	}
	var privateEvents int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM invocation_events WHERE isolation_domain_id=$1 AND invocation_id=$2 AND (payload::text LIKE '%private prompt sentinel%' OR payload::text LIKE '%private answer sentinel%' OR payload::text LIKE '%question-1%')`, value.IsolationDomainID, value.InvocationID).Scan(&privateEvents); err != nil || privateEvents != 0 {
		t.Fatal("private question data entered the journal")
	}
}

type questionBridgeTransport struct {
	scope string
	turn  *questionBridgeTurn
}

func (transport *questionBridgeTransport) GetExecutionByOperation(context.Context, string, string) (execution.Execution, error) {
	return execution.Execution{IsolationDomainID: transport.scope, ID: "exe_question_fixture", State: "ready"}, nil
}
func (transport *questionBridgeTransport) Observe(_ context.Context, ref execution.ExecutionRef) (execution.Observation, error) {
	return execution.Observation{IsolationDomainID: ref.IsolationDomainID, ExecutionID: ref.ID, State: "ready"}, nil
}
func (*questionBridgeTransport) StartRuntime(context.Context, execution.ExecutionRef) (execution.RuntimeSession, error) {
	return questionBridgeSession{}, nil
}
func (*questionBridgeTransport) Export(context.Context, execution.ExportRequest) (execution.ExportResult, error) {
	return execution.ExportResult{}, errors.New("unexpected export")
}
func (*questionBridgeTransport) Finalize(context.Context, artifact.Finalization) (artifact.Record, error) {
	return artifact.Record{}, errors.New("unexpected artifact")
}
func (transport *questionBridgeTransport) New(execution.RuntimeSession) (reconcile.InvocationRuntimeAdapter, error) {
	return transport, nil
}
func (transport *questionBridgeTransport) Start(_ context.Context, request dgruntime.StartRequest) (dgruntime.Turn, error) {
	if request.QuestionMode != dgruntime.QuestionInteractive {
		return nil, dgruntime.ErrQuestionMode
	}
	transport.turn.events <- dgruntime.Event{Sequence: 1, Type: "interaction.question.requested", Payload: map[string]any{"questionId": "question-1", "questions": questionPrompts(), "expiresAt": time.Now().Add(request.QuestionTimeout).UTC().Truncate(time.Microsecond).Add(-123 * time.Nanosecond).Format(time.RFC3339Nano)}}
	transport.turn.events <- dgruntime.Event{Sequence: 2, Type: "output.text.delta", Payload: map[string]any{"text": "Question bridge complete."}}
	return transport.turn, nil
}
func (*questionBridgeTransport) Close() error { return nil }

type questionBridgeSession struct{}
type questionBridgeInput struct{ io.Writer }

func (questionBridgeInput) Close() error            { return nil }
func (questionBridgeSession) Input() io.WriteCloser { return questionBridgeInput{io.Discard} }
func (questionBridgeSession) Output() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }
func (questionBridgeSession) Errors() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }
func (questionBridgeSession) Wait() error           { return nil }
func (questionBridgeSession) Close() error          { return nil }

type questionBridgeTurn struct {
	mu                   sync.Mutex
	once                 sync.Once
	events               chan dgruntime.Event
	done                 chan struct{}
	active               bool
	answerCalls          int
	answerID, answerText string
}

func (turn *questionBridgeTurn) Events() <-chan dgruntime.Event { return turn.events }
func (*questionBridgeTurn) ResolveApproval(context.Context, string, dgruntime.ApprovalDecision) error {
	return dgruntime.ErrApprovalMode
}
func (turn *questionBridgeTurn) QuestionPending(ctx context.Context, id string) (bool, error) {
	turn.mu.Lock()
	defer turn.mu.Unlock()
	return turn.active && id == "question-1", ctx.Err()
}
func (turn *questionBridgeTurn) AnswerQuestion(ctx context.Context, id string, answers []domain.QuestionAnswer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	turn.mu.Lock()
	defer turn.mu.Unlock()
	if !turn.active || id != "question-1" || len(answers) != 1 || answers[0].Text == nil {
		return dgruntime.ErrQuestionNotFound
	}
	turn.answerCalls++
	turn.answerID = id
	turn.answerText = *answers[0].Text
	turn.active = false
	turn.once.Do(func() { close(turn.done) })
	return nil
}
func (turn *questionBridgeTurn) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-turn.done:
		return nil
	}
}
func (turn *questionBridgeTurn) Interrupt(context.Context) error { return turn.Close() }
func (turn *questionBridgeTurn) Close() error {
	turn.mu.Lock()
	defer turn.mu.Unlock()
	turn.active = false
	turn.once.Do(func() { close(turn.done) })
	return nil
}
