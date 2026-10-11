package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/artifact"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestRuntimeToolArtifactUsesCurrentAuthorityAndBoundedJournal(t *testing.T) {
	for _, boundary := range []string{"success", "authorization", "storage", "journal", "native-reference", "native-sequence", "escaped-inline", "lease", "readiness", "post-storage-readiness"} {
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
			event := dgruntime.Event{Sequence: 2, Type: dgruntime.ToolCompletedEvent, Payload: dgruntime.CompletedToolOutput{SchemaVersion: dgruntime.ToolOutputSchemaV1, Status: "failed", Content: []any{map[string]any{"type": "text", "text": text}}}.Payload()}
			parsed, _ := dgruntime.ParseCompletedToolOutput(event.Payload)
			content, _ := json.Marshal(parsed)
			switch boundary {
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
				event.Type = dgruntime.ToolArtifactEvent
			}
			reference, err := driver.recordRuntimeOutput(context.Background(), claim, effect, target, event)
			if boundary != "success" && boundary != "escaped-inline" {
				if err == nil || reference != nil || len(store.events) != 0 {
					t.Fatal("failed publication escaped into journal", err)
				}
				if (boundary == "authorization" || boundary == "native-reference" || boundary == "native-sequence" || boundary == "lease" || boundary == "readiness") && len(finalizer.values) != 0 {
					t.Fatal("artifact effect ran without authority")
				}
				return
			}
			if err != nil || reference != nil || len(store.events) != 1 || store.events[0].Type != dgruntime.ToolArtifactEvent {
				t.Fatal("snapshot was not referenced", err)
			}
			if len(store.events[0].Payload["preview"].(string)) > dgruntime.MaximumMessagePreviewBytes || store.events[0].Payload["sizeBytes"] != int64(len(content)) || len(finalizer.values) != 1 || string(finalizer.values[0].Content) != string(content) || !finalizer.values[0].Binding.Record.Sensitive {
				t.Fatal("snapshot bytes or sensitivity changed")
			}
			if _, err := dgruntime.ParseToolArtifact(store.events[0].Payload); err != nil {
				t.Fatal(err)
			}
		})
	}
}
