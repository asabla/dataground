package reconcile

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestCodexInvocationRuntimeRequestBuilderMapsDurableProfile(t *testing.T) {
	builder := CodexInvocationRuntimeRequestBuilder{}
	target := codexRuntimeRequestTarget()
	first, err := builder.BuildInvocationRuntimeRequest(target)
	if err != nil {
		t.Fatal(err)
	}
	second, err := builder.BuildInvocationRuntimeRequest(target)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("runtime request is not deterministic: %#v and %#v", first, second)
	}
	if first.Prompt != "persisted prompt" ||
		first.ApprovalMode != dgruntime.ApprovalLocked ||
		first.SandboxMode != dgruntime.SandboxReadOnly ||
		first.Model != "" ||
		first.WorkingDir != "" {
		t.Fatalf("runtime request = %#v", first)
	}
	properties := first.OutputSchema["properties"].(map[string]any)
	properties["answer"] = map[string]any{"type": "number"}
	persistedProperties := target.OutputSchema["properties"].(map[string]any)
	if persistedProperties["answer"].(map[string]any)["type"] != "string" {
		t.Fatal("runtime request retained the persisted output-schema map")
	}
}

func TestCodexInvocationRuntimeRequestBuilderRejectsAmbiguousInputs(t *testing.T) {
	tests := map[string]struct {
		change func(*persistence.InvocationRuntimeTarget)
		want   error
	}{
		"unknown profile": {
			change: func(target *persistence.InvocationRuntimeTarget) {
				target.RuntimeProfile = "codex.app-server/v2"
			},
			want: ErrInvocationRuntimeProfileUnsupported,
		},
		"missing prompt": {
			change: func(target *persistence.InvocationRuntimeTarget) {
				target.Input = map[string]any{}
			},
			want: ErrInvocationRuntimeInputInvalid,
		},
		"non-string prompt": {
			change: func(target *persistence.InvocationRuntimeTarget) {
				target.Input = map[string]any{"prompt": 7}
			},
			want: ErrInvocationRuntimeInputInvalid,
		},
		"empty prompt": {
			change: func(target *persistence.InvocationRuntimeTarget) {
				target.Input = map[string]any{"prompt": ""}
			},
			want: ErrInvocationRuntimeInputInvalid,
		},
		"nul prompt": {
			change: func(target *persistence.InvocationRuntimeTarget) {
				target.Input = map[string]any{"prompt": "unsafe\x00prompt"}
			},
			want: ErrInvocationRuntimeInputInvalid,
		},
		"oversized prompt": {
			change: func(target *persistence.InvocationRuntimeTarget) {
				target.Input = map[string]any{
					"prompt": strings.Repeat("a", maximumCodexInvocationPromptBytes+1),
				}
			},
			want: ErrInvocationRuntimeInputInvalid,
		},
		"hidden native override": {
			change: func(target *persistence.InvocationRuntimeTarget) {
				target.Input = map[string]any{
					"prompt": "persisted prompt",
					"model":  "caller-controlled",
				}
			},
			want: ErrInvocationRuntimeInputInvalid,
		},
		"non-json output schema": {
			change: func(target *persistence.InvocationRuntimeTarget) {
				target.OutputSchema = map[string]any{"minimum": math.Inf(1)}
			},
			want: ErrInvocationRuntimeOutputSchemaInvalid,
		},
		"oversized output schema": {
			change: func(target *persistence.InvocationRuntimeTarget) {
				target.OutputSchema = map[string]any{
					"description": strings.Repeat(
						"a",
						maximumCodexInvocationOutputSchemaBytes+1,
					),
				}
			},
			want: ErrInvocationRuntimeOutputSchemaInvalid,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			target := codexRuntimeRequestTarget()
			test.change(&target)
			_, err := (CodexInvocationRuntimeRequestBuilder{}).
				BuildInvocationRuntimeRequest(target)
			if !errors.Is(err, test.want) {
				t.Fatalf("runtime request error = %v, want %v", err, test.want)
			}
		})
	}
}

func codexRuntimeRequestTarget() persistence.InvocationRuntimeTarget {
	return persistence.InvocationRuntimeTarget{
		RuntimeProfile: CodexAppServerRuntimeProfileV1,
		Input:          map[string]any{"prompt": "persisted prompt"},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"answer": map[string]any{"type": "string"},
			},
		},
	}
}
