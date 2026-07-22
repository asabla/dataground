package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
)

func TestInvocationCancellationDriverAuthorizesAndTerminatesDurableExecution(t *testing.T) {
	target := cancellationTarget()
	value := cancellationExecution(target.IsolationDomainID, "running")
	authorizer := &cancellationAuthorizerStub{}
	provider := &cancellationProviderStub{
		observation: execution.Observation{
			IsolationDomainID: target.IsolationDomainID,
			ExecutionID:       value.ID,
			State:             "running",
		},
	}
	driver, err := NewInvocationCancellationDriver(
		&cancellationTargetStub{target: target},
		authorizer,
		&cancellationExecutionSourceStub{value: value},
		provider,
	)
	if err != nil {
		t.Fatal(err)
	}

	result, ready, err := driver.Observe(context.Background(), cancellationEffect(target))
	if err != nil || ready || result["state"] != "running" {
		t.Fatalf("observe active execution = (%#v, %t, %v)", result, ready, err)
	}
	result, err = driver.Apply(context.Background(), cancellationEffect(target))
	if err != nil {
		t.Fatal(err)
	}
	if authorizer.calls != 1 || provider.terminateCalls != 1 {
		t.Fatalf("calls = authorization:%d termination:%d", authorizer.calls, provider.terminateCalls)
	}
	wantRef := execution.ExecutionRef{IsolationDomainID: target.IsolationDomainID, ID: value.ID}
	if provider.terminated != wantRef {
		t.Fatalf("termination ref = %#v, want %#v", provider.terminated, wantRef)
	}
	if result["executionId"] != value.ID || result["state"] != "terminated" {
		t.Fatalf("cancellation observation = %#v", result)
	}
	if _, exposed := result["gatewayId"]; exposed {
		t.Fatalf("cancellation observation exposes gateway identity: %#v", result)
	}
}

func TestInvocationCancellationDriverObservesSafeTerminalOutcomesWithoutReauthorization(t *testing.T) {
	tests := map[string]struct {
		target    persistence.InvocationCancellationTarget
		execution execution.Execution
		sourceErr error
		wantState string
	}{
		"terminated execution": {
			target:    cancellationTarget(),
			execution: cancellationExecution(cancellationTarget().IsolationDomainID, "terminated"),
			wantState: "terminated",
		},
		"admission never prepared": {
			target: func() persistence.InvocationCancellationTarget {
				value := cancellationTarget()
				value.AdmissionPrepared = false
				return value
			}(),
			sourceErr: execution.ErrExecutionMissing,
			wantState: "absent",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			authorizer := &cancellationAuthorizerStub{err: ErrInvocationCancellationDenied}
			provider := &cancellationProviderStub{observeErr: errors.New("provider must not be called")}
			driver, err := NewInvocationCancellationDriver(
				&cancellationTargetStub{target: test.target},
				authorizer,
				&cancellationExecutionSourceStub{value: test.execution, err: test.sourceErr},
				provider,
			)
			if err != nil {
				t.Fatal(err)
			}
			result, ready, err := driver.Observe(context.Background(), cancellationEffect(test.target))
			if err != nil || !ready || result["state"] != test.wantState {
				t.Fatalf("observe terminal outcome = (%#v, %t, %v)", result, ready, err)
			}
			if authorizer.calls != 0 || provider.observeCalls != 0 || provider.terminateCalls != 0 {
				t.Fatalf(
					"terminal calls = authorization:%d observation:%d termination:%d",
					authorizer.calls,
					provider.observeCalls,
					provider.terminateCalls,
				)
			}
		})
	}
}

func TestInvocationCancellationDriverFailsClosedBeforeTermination(t *testing.T) {
	target := cancellationTarget()
	tests := map[string]struct {
		effect       persistence.EffectRecord
		target       persistence.InvocationCancellationTarget
		targetErr    error
		execution    execution.Execution
		executionErr error
		authorize    error
		terminate    error
		want         error
	}{
		"wrong phase": {
			effect: func() persistence.EffectRecord {
				value := cancellationEffect(target)
				value.Phase = "run-invocation"
				return value
			}(),
			target: target,
			want:   ErrInvocationCancellationTargetMismatch,
		},
		"durably missing target": {
			effect:    cancellationEffect(target),
			targetErr: persistence.ErrInvocationCancellationTargetMissing,
			want:      persistence.ErrInvocationCancellationTargetMissing,
		},
		"legacy state machine": {
			effect: cancellationEffect(target),
			target: func() persistence.InvocationCancellationTarget {
				value := target
				value.StateMachineVersion = 1
				return value
			}(),
			want: ErrInvocationCancellationTargetMismatch,
		},
		"admission outcome unresolved": {
			effect:       cancellationEffect(target),
			target:       target,
			executionErr: execution.ErrExecutionMissing,
			want:         ErrInvocationCancellationExecutionUnknown,
		},
		"cross-domain execution": {
			effect: cancellationEffect(target),
			target: target,
			execution: execution.Execution{
				IsolationDomainID: "iso_other",
				ID:                "exe_other",
				State:             "running",
			},
			want: ErrInvocationCancellationTargetMismatch,
		},
		"authorization denial": {
			effect:    cancellationEffect(target),
			target:    target,
			execution: cancellationExecution(target.IsolationDomainID, "running"),
			authorize: ErrInvocationCancellationDenied,
			want:      ErrInvocationCancellationDenied,
		},
		"ambiguous termination": {
			effect:    cancellationEffect(target),
			target:    target,
			execution: cancellationExecution(target.IsolationDomainID, "running"),
			terminate: errors.New("provider result unavailable"),
			want:      ErrAmbiguousEffect,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			authorizer := &cancellationAuthorizerStub{err: test.authorize}
			provider := &cancellationProviderStub{terminateErr: test.terminate}
			driver, err := NewInvocationCancellationDriver(
				&cancellationTargetStub{target: test.target, err: test.targetErr},
				authorizer,
				&cancellationExecutionSourceStub{value: test.execution, err: test.executionErr},
				provider,
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Apply(context.Background(), test.effect)
			if !errors.Is(err, test.want) {
				t.Fatalf("apply error = %v, want %v", err, test.want)
			}
			if name == "wrong phase" || name == "durably missing target" || name == "legacy state machine" {
				if !errors.Is(err, ErrEffectInvalid) {
					t.Fatalf("invalid target was not classified as terminal: %v", err)
				}
			}
			if name == "admission outcome unresolved" || name == "cross-domain execution" ||
				name == "ambiguous termination" {
				if !errors.Is(err, ErrAmbiguousEffect) {
					t.Fatalf("uncertain cancellation was not classified as ambiguous: %v", err)
				}
			}
			if name == "authorization denial" && !errors.Is(err, ErrEffectDenied) {
				t.Fatalf("authorization denial was not classified as terminal: %v", err)
			}
			wantTerminationCalls := 0
			if name == "ambiguous termination" {
				wantTerminationCalls = 1
			}
			if provider.terminateCalls != wantTerminationCalls {
				t.Fatalf("provider termination calls = %d, want %d", provider.terminateCalls, wantTerminationCalls)
			}
		})
	}
}

func TestInvocationCancellationDriverRequiresCompleteDependencies(t *testing.T) {
	targets := &cancellationTargetStub{}
	authorizer := &cancellationAuthorizerStub{}
	executions := &cancellationExecutionSourceStub{}
	provider := &cancellationProviderStub{}
	tests := []struct {
		targets    InvocationCancellationTargetStore
		authorizer InvocationCancellationAuthorizer
		executions executionByOperationSource
		provider   invocationCancellationProvider
	}{
		{nil, authorizer, executions, provider},
		{targets, nil, executions, provider},
		{targets, authorizer, nil, provider},
		{targets, authorizer, executions, nil},
	}
	for _, test := range tests {
		if _, err := NewInvocationCancellationDriver(
			test.targets,
			test.authorizer,
			test.executions,
			test.provider,
		); err == nil {
			t.Fatal("incomplete cancellation driver dependencies were accepted")
		}
	}
}

type cancellationTargetStub struct {
	target persistence.InvocationCancellationTarget
	err    error
}

func (stub *cancellationTargetStub) GetInvocationCancellationTarget(
	_ context.Context,
	_ string,
	_ string,
) (persistence.InvocationCancellationTarget, error) {
	return stub.target, stub.err
}

type cancellationAuthorizerStub struct {
	calls int
	err   error
}

func (stub *cancellationAuthorizerStub) AuthorizeInvocationCancellation(
	_ context.Context,
	_ persistence.InvocationCancellationTarget,
) error {
	stub.calls++
	return stub.err
}

type cancellationExecutionSourceStub struct {
	value execution.Execution
	err   error
}

func (stub *cancellationExecutionSourceStub) GetExecutionByOperation(
	_ context.Context,
	_ string,
	_ string,
) (execution.Execution, error) {
	return stub.value, stub.err
}

type cancellationProviderStub struct {
	observation    execution.Observation
	observeErr     error
	terminateErr   error
	observed       execution.ExecutionRef
	terminated     execution.ExecutionRef
	observeCalls   int
	terminateCalls int
}

func (stub *cancellationProviderStub) Observe(
	_ context.Context,
	ref execution.ExecutionRef,
) (execution.Observation, error) {
	stub.observeCalls++
	stub.observed = ref
	return stub.observation, stub.observeErr
}

func (stub *cancellationProviderStub) Terminate(
	_ context.Context,
	ref execution.ExecutionRef,
) error {
	stub.terminateCalls++
	stub.terminated = ref
	return stub.terminateErr
}

func cancellationTarget() persistence.InvocationCancellationTarget {
	return persistence.InvocationCancellationTarget{
		IsolationDomainID:   "iso_aaaaaaaaaaaaaaaaaaaa",
		OperationID:         "op_bbbbbbbbbbbbbbbbbbbb",
		InvocationID:        "inv_cccccccccccccccccccc",
		ServiceID:           "svc_gggggggggggggggggggg",
		RevisionID:          "rev_dddddddddddddddddddd",
		ActorID:             "principal:cancellation",
		CorrelationID:       "cor_hhhhhhhhhhhhhhhhhhhh",
		StateMachineVersion: 2,
		AdmissionPrepared:   true,
	}
}

func cancellationEffect(target persistence.InvocationCancellationTarget) persistence.EffectRecord {
	return persistence.EffectRecord{
		IsolationDomainID: target.IsolationDomainID,
		OperationKind:     persistence.OperationKindInvocation,
		OperationID:       target.OperationID,
		Phase:             "cancel-invocation",
	}
}

func cancellationExecution(isolationDomainID string, state string) execution.Execution {
	return execution.Execution{
		IsolationDomainID: isolationDomainID,
		ID:                "exe_eeeeeeeeeeeeeeeeeeee",
		GatewayID:         "gtw_ffffffffffffffffffff",
		State:             state,
	}
}
