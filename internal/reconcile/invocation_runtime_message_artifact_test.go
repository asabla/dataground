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
	for _, boundary := range []string{"success", "authorization", "storage", "journal", "native-reference", "native-sequence", "escaped-inline", "small-nul", "structured-nul"} {
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
			if boundary == "small-nul" {
				text = "before\x00after"
			}
			if boundary == "structured-nul" {
				text = `{"answer":"before\u0000after"}`
				target.OutputSchema = map[string]any{"type": "object"}
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
			if boundary != "success" && boundary != "escaped-inline" && boundary != "small-nul" && boundary != "structured-nul" {
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

func TestMessageContainsNULPreservesLiteralEscapes(t *testing.T) {
	for _, tc := range []struct {
		text             string
		structured, want bool
	}{
		{text: "plain\x00text", want: true},
		{text: `plain\u0000text`},
		{text: `{"answer":"literal\\u0000"}`, structured: true},
		{text: `{"answer":[{"nested":"\u0000"}]}`, structured: true, want: true},
		{text: `{"\u0000":"key"}`, structured: true, want: true},
		{text: `{"number":1e10000,"answer":"\u0000"}`, structured: true, want: true},
		{text: `{"answer":"invalid`, structured: true},
	} {
		if got := messageContainsNUL(tc.text, tc.structured); got != tc.want {
			t.Errorf("NUL detection = %v, want %v", got, tc.want)
		}
	}
}

func TestRuntimeMessageDeltaIsBoundedDisplayText(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload map[string]any
		valid   bool
	}{
		{name: "nul", payload: map[string]any{"text": "before\x00after"}, valid: true},
		{name: "maximum-nul", payload: map[string]any{"text": strings.Repeat("\x00", dgruntime.MaximumInlineMessageTextBytes)}, valid: true},
		{name: "literal", payload: map[string]any{"text": `literal\u0000`}, valid: true},
		{name: "oversize", payload: map[string]any{"text": strings.Repeat("x", dgruntime.MaximumInlineMessageTextBytes+1)}},
		{name: "invalid-utf8", payload: map[string]any{"text": string([]byte{0xff})}},
		{name: "native-route", payload: map[string]any{"text": "preview", "threadId": "native"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claim, effect, target := runtimeDriverFixture()
			store := &runtimeStoreStub{target: target}
			driver := newRuntimeDriverForTest(t, store, &runtimeAuthorizerStub{}, &runtimeExecutionSourceStub{}, &runtimeProviderStub{}, &runtimeAdapterFactoryStub{})
			ref, err := driver.recordRuntimeOutput(context.Background(), claim, effect, target, dgruntime.Event{Sequence: 1, Type: "output.text.delta", Payload: tc.payload})
			if !tc.valid {
				if err == nil || len(store.events) != 0 {
					t.Fatal("invalid delta entered journal")
				}
				return
			}
			if err != nil || ref != nil || len(store.events) != 1 {
				t.Fatal("delta was not journaled", err)
			}
			preview := store.events[0].Payload["text"].(string)
			if strings.ContainsRune(preview, 0) || len(preview) > dgruntime.MaximumInlineMessageTextBytes {
				t.Fatal("unsafe or oversized delta")
			}
			if tc.name == "literal" && preview != tc.payload["text"] {
				t.Fatal("literal escape changed")
			}
		})
	}
}
