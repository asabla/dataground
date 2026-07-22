package reconcile

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
)

func TestInvocationAdmissionDriverReauthorizesAndAdmitsDurableTarget(t *testing.T) {
	target := admissionTarget()
	targets := &admissionTargetStub{target: target}
	authorizer := &admissionAuthorizerStub{}
	admitter := &admitterStub{value: admittedExecution(target.IsolationDomainID)}
	executions := &executionByOperationStub{err: execution.ErrExecutionMissing}
	driver, err := NewInvocationAdmissionDriver(targets, authorizer, admitter, executions)
	if err != nil {
		t.Fatal(err)
	}

	effect := admissionEffect(target)
	observed, found, err := driver.Observe(context.Background(), effect)
	if err != nil || found || observed != nil {
		t.Fatalf("observe before admission = (%v, %t, %v)", observed, found, err)
	}
	result, err := driver.Apply(context.Background(), effect)
	if err != nil {
		t.Fatal(err)
	}
	if admitter.calls != 1 || authorizer.calls != 2 {
		t.Fatalf("calls = admission:%d authorization:%d", admitter.calls, authorizer.calls)
	}
	wantRequest := execution.AdmissionRequest{
		IsolationDomainID: target.IsolationDomainID,
		RevisionID:        target.RevisionID,
		OperationID:       target.OperationID,
	}
	if admitter.request != wantRequest {
		t.Fatalf("admission request = %#v, want %#v", admitter.request, wantRequest)
	}
	if result["executionId"] != admitter.value.ID || result["state"] != admitter.value.State {
		t.Fatalf("admission observation = %#v", result)
	}
	if _, exposed := result["gatewayId"]; exposed {
		t.Fatalf("admission observation exposes gateway identity: %#v", result)
	}
}

func TestInvocationAdmissionDriverObservesPersistedExecutionWithoutReadmission(t *testing.T) {
	target := admissionTarget()
	value := admittedExecution(target.IsolationDomainID)
	admitter := &admitterStub{}
	driver, err := NewInvocationAdmissionDriver(
		&admissionTargetStub{target: target},
		&admissionAuthorizerStub{},
		admitter,
		&executionByOperationStub{value: value},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, found, err := driver.Observe(context.Background(), admissionEffect(target))
	if err != nil || !found {
		t.Fatalf("observe persisted execution = (%#v, %t, %v)", result, found, err)
	}
	if admitter.calls != 0 || result["executionId"] != value.ID {
		t.Fatalf("persisted observation = %#v, admission calls = %d", result, admitter.calls)
	}
}

func TestInvocationAdmissionDriverFailsClosedBeforeProviderAdmission(t *testing.T) {
	denied := errors.New("admission denied")
	tests := map[string]struct {
		effect    persistence.EffectRecord
		target    persistence.InvocationAdmissionTarget
		targetErr error
		authorize error
		execution execution.Execution
		want      error
	}{
		"wrong phase": {
			effect: func() persistence.EffectRecord {
				value := admissionEffect(admissionTarget())
				value.Phase = "run-invocation"
				return value
			}(),
			target: admissionTarget(),
			want:   ErrInvocationAdmissionTargetMismatch,
		},
		"missing target": {
			effect:    admissionEffect(admissionTarget()),
			targetErr: errors.New("target unavailable"),
			want:      errors.New("target unavailable"),
		},
		"authorization denial": {
			effect:    admissionEffect(admissionTarget()),
			target:    admissionTarget(),
			authorize: denied,
			want:      denied,
		},
		"legacy state machine": {
			effect: admissionEffect(admissionTarget()),
			target: func() persistence.InvocationAdmissionTarget {
				value := admissionTarget()
				value.StateMachineVersion = 1
				return value
			}(),
			want: ErrInvocationAdmissionTargetMismatch,
		},
		"cross-domain result": {
			effect: admissionEffect(admissionTarget()),
			target: admissionTarget(),
			execution: execution.Execution{
				IsolationDomainID: "iso_other", ID: "exe_other", State: "provisioning",
			},
			want: ErrInvocationAdmissionTargetMismatch,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			targets := &admissionTargetStub{target: test.target, err: test.targetErr}
			authorizer := &admissionAuthorizerStub{err: test.authorize}
			admitter := &admitterStub{value: test.execution}
			driver, err := NewInvocationAdmissionDriver(
				targets,
				authorizer,
				admitter,
				&executionByOperationStub{err: execution.ErrExecutionMissing},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Apply(context.Background(), test.effect)
			if test.want == denied || errors.Is(test.want, ErrInvocationAdmissionTargetMismatch) {
				if !errors.Is(err, test.want) {
					t.Fatalf("apply error = %v, want %v", err, test.want)
				}
			} else if err == nil || err.Error() != test.want.Error() {
				t.Fatalf("apply error = %v, want %v", err, test.want)
			}
			if admitter.calls != 0 && name != "cross-domain result" {
				t.Fatalf("provider admission called %d times", admitter.calls)
			}
		})
	}
}

func TestRoutedDriverUsesOnlyExactEffectRoute(t *testing.T) {
	fallback := &recordingEffectDriver{}
	admission := &recordingEffectDriver{}
	driver, err := NewRoutedDriver(fallback, map[EffectRoute]EffectDriver{
		{OperationKind: persistence.OperationKindInvocation, Phase: "start-invocation"}: admission,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := persistence.EffectRecord{
		OperationKind: persistence.OperationKindInvocation,
		Phase:         "start-invocation",
	}
	run := start
	run.Phase = "run-invocation"
	if _, err := driver.Apply(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.Observe(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(admission.applied, []persistence.EffectRecord{start}) ||
		!reflect.DeepEqual(fallback.observed, []persistence.EffectRecord{run}) {
		t.Fatalf("routes = admission %#v, fallback %#v", admission.applied, fallback.observed)
	}
}

func TestReconcilerRoutesAdmissionAndRuntimeAsSeparateDurableEffects(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "inv_test", Command: "invoke", ObservedState: "queued",
		StateMachineVersion: 2,
		DeadlineAt:          time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
	})
	runtimeDriver := &recordingEffectDriver{}
	admissionDriver := &recordingEffectDriver{}
	driver, err := NewRoutedDriver(runtimeDriver, map[EffectRoute]EffectDriver{
		{OperationKind: persistence.OperationKindInvocation, Phase: "start-invocation"}: admissionDriver,
	})
	if err != nil {
		t.Fatal(err)
	}

	runUntilState(t, New(store, driver, "worker-a"), store, "succeeded")
	if len(admissionDriver.applied) != 1 ||
		admissionDriver.applied[0].Phase != "start-invocation" ||
		len(runtimeDriver.applied) != 1 ||
		runtimeDriver.applied[0].Phase != "run-invocation" {
		t.Fatalf(
			"effect routing = admission %#v, runtime %#v",
			admissionDriver.applied,
			runtimeDriver.applied,
		)
	}
}

type admissionTargetStub struct {
	target persistence.InvocationAdmissionTarget
	err    error
}

func (stub *admissionTargetStub) GetInvocationAdmissionTarget(
	_ context.Context,
	_ string,
	_ string,
) (persistence.InvocationAdmissionTarget, error) {
	return stub.target, stub.err
}

type admissionAuthorizerStub struct {
	calls int
	err   error
}

func (stub *admissionAuthorizerStub) AuthorizeInvocationAdmission(
	_ context.Context,
	_ persistence.InvocationAdmissionTarget,
) error {
	stub.calls++
	return stub.err
}

type admitterStub struct {
	request execution.AdmissionRequest
	value   execution.Execution
	err     error
	calls   int
}

func (stub *admitterStub) Admit(
	_ context.Context,
	request execution.AdmissionRequest,
) (execution.Execution, error) {
	stub.calls++
	stub.request = request
	return stub.value, stub.err
}

type executionByOperationStub struct {
	value execution.Execution
	err   error
}

func (stub *executionByOperationStub) GetExecutionByOperation(
	_ context.Context,
	_ string,
	_ string,
) (execution.Execution, error) {
	return stub.value, stub.err
}

type recordingEffectDriver struct {
	applied  []persistence.EffectRecord
	observed []persistence.EffectRecord
}

func (driver *recordingEffectDriver) Apply(
	_ context.Context,
	effect persistence.EffectRecord,
) (map[string]any, error) {
	driver.applied = append(driver.applied, effect)
	return map[string]any{"status": "applied"}, nil
}

func (driver *recordingEffectDriver) Observe(
	_ context.Context,
	effect persistence.EffectRecord,
) (map[string]any, bool, error) {
	driver.observed = append(driver.observed, effect)
	return nil, false, nil
}

func admissionTarget() persistence.InvocationAdmissionTarget {
	return persistence.InvocationAdmissionTarget{
		IsolationDomainID:   "iso_aaaaaaaaaaaaaaaaaaaa",
		OperationID:         "op_bbbbbbbbbbbbbbbbbbbb",
		InvocationID:        "inv_cccccccccccccccccccc",
		ServiceID:           "svc_gggggggggggggggggggg",
		RevisionID:          "rev_dddddddddddddddddddd",
		ActorID:             "principal:test",
		CorrelationID:       "cor_hhhhhhhhhhhhhhhhhhhh",
		StateMachineVersion: 2,
	}
}

func admissionEffect(target persistence.InvocationAdmissionTarget) persistence.EffectRecord {
	return persistence.EffectRecord{
		IsolationDomainID: target.IsolationDomainID,
		OperationKind:     persistence.OperationKindInvocation,
		OperationID:       target.OperationID,
		Phase:             "start-invocation",
	}
}

func admittedExecution(isolationDomainID string) execution.Execution {
	return execution.Execution{
		IsolationDomainID: isolationDomainID,
		ID:                "exe_eeeeeeeeeeeeeeeeeeee",
		GatewayID:         "gtw_ffffffffffffffffffff",
		State:             "provisioning",
	}
}
