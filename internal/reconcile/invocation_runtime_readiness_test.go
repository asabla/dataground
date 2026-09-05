package reconcile

import (
	"context"
	"errors"
	"testing"
)

func TestInvocationRuntimeReadinessIsRechecked(t *testing.T) {
	t.Parallel()
	expected := errors.New("certification withdrawn")
	calls := 0
	driver := &InvocationRuntimeDriver{
		readiness: func(context.Context) error {
			calls++
			if calls == 2 {
				return expected
			}
			return nil
		},
	}
	if err := driver.ready(context.Background()); err != nil {
		t.Fatalf("initial readiness: %v", err)
	}
	if err := driver.ready(context.Background()); !errors.Is(err, expected) {
		t.Fatalf("changed readiness = %v", err)
	}
	if calls != 2 {
		t.Fatalf("readiness calls = %d", calls)
	}
}

func TestInvocationRuntimeReadinessRemainsOptionalOutsideGovernedComposition(t *testing.T) {
	t.Parallel()
	driver := &InvocationRuntimeDriver{}
	if err := driver.ready(context.Background()); err != nil {
		t.Fatalf("optional readiness: %v", err)
	}
}

func TestInvocationRuntimeReadinessHonorsCancellationBeforeAndDuringChecks(t *testing.T) {
	t.Parallel()
	for _, during := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		driver := &InvocationRuntimeDriver{readiness: func(context.Context) error { calls++; cancel(); return nil }}
		if !during {
			cancel()
		}
		if err := driver.ready(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled readiness passed: %v", err)
		}
		if (calls == 1) != during {
			t.Fatal("cancelled readiness invoked the dependency")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&InvocationRuntimeDriver{}).ready(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("optional readiness ignored cancellation: %v", err)
	}
}
