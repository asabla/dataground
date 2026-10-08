package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestInvalidRuntimeOutputRetentionRequiresCurrentAuthority(t *testing.T) {
	for _, boundary := range []string{"authorization", "readiness", "readiness-before-artifact", "readiness-after-artifact", "artifact", "receipt"} {
		t.Run(boundary, func(t *testing.T) {
			claim, effect, target := runtimeDriverFixture()
			store := &runtimeStoreStub{target: target, attempt: persistence.InvocationRuntimeAttempt{
				EffectID: effect.EffectID, Status: "output_invalid", Result: map[string]any{"sourceSequence": "2"},
			}, events: []persistence.InvocationRuntimeEvent{{SourceSequence: 2, Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": "invalid", "phase": "final"}}}}
			authorizer := &runtimeAuthorizerStub{}
			provider := &runtimeProviderStub{}
			driver := newRuntimeDriverForTest(t, store, authorizer, &runtimeExecutionSourceStub{}, provider, &runtimeAdapterFactoryStub{})
			finalizer := driver.artifacts.(*runtimeArtifactFinalizerStub)
			unavailable := errors.New("unavailable")
			switch boundary {
			case "authorization":
				authorizer.err = ErrInvocationRuntimeDenied
			case "readiness", "readiness-before-artifact", "readiness-after-artifact":
				reject := 1
				if boundary == "readiness-before-artifact" {
					reject = 3
				}
				if boundary == "readiness-after-artifact" {
					reject = 4
				}
				calls := 0
				driver.readiness = func(context.Context) error {
					calls++
					if calls == reject {
						return unavailable
					}
					return nil
				}
			case "artifact":
				finalizer.err = unavailable
			case "receipt":
				store.failErr = unavailable
			}
			_, found, err := driver.ObserveClaimed(context.Background(), claim, effect)
			if found || err == nil || errors.Is(err, ErrEffectTerminal) || store.attempt.Status != "output_invalid" {
				t.Fatal("unavailable retention became terminal", store.attempt, err)
			}
			if (boundary == "authorization" || boundary == "readiness" || boundary == "readiness-before-artifact") && len(finalizer.values) != 0 {
				t.Fatal("retention wrote without current authority")
			}
			authorizer.err, finalizer.err, store.failErr = nil, nil, nil
			driver.readiness = nil
			_, found, err = driver.ObserveClaimed(context.Background(), claim, effect)
			if found || !errors.Is(err, ErrEffectTerminal) || !errors.Is(err, ErrInvocationRuntimeOutputInvalid) || store.attempt.Status != "failed" {
				t.Fatal("retention did not recover", store.attempt, err)
			}
			if provider.startCalls != 0 || provider.observeCalls != 0 || len(provider.exports) != 0 || store.beginCalls != 0 {
				t.Fatal("retention accessed native execution")
			}
			if got := finalizer.values[len(finalizer.values)-1].Content; string(got) != "invalid" {
				t.Fatal("recovered artifact changed content")
			}
		})
	}
}
