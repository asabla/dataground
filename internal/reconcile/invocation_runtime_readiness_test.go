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
