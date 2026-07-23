package reconcile

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestInvocationRuntimeDriverRunsOneFencedTurn(t *testing.T) {
	claim, effect, target := runtimeDriverFixture()
	store := &runtimeStoreStub{target: target, attemptErr: persistence.ErrInvocationRuntimeAttemptMissing}
	authorizer := &runtimeAuthorizerStub{}
	provider := &runtimeProviderStub{
		observation: execution.Observation{
			IsolationDomainID: target.IsolationDomainID,
			ExecutionID:       "exe_runtime",
			State:             "ready",
		},
	}
	turn := &runtimeTurnStub{
		events: runtimeEvents(
			dgruntime.Event{Sequence: 1, Type: "output.text.delta", Payload: map[string]any{"text": "persisted output"}},
			dgruntime.Event{Sequence: 2, Type: "lifecycle.succeeded", Payload: map[string]any{"message": "finished"}},
		),
	}
	adapter := &runtimeAdapterStub{turn: turn}
	driver := newRuntimeDriverForTest(
		t,
		store,
		authorizer,
		&runtimeExecutionSourceStub{value: execution.Execution{
			IsolationDomainID: target.IsolationDomainID,
			ID:                "exe_runtime",
			State:             "ready",
		}},
		provider,
		&runtimeAdapterFactoryStub{adapter: adapter},
	)

	if result, found, err := driver.ObserveClaimed(context.Background(), claim, effect); err != nil || found || result != nil {
		t.Fatalf("observe unused attempt = (%#v, %t, %v)", result, found, err)
	}
	result, err := driver.ApplyClaimed(context.Background(), claim, effect)
	if err != nil {
		t.Fatal(err)
	}
	output, ok := result["output"].(map[string]any)
	if result["status"] != "succeeded" || !ok || output["text"] != "persisted output" ||
		store.renewCalls != 1 || store.beginCalls != 1 || store.completeCalls != 1 ||
		store.failCalls != 0 {
		t.Fatalf("runtime completion = result %#v, store %#v", result, store)
	}
	if len(store.events) != 2 || store.events[0].SourceSequence != 1 || store.events[1].SourceSequence != 2 {
		t.Fatalf("runtime events = %#v", store.events)
	}
	if authorizer.calls != 1 || provider.observeCalls != 1 || provider.startCalls != 1 || adapter.startCalls != 1 {
		t.Fatalf(
			"runtime calls = authorization:%d observe:%d session:%d turn:%d",
			authorizer.calls,
			provider.observeCalls,
			provider.startCalls,
			adapter.startCalls,
		)
	}
	if adapter.request.Prompt != "persisted prompt" || adapter.request.ApprovalMode != dgruntime.ApprovalLocked {
		t.Fatalf("runtime request = %#v", adapter.request)
	}
	if !reflect.DeepEqual(authorizer.request, adapter.request) {
		t.Fatalf("authorized request = %#v, adapter request = %#v", authorizer.request, adapter.request)
	}
}

func TestInvocationRuntimeDriverPersistsDeterministicTurnFailure(t *testing.T) {
	claim, effect, target := runtimeDriverFixture()
	store := &runtimeStoreStub{target: target}
	turn := &runtimeTurnStub{
		events: runtimeEvents(dgruntime.Event{
			Sequence: 1,
			Type:     "lifecycle.failed",
			Payload:  map[string]any{"code": "RUNTIME_TURN_FAILED", "retryable": false},
		}),
		waitErr: dgruntime.ErrTurnFailed,
	}
	driver := newRuntimeDriverForTest(
		t,
		store,
		&runtimeAuthorizerStub{},
		&runtimeExecutionSourceStub{value: execution.Execution{
			IsolationDomainID: target.IsolationDomainID,
			ID:                "exe_runtime",
			State:             "ready",
		}},
		&runtimeProviderStub{observation: execution.Observation{
			IsolationDomainID: target.IsolationDomainID,
			ExecutionID:       "exe_runtime",
			State:             "ready",
		}},
		&runtimeAdapterFactoryStub{adapter: &runtimeAdapterStub{turn: turn}},
	)

	_, err := driver.ApplyClaimed(context.Background(), claim, effect)
	if !errors.Is(err, ErrEffectTerminal) || !errors.Is(err, dgruntime.ErrTurnFailed) {
		t.Fatalf("runtime failure = %v", err)
	}
	if store.failCalls != 1 || store.completeCalls != 0 ||
		store.attempt.Result["code"] != "RUNTIME_TURN_FAILED" {
		t.Fatalf("failed attempt = %#v", store.attempt)
	}
}

func TestInvocationRuntimeDriverRejectsInvalidDeclaredOutput(t *testing.T) {
	claim, effect, target := runtimeDriverFixture()
	target.OutputSchema = map[string]any{
		"type":     "object",
		"required": []any{"answer"},
		"properties": map[string]any{
			"answer": map[string]any{"type": "string"},
		},
	}
	store := &runtimeStoreStub{target: target}
	turn := &runtimeTurnStub{events: runtimeEvents(
		dgruntime.Event{
			Sequence: 1,
			Type:     "output.text.delta",
			Payload:  map[string]any{"text": "{\"answer\":42}"},
		},
		dgruntime.Event{
			Sequence: 2,
			Type:     "lifecycle.succeeded",
			Payload:  map[string]any{"message": "finished"},
		},
	)}
	driver := newRuntimeDriverForTest(
		t,
		store,
		&runtimeAuthorizerStub{},
		&runtimeExecutionSourceStub{value: execution.Execution{
			IsolationDomainID: target.IsolationDomainID,
			ID:                "exe_runtime",
			State:             "ready",
		}},
		&runtimeProviderStub{observation: execution.Observation{
			IsolationDomainID: target.IsolationDomainID,
			ExecutionID:       "exe_runtime",
			State:             "ready",
		}},
		&runtimeAdapterFactoryStub{adapter: &runtimeAdapterStub{turn: turn}},
	)

	_, err := driver.ApplyClaimed(context.Background(), claim, effect)
	if !errors.Is(err, ErrEffectTerminal) ||
		!errors.Is(err, ErrInvocationRuntimeOutputInvalid) {
		t.Fatalf("invalid declared output = %v", err)
	}
	if store.failCalls != 1 || store.completeCalls != 0 ||
		store.attempt.Result["code"] != "RUNTIME_OUTPUT_INVALID" {
		t.Fatalf("invalid output attempt = %#v", store.attempt)
	}
}

func TestInvocationRuntimeDriverNeverRepeatsReservedAttempt(t *testing.T) {
	claim, effect, target := runtimeDriverFixture()
	store := &runtimeStoreStub{target: target, attempt: persistence.InvocationRuntimeAttempt{
		IsolationDomainID: effect.IsolationDomainID,
		OperationID:       effect.OperationID,
		EffectID:          effect.EffectID,
		LeaseOwner:        claim.LeaseOwner,
		FencingToken:      claim.FencingToken,
		Status:            "reserved",
	}}
	provider := &runtimeProviderStub{}
	driver := newRuntimeDriverForTest(
		t,
		store,
		&runtimeAuthorizerStub{},
		&runtimeExecutionSourceStub{},
		provider,
		&runtimeAdapterFactoryStub{},
	)

	_, found, err := driver.ObserveClaimed(context.Background(), claim, effect)
	if found || !errors.Is(err, ErrAmbiguousEffect) ||
		!errors.Is(err, persistence.ErrInvocationRuntimeAttemptAmbiguous) {
		t.Fatalf("reserved attempt observation = (%t, %v)", found, err)
	}
	if provider.observeCalls != 0 || provider.startCalls != 0 || store.beginCalls != 0 {
		t.Fatalf("reserved attempt crossed runtime boundary: provider %#v, store %#v", provider, store)
	}
}

func TestInvocationRuntimeDriverChecksPreconditionsBeforeReservation(t *testing.T) {
	claim, effect, target := runtimeDriverFixture()
	tests := map[string]struct {
		authorize    error
		observation  execution.Observation
		buildErr     error
		outputSchema map[string]any
		want         error
	}{
		"authorization denial": {
			authorize: ErrInvocationRuntimeDenied,
			observation: execution.Observation{
				IsolationDomainID: target.IsolationDomainID,
				ExecutionID:       "exe_runtime",
				State:             "ready",
			},
			want: ErrEffectDenied,
		},
		"execution provisioning": {
			observation: execution.Observation{
				IsolationDomainID: target.IsolationDomainID,
				ExecutionID:       "exe_runtime",
				State:             "provisioning",
			},
			want: ErrInvocationRuntimeExecutionNotReady,
		},
		"invalid request mapping": {
			observation: execution.Observation{
				IsolationDomainID: target.IsolationDomainID,
				ExecutionID:       "exe_runtime",
				State:             "ready",
			},
			buildErr: errors.New("runtime profile mapping is invalid"),
			want:     ErrEffectInvalid,
		},
		"invalid output schema": {
			observation: execution.Observation{
				IsolationDomainID: target.IsolationDomainID,
				ExecutionID:       "exe_runtime",
				State:             "ready",
			},
			outputSchema: map[string]any{"type": "not-a-json-schema-type"},
			want:         ErrEffectInvalid,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store := &runtimeStoreStub{target: target}
			provider := &runtimeProviderStub{observation: test.observation}
			builder := InvocationRuntimeRequestBuilderFunc(func(
				persistence.InvocationRuntimeTarget,
			) (dgruntime.StartRequest, error) {
				return dgruntime.StartRequest{
					Prompt:       "persisted prompt",
					OutputSchema: test.outputSchema,
				}, test.buildErr
			})
			driver, err := NewInvocationRuntimeDriver(
				store,
				&runtimeAuthorizerStub{err: test.authorize},
				builder,
				&runtimeExecutionSourceStub{value: execution.Execution{
					IsolationDomainID: target.IsolationDomainID,
					ID:                "exe_runtime",
					State:             "ready",
				}},
				provider,
				&runtimeAdapterFactoryStub{},
				&runtimeArtifactFinalizerStub{},
				InvocationRuntimeDriverConfig{LeaseDuration: time.Minute, RenewInterval: time.Second},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.ApplyClaimed(context.Background(), claim, effect)
			if !errors.Is(err, test.want) {
				t.Fatalf("precondition error = %v, want %v", err, test.want)
			}
			if store.beginCalls != 0 || provider.startCalls != 0 {
				t.Fatalf("precondition crossed runtime boundary: store %#v, provider %#v", store, provider)
			}
		})
	}
}

func newRuntimeDriverForTest(
	t *testing.T,
	store InvocationRuntimeStore,
	authorizer InvocationRuntimeAuthorizer,
	executions executionByOperationSource,
	provider invocationRuntimeProvider,
	adapters InvocationRuntimeAdapterFactory,
) *InvocationRuntimeDriver {
	t.Helper()
	driver, err := NewInvocationRuntimeDriver(
		store,
		authorizer,
		InvocationRuntimeRequestBuilderFunc(func(
			target persistence.InvocationRuntimeTarget,
		) (dgruntime.StartRequest, error) {
			return dgruntime.StartRequest{
				Prompt:       "persisted prompt",
				OutputSchema: target.OutputSchema,
				ApprovalMode: dgruntime.ApprovalLocked,
				SandboxMode:  dgruntime.SandboxReadOnly,
			}, nil
		}),
		executions,
		provider,
		adapters,
		&runtimeArtifactFinalizerStub{},
		InvocationRuntimeDriverConfig{LeaseDuration: time.Minute, RenewInterval: time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	return driver
}

func runtimeDriverFixture() (
	persistence.OperationClaim,
	persistence.EffectRecord,
	persistence.InvocationRuntimeTarget,
) {
	claim := persistence.OperationClaim{
		Kind:                persistence.OperationKindInvocation,
		IsolationDomainID:   "iso_runtime",
		ID:                  "op_runtime",
		ResourceID:          "inv_runtime",
		Command:             "invoke",
		ObservedState:       "running",
		StateMachineVersion: 2,
		LeaseOwner:          "worker-runtime",
		FencingToken:        7,
		LeaseExpiresAt:      time.Now().Add(time.Minute),
		DeadlineAt:          time.Now().Add(time.Hour),
		ActorID:             "principal:runtime",
		CorrelationID:       "cor_runtime",
	}
	effect := persistence.EffectRecord{
		IsolationDomainID: claim.IsolationDomainID,
		OperationKind:     claim.Kind,
		OperationID:       claim.ID,
		EffectID: identity.Derived(
			"eff",
			claim.IsolationDomainID+":"+claim.Kind+":"+claim.ID+":run-invocation",
		),
		Phase:  "run-invocation",
		Status: "prepared",
	}
	target := persistence.InvocationRuntimeTarget{
		IsolationDomainID:   claim.IsolationDomainID,
		OperationID:         claim.ID,
		InvocationID:        claim.ResourceID,
		ServiceID:           "svc_runtime",
		RevisionID:          "rev_runtime",
		ActorID:             claim.ActorID,
		CorrelationID:       claim.CorrelationID,
		StateMachineVersion: claim.StateMachineVersion,
		Input:               map[string]any{"prompt": "persisted prompt"},
		RuntimeProfile:      "codex-pinned",
	}
	return claim, effect, target
}

type runtimeStoreStub struct {
	target        persistence.InvocationRuntimeTarget
	targetErr     error
	attempt       persistence.InvocationRuntimeAttempt
	attemptErr    error
	eventErr      error
	events        []persistence.InvocationRuntimeEvent
	beginCalls    int
	completeCalls int
	failCalls     int
	renewCalls    int
}

func (stub *runtimeStoreStub) GetClaimedInvocationRuntimeTarget(
	_ context.Context,
	_ persistence.OperationClaim,
) (persistence.InvocationRuntimeTarget, error) {
	return stub.target, stub.targetErr
}

func (stub *runtimeStoreStub) GetInvocationRuntimeAttempt(
	_ context.Context,
	_ string,
	_ string,
) (persistence.InvocationRuntimeAttempt, error) {
	return stub.attempt, stub.attemptErr
}

func (stub *runtimeStoreStub) BeginInvocationRuntimeAttempt(
	_ context.Context,
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
) (persistence.InvocationRuntimeAttempt, error) {
	stub.beginCalls++
	stub.attempt = persistence.InvocationRuntimeAttempt{
		IsolationDomainID: effect.IsolationDomainID,
		OperationID:       effect.OperationID,
		EffectID:          effect.EffectID,
		LeaseOwner:        claim.LeaseOwner,
		FencingToken:      claim.FencingToken,
		Status:            "reserved",
	}
	stub.attemptErr = nil
	return stub.attempt, nil
}

func (stub *runtimeStoreStub) CompleteInvocationRuntimeAttempt(
	_ context.Context,
	_ persistence.OperationClaim,
	_ persistence.EffectRecord,
	result map[string]any,
) (persistence.InvocationRuntimeAttempt, error) {
	stub.completeCalls++
	stub.attempt.Status = "succeeded"
	stub.attempt.Result = result
	return stub.attempt, nil
}

func (stub *runtimeStoreStub) FailInvocationRuntimeAttempt(
	_ context.Context,
	_ persistence.OperationClaim,
	_ persistence.EffectRecord,
	result map[string]any,
) (persistence.InvocationRuntimeAttempt, error) {
	stub.failCalls++
	stub.attempt.Status = "failed"
	stub.attempt.Result = result
	return stub.attempt, nil
}

func (stub *runtimeStoreStub) RecordInvocationRuntimeEvent(
	_ context.Context,
	_ persistence.OperationClaim,
	event persistence.InvocationRuntimeEvent,
) (domain.EventEnvelope, error) {
	if stub.eventErr != nil {
		return domain.EventEnvelope{}, stub.eventErr
	}
	stub.events = append(stub.events, event)
	return domain.EventEnvelope{}, nil
}

func (stub *runtimeStoreStub) RenewLease(
	_ context.Context,
	claim persistence.OperationClaim,
	duration time.Duration,
) (persistence.OperationClaim, error) {
	stub.renewCalls++
	claim.LeaseExpiresAt = time.Now().Add(duration)
	return claim, nil
}

type runtimeAuthorizerStub struct {
	calls   int
	err     error
	request dgruntime.StartRequest
}

func (stub *runtimeAuthorizerStub) AuthorizeInvocationRuntime(
	_ context.Context,
	_ persistence.InvocationRuntimeTarget,
	request dgruntime.StartRequest,
) error {
	stub.calls++
	stub.request = request
	return stub.err
}

type runtimeExecutionSourceStub struct {
	value execution.Execution
	err   error
}

func (stub *runtimeExecutionSourceStub) GetExecutionByOperation(
	_ context.Context,
	_ string,
	_ string,
) (execution.Execution, error) {
	return stub.value, stub.err
}

type runtimeProviderStub struct {
	observation   execution.Observation
	observeErr    error
	startErr      error
	observeCalls  int
	startCalls    int
	exports       []execution.ExportRequest
	exportResults []execution.ExportResult
	exportErr     error
}

func (stub *runtimeProviderStub) Observe(
	_ context.Context,
	_ execution.ExecutionRef,
) (execution.Observation, error) {
	stub.observeCalls++
	return stub.observation, stub.observeErr
}

func (stub *runtimeProviderStub) StartRuntime(
	_ context.Context,
	_ execution.ExecutionRef,
) (execution.RuntimeSession, error) {
	stub.startCalls++
	return runtimeSessionStub{}, stub.startErr
}

func (stub *runtimeProviderStub) Export(
	_ context.Context,
	request execution.ExportRequest,
) (execution.ExportResult, error) {
	stub.exports = append(stub.exports, request)
	if stub.exportErr != nil {
		return execution.ExportResult{}, stub.exportErr
	}
	if len(stub.exportResults) > 0 {
		result := stub.exportResults[0]
		stub.exportResults = stub.exportResults[1:]
		return result, nil
	}
	return execution.ExportResult{
		IsolationDomainID: request.IsolationDomainID,
		ExecutionID:       request.ExecutionID,
	}, nil
}

type runtimeArtifactFinalizerStub struct {
	values []artifact.Finalization
	err    error
}

func (stub *runtimeArtifactFinalizerStub) Finalize(
	_ context.Context,
	finalization artifact.Finalization,
) (artifact.Record, error) {
	stub.values = append(stub.values, finalization)
	if stub.err != nil {
		return artifact.Record{}, stub.err
	}
	return finalization.Binding.Record, nil
}

type runtimeAdapterFactoryStub struct {
	adapter InvocationRuntimeAdapter
	err     error
}

func (stub *runtimeAdapterFactoryStub) New(
	_ execution.RuntimeSession,
) (InvocationRuntimeAdapter, error) {
	return stub.adapter, stub.err
}

type runtimeAdapterStub struct {
	turn       dgruntime.Turn
	err        error
	request    dgruntime.StartRequest
	startCalls int
}

func (stub *runtimeAdapterStub) Start(
	_ context.Context,
	request dgruntime.StartRequest,
) (dgruntime.Turn, error) {
	stub.startCalls++
	stub.request = request
	return stub.turn, stub.err
}

func (*runtimeAdapterStub) Close() error { return nil }

type runtimeTurnStub struct {
	events     <-chan dgruntime.Event
	waitErr    error
	interrupts int
}

func (stub *runtimeTurnStub) Events() <-chan dgruntime.Event { return stub.events }
func (*runtimeTurnStub) ResolveApproval(context.Context, string, dgruntime.ApprovalDecision) error {
	return dgruntime.ErrApprovalMode
}
func (stub *runtimeTurnStub) Interrupt(context.Context) error {
	stub.interrupts++
	return nil
}
func (stub *runtimeTurnStub) Wait(context.Context) error { return stub.waitErr }
func (*runtimeTurnStub) Close() error                    { return nil }

func runtimeEvents(values ...dgruntime.Event) <-chan dgruntime.Event {
	events := make(chan dgruntime.Event, len(values))
	for _, value := range values {
		events <- value
	}
	return events
}

type runtimeSessionStub struct{}

func (runtimeSessionStub) Input() io.WriteCloser { return nil }
func (runtimeSessionStub) Output() io.ReadCloser { return nil }
func (runtimeSessionStub) Errors() io.ReadCloser { return nil }
func (runtimeSessionStub) Wait() error           { return nil }
func (runtimeSessionStub) Close() error          { return nil }

