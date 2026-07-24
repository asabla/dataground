package execution

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestProviderProfileRegistryResolvesOwnedExactSelection(t *testing.T) {
	registry, err := NewProviderProfileRegistry([]string{"openai-codex", "anthropic.claude"})
	if err != nil {
		t.Fatalf("construct provider profile registry: %v", err)
	}
	requested := []string{"openai-codex", "anthropic.claude", "openai-codex"}
	resolved, err := registry.Resolve(requested)
	if err != nil {
		t.Fatalf("resolve provider profiles: %v", err)
	}
	requested[0] = "changed"
	want := []string{"anthropic.claude", "openai-codex"}
	if !reflect.DeepEqual(resolved, want) {
		t.Fatalf("resolved profiles = %#v, want %#v", resolved, want)
	}
}

func TestProviderProfileRegistryRejectsInvalidDuplicateAndUnknownProfiles(t *testing.T) {
	for _, names := range [][]string{
		{""},
		{"Codex"},
		{"codex=secret"},
		{"../codex"},
		{"codex", "codex"},
	} {
		if _, err := NewProviderProfileRegistry(names); !errors.Is(err, ErrProviderProfileInvalid) {
			t.Fatalf("registry %q error = %v, want invalid", names, err)
		}
	}
	registry, err := NewProviderProfileRegistry([]string{"codex"})
	if err != nil {
		t.Fatalf("construct provider profile registry: %v", err)
	}
	if _, err := registry.Resolve([]string{"other"}); !errors.Is(err, ErrProviderProfileUnavailable) {
		t.Fatalf("unknown profile error = %v, want unavailable", err)
	}
	if _, err := registry.Resolve([]string{"codex=secret"}); !errors.Is(err, ErrProviderProfileInvalid) {
		t.Fatalf("invalid selection error = %v, want invalid", err)
	}
}

func TestProviderProfileRegistryFailsClosedWithoutConfiguration(t *testing.T) {
	var registry *ProviderProfileRegistry
	resolved, err := registry.Resolve(nil)
	if err != nil || resolved != nil {
		t.Fatalf("empty selection without registry = %#v, %v", resolved, err)
	}
	if _, err := registry.Resolve([]string{"codex"}); !errors.Is(err, ErrProviderProfileUnavailable) {
		t.Fatalf("configured selection without registry = %v, want unavailable", err)
	}
}

func TestProviderProfileRegistryDoesNotSerializeConfiguration(t *testing.T) {
	registry, err := NewProviderProfileRegistry([]string{"codex"})
	if err != nil {
		t.Fatalf("construct provider profile registry: %v", err)
	}
	encoded, err := json.Marshal(registry)
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("registry serialized deployment configuration: %s", encoded)
	}
}
