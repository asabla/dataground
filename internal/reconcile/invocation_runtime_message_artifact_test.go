package reconcile

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/artifact"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestRuntimeMessageArtifactUsesCurrentAuthorityAndBoundedJournal(t *testing.T) {
	for _, boundary := range []string{"success", "authorization", "storage", "journal", "native-reference", "native-sequence", "escaped-inline"} {
		t.Run(boundary, func(t *testing.T) {
			claim, effect, target := runtimeDriverFixture()
			store := &runtimeStoreStub{target: target}
			authorizer := &runtimeAuthorizerStub{}
			driver := newRuntimeDriverForTest(t, store, authorizer, &runtimeExecutionSourceStub{}, &runtimeProviderStub{}, &runtimeAdapterFactoryStub{})
			finalizer := driver.artifacts.(*runtimeArtifactFinalizerStub)
			text := strings.Repeat("界", 30000)
			if boundary == "escaped-inline" {
				text = strings.Repeat("\x00", 30000)
			}
			event := dgruntime.Event{Sequence: 2, Type: dgruntime.MessageCompletedEvent, Payload: dgruntime.CompletedMessage{Text: text, Phase: "final"}.Payload()}
			switch boundary {
			case "authorization":
				authorizer.err = ErrInvocationRuntimeDenied
			case "storage":
				finalizer.err = artifact.ErrInvocationArtifactUnavailable
			case "journal":
				store.eventErr = errors.New("journal unavailable")
			case "native-sequence":
				event.Sequence = ^uint64(0)
			case "native-reference":
				event.Type = dgruntime.MessageArtifactEvent
			}
			reference, err := driver.recordRuntimeOutput(context.Background(), claim, effect, target, event)
			if boundary != "success" && boundary != "escaped-inline" {
				if err == nil || reference != nil || len(store.events) != 0 {
					t.Fatal("failed publication escaped into journal", err)
				}
				if (boundary == "authorization" || boundary == "native-reference" || boundary == "native-sequence") && len(finalizer.values) != 0 {
					t.Fatal("artifact effect ran without authority")
				}
				return
			}
			if err != nil || reference == nil || len(store.events) != 1 || store.events[0].Type != dgruntime.MessageArtifactEvent {
				t.Fatal("snapshot was not referenced", err)
			}
			if len(reference.Preview) > dgruntime.MaximumMessagePreviewBytes || reference.SizeBytes != int64(len(text)) || len(finalizer.values) != 1 || string(finalizer.values[0].Content) != text || !finalizer.values[0].Binding.Record.Sensitive {
				t.Fatal("snapshot bytes or sensitivity changed")
			}
			if _, err := dgruntime.ParseMessageArtifact(store.events[0].Payload); err != nil {
				t.Fatal(err)
			}
		})
	}
}
