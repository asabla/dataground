package reconcile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestRuntimeCommandArtifactUsesCurrentAuthorityAndBoundedJournal(t *testing.T) {
	for _, boundary := range []string{"success", "authorization", "storage", "journal", "native-reference", "native-sequence", "escaped-inline", "empty", "lease", "readiness", "post-storage-readiness"} {
		t.Run(boundary, func(t *testing.T) {
			claim, effect, target := runtimeDriverFixture()
			store := &runtimeStoreStub{target: target}
			authorizer := &runtimeAuthorizerStub{}
			driver := newRuntimeDriverForTest(t, store, authorizer, &runtimeExecutionSourceStub{}, &runtimeProviderStub{}, &runtimeAdapterFactoryStub{})
			finalizer := driver.artifacts.(*runtimeArtifactFinalizerStub)
			text := strings.Repeat("界", 30000)
			if boundary == "empty" {
				text = ""
			}
			if boundary == "escaped-inline" {
				text = strings.Repeat("\x00", 30000)
			}
			event := dgruntime.Event{Sequence: 2, Type: dgruntime.CommandCompletedEvent, Payload: dgruntime.CompletedCommand{Text: text, Status: "failed"}.Payload()}
			switch boundary {
			case "lease":
				driver.store = &commandLeaseDeniedStore{runtimeStoreStub: store}
			case "readiness":
				driver.readiness = func(context.Context) error { return errors.New("not ready") }
			case "post-storage-readiness":
				calls := 0
				driver.readiness = func(context.Context) error {
					calls++
					if calls == 3 {
						return errors.New("no longer ready")
					}
					return nil
				}
			case "authorization":
				authorizer.err = ErrInvocationRuntimeDenied
			case "storage":
				finalizer.err = artifact.ErrInvocationArtifactUnavailable
			case "journal":
				store.eventErr = errors.New("journal unavailable")
			case "native-sequence":
				event.Sequence = ^uint64(0)
			case "native-reference":
				event.Type = dgruntime.CommandArtifactEvent
			}
			reference, err := driver.recordRuntimeOutput(context.Background(), claim, effect, target, event)
			if boundary != "success" && boundary != "escaped-inline" && boundary != "empty" {
				if err == nil || reference != nil || len(store.events) != 0 {
					t.Fatal("failed publication escaped into journal", err)
				}
				if (boundary == "authorization" || boundary == "native-reference" || boundary == "native-sequence" || boundary == "lease" || boundary == "readiness") && len(finalizer.values) != 0 {
					t.Fatal("artifact effect ran without authority")
				}
				return
			}
			if err != nil || reference != nil || len(store.events) != 1 || store.events[0].Type != dgruntime.CommandArtifactEvent {
				t.Fatal("snapshot was not referenced", err)
			}
			if len(store.events[0].Payload["preview"].(string)) > dgruntime.MaximumMessagePreviewBytes || store.events[0].Payload["sizeBytes"] != int64(len(text)) || len(finalizer.values) != 1 || string(finalizer.values[0].Content) != text || !finalizer.values[0].Binding.Record.Sensitive {
				t.Fatal("snapshot bytes or sensitivity changed")
			}
			if _, err := dgruntime.ParseCommandArtifact(store.events[0].Payload); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type commandLeaseDeniedStore struct{ *runtimeStoreStub }

func (store *commandLeaseDeniedStore) RenewLease(context.Context, persistence.OperationClaim, time.Duration) (persistence.OperationClaim, error) {
	return persistence.OperationClaim{}, persistence.ErrLeaseLost
}
