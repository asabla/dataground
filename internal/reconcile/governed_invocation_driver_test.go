package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/persistence"
)

func TestGovernedInvocationDriverOwnsCompleteVersionTwoLifecycle(t *testing.T) {
	fallback := &compositionFallbackDriver{}
	admission := &InvocationAdmissionDriver{}
	runtime := &InvocationRuntimeDriver{}
	cancellation := &InvocationCancellationDriver{}

	driver, err := NewGovernedInvocationDriver(
		fallback,
		admission,
		runtime,
		cancellation,
	)
	if err != nil {
		t.Fatal(err)
	}
	if driver.fallback != fallback {
		t.Fatal("governed invocation driver replaced the fallback")
	}
	if len(driver.routes) != 2 || len(driver.claimedRoutes) != 1 {
		t.Fatalf(
			"governed route counts = ordinary %d, claimed %d",
			len(driver.routes),
			len(driver.claimedRoutes),
		)
	}
	admissionRoute := EffectRoute{
		OperationKind: persistence.OperationKindInvocation,
		Phase:         "start-invocation",
	}
	cancellationRoute := EffectRoute{
		OperationKind: persistence.OperationKindInvocation,
		Phase:         "cancel-invocation",
	}
	runtimeRoute := EffectRoute{
		OperationKind: persistence.OperationKindInvocation,
		Phase:         "run-invocation",
	}
	if driver.routes[admissionRoute] != admission {
		t.Fatal("admission route is not owned by the governed admission driver")
	}
	if driver.routes[cancellationRoute] != cancellation {
		t.Fatal("cancellation route is not owned by the governed cancellation driver")
	}
	if driver.claimedRoutes[runtimeRoute] != runtime {
		t.Fatal("runtime route is not owned by the claim-bound runtime driver")
	}

	effect := persistence.EffectRecord{
		IsolationDomainID: "iso_test",
		OperationKind:     persistence.OperationKindInvocation,
		OperationID:       "op_test",
		Phase:             "run-invocation",
	}
	if _, err := driver.Apply(context.Background(), effect); !errors.Is(err, ErrEffectClaimRequired) {
		t.Fatalf("claimless governed runtime error = %v, want claim required", err)
	}
	if fallback.applyCount != 0 {
		t.Fatal("claimless governed runtime reached the fallback")
	}

	effect.OperationKind = persistence.OperationKindPublication
	effect.Phase = "publish-service"
	if _, err := driver.Apply(context.Background(), effect); err != nil {
		t.Fatalf("unowned effect fallback: %v", err)
	}
	if fallback.applyCount != 1 {
		t.Fatalf("fallback applications = %d, want one", fallback.applyCount)
	}
}

func TestGovernedInvocationDriverRejectsIncompleteComposition(t *testing.T) {
	fallback := &compositionFallbackDriver{}
	admission := &InvocationAdmissionDriver{}
	runtime := &InvocationRuntimeDriver{}
	cancellation := &InvocationCancellationDriver{}
	tests := []struct {
		name         string
		fallback     EffectDriver
		admission    *InvocationAdmissionDriver
		runtime      *InvocationRuntimeDriver
		cancellation *InvocationCancellationDriver
	}{
		{
			name:         "fallback",
			admission:    admission,
			runtime:      runtime,
			cancellation: cancellation,
		},
		{
			name:         "admission",
			fallback:     fallback,
			runtime:      runtime,
			cancellation: cancellation,
		},
		{
			name:         "runtime",
			fallback:     fallback,
			admission:    admission,
			cancellation: cancellation,
		},
		{
			name:      "cancellation",
			fallback:  fallback,
			admission: admission,
			runtime:   runtime,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			driver, err := NewGovernedInvocationDriver(
				test.fallback,
				test.admission,
				test.runtime,
				test.cancellation,
			)
			if driver != nil || !errors.Is(err, ErrGovernedInvocationDriversIncomplete) {
				t.Fatalf("incomplete composition = (%#v, %v)", driver, err)
			}
		})
	}
}

type compositionFallbackDriver struct {
	applyCount int
}

func (*compositionFallbackDriver) Observe(
	context.Context,
	persistence.EffectRecord,
) (map[string]any, bool, error) {
	return nil, false, nil
}

func (driver *compositionFallbackDriver) Apply(
	context.Context,
	persistence.EffectRecord,
) (map[string]any, error) {
	driver.applyCount++
	return map[string]any{"status": "succeeded"}, nil
}
