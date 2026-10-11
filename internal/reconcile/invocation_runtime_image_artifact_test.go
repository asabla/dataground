package reconcile

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/artifact"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/runtimetest"
)

func TestRuntimeImageArtifactUsesCurrentAuthorityAndBoundedJournal(t *testing.T) {
	for _, boundary := range []string{"success", "authorization", "storage", "journal", "native-reference", "native-sequence", "malformed", "lease", "readiness", "post-storage-readiness"} {
		t.Run(boundary, func(t *testing.T) {
			claim, effect, target := runtimeDriverFixture()
			store := &runtimeStoreStub{target: target}
			authorizer := &runtimeAuthorizerStub{}
			driver := newRuntimeDriverForTest(t, store, authorizer, &runtimeExecutionSourceStub{}, &runtimeProviderStub{}, &runtimeAdapterFactoryStub{})
			finalizer := driver.artifacts.(*runtimeArtifactFinalizerStub)
			text := runtimetest.ImageData
			event := dgruntime.Event{Sequence: 2, Type: dgruntime.ImageCompletedEvent, Payload: dgruntime.CompletedImage{Data: text}.Payload()}
			content, _ := base64.StdEncoding.DecodeString(text)
			switch boundary {
			case "malformed":
				event.Payload = dgruntime.CompletedImage{Data: "private non-image"}.Payload()
			case "lease":
				driver.store = &patchLeaseDeniedStore{runtimeStoreStub: store}
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
				event.Type = dgruntime.ImageArtifactEvent
			}
			reference, err := driver.recordRuntimeOutput(context.Background(), claim, effect, target, event)
			if boundary != "success" {
				if err == nil || reference != nil || len(store.events) != 0 {
					t.Fatal("failed publication escaped into journal", err)
				}
				if (boundary == "authorization" || boundary == "native-reference" || boundary == "native-sequence" || boundary == "lease" || boundary == "readiness" || boundary == "malformed") && len(finalizer.values) != 0 {
					t.Fatal("artifact effect ran without authority")
				}
				return
			}
			if err != nil || reference != nil || len(store.events) != 1 || store.events[0].Type != dgruntime.ImageArtifactEvent {
				t.Fatal("snapshot was not referenced", err)
			}
			if len(store.events[0].Payload["preview"].(string)) > dgruntime.MaximumMessagePreviewBytes || store.events[0].Payload["sizeBytes"] != int64(len(content)) || len(finalizer.values) != 1 || string(finalizer.values[0].Content) != string(content) || !finalizer.values[0].Binding.Record.Sensitive {
				t.Fatal("snapshot bytes or sensitivity changed")
			}
			if _, err := dgruntime.ParseImageArtifact(store.events[0].Payload); err != nil {
				t.Fatal(err)
			}
		})
	}
}
