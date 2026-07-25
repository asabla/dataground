package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
)

func TestObserveProviderBindingUsesExactGatewayAndIdentity(t *testing.T) {
	t.Parallel()

	ref := testProviderBindingRef()
	runner := &scriptedRunner{results: []scriptedResult{{
		result: CommandResult{Stdout: bindingJSON(t, providerBindingView{
			ID: ref.ID, Name: ref.Name, ResourceVersion: ref.ResourceVersion,
		})},
	}}}
	provider := providerBindingTestProvider(t, runner)
	observation, err := provider.ObserveProviderBinding(context.Background(), ref)
	if err != nil {
		t.Fatalf("ObserveProviderBinding() error = %v", err)
	}
	if !providerBindingMatchesRef(observation, ref) {
		t.Fatalf("observation = %+v", observation)
	}
	if len(runner.calls) != 1 ||
		!slices.Equal(runner.calls[0].args, []string{
			"--gateway-endpoint", "http://127.0.0.1:8080",
			"provider", "list", "--limit", "100", "--offset", "0", "--output", "json",
		}) {
		t.Fatalf("provider list calls = %#v", runner.calls)
	}
}

func TestObserveProviderBindingPaginatesToAbsence(t *testing.T) {
	t.Parallel()

	firstPage := make([]providerBindingView, providerBindingPageSize)
	for index := range firstPage {
		firstPage[index] = providerBindingView{
			ID: fmt.Sprintf("provider-%03d", index),
			Name: fmt.Sprintf("other-%03d", index),
			ResourceVersion: 1,
		}
	}
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: bindingJSON(t, firstPage...)}},
		{result: CommandResult{Stdout: []byte("[]")}},
	}}
	provider := providerBindingTestProvider(t, runner)
	observation, err := provider.ObserveProviderBinding(context.Background(), testProviderBindingRef())
	if err != nil {
		t.Fatalf("ObserveProviderBinding() error = %v", err)
	}
	if observation.Exists || observation.ObservedAt.IsZero() {
		t.Fatalf("absence observation = %+v", observation)
	}
	if len(runner.calls) != 2 ||
		!containsSequence(runner.calls[1].args, "--offset", "100") {
		t.Fatalf("pagination calls = %#v", runner.calls)
	}
}

func TestDeleteProviderBindingRejectsIdentityReplacement(t *testing.T) {
	t.Parallel()

	ref := testProviderBindingRef()
	runner := &scriptedRunner{results: []scriptedResult{{
		result: CommandResult{Stdout: bindingJSON(t, providerBindingView{
			ID: "replacement-id", Name: ref.Name, ResourceVersion: ref.ResourceVersion + 1,
		})},
	}}}
	provider := providerBindingTestProvider(t, runner)
	if err := provider.DeleteProviderBinding(context.Background(), ref); !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("DeleteProviderBinding() error = %v", err)
	}
	if len(runner.calls) != 1 || containsSequence(runner.calls[0].args, "provider", "delete") {
		t.Fatalf("replacement binding was deleted: %#v", runner.calls)
	}
}

func TestDeleteProviderBindingRecoversLostAcknowledgementByAbsence(t *testing.T) {
	t.Parallel()

	ref := testProviderBindingRef()
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: bindingJSON(t, providerBindingView{
			ID: ref.ID, Name: ref.Name, ResourceVersion: ref.ResourceVersion,
		})}},
		{err: errors.New("sensitive gateway failure")},
		{result: CommandResult{Stdout: []byte("[]")}},
	}}
	provider := providerBindingTestProvider(t, runner)
	if err := provider.DeleteProviderBinding(context.Background(), ref); err != nil {
		t.Fatalf("DeleteProviderBinding() error = %v", err)
	}
	if len(runner.calls) != 3 ||
		!containsSequence(runner.calls[1].args, "provider", "delete", ref.Name) {
		t.Fatalf("provider delete calls = %#v", runner.calls)
	}
}

func TestProviderBindingFailuresAreSanitized(t *testing.T) {
	t.Parallel()

	for name, result := range map[string]scriptedResult{
		"command":    {err: errors.New("sensitive command payload")},
		"json":       {result: CommandResult{Stdout: []byte(`[{"name":"secret"`)}},
		"oversized":  {result: CommandResult{Stdout: []byte(strings.Repeat("x", providerBindingMaxOutputBytes+1))}},
	} {
		name, result := name, result
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			provider := providerBindingTestProvider(t, &scriptedRunner{results: []scriptedResult{result}})
			_, err := provider.ObserveProviderBinding(context.Background(), testProviderBindingRef())
			if !errors.Is(err, ErrProviderFailure) {
				t.Fatalf("ObserveProviderBinding() error = %v", err)
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("provider error leaked upstream details: %v", err)
			}
		})
	}
}

func providerBindingTestProvider(t *testing.T, runner *scriptedRunner) *Provider {
	t.Helper()

	provider := New(Config{
		Now: func() time.Time {
			return time.Date(2026, time.July, 25, 18, 0, 0, 0, time.UTC)
		},
	}, runner)
	if _, err := provider.RegisterGateway(context.Background(), execution.GatewayRegistration{
		IsolationDomainID: "iso-a",
		ID:                "gateway-a",
		Endpoint:          "http://127.0.0.1:8080",
		Driver:            "docker",
	}); err != nil {
		t.Fatalf("RegisterGateway() error = %v", err)
	}
	return provider
}

func testProviderBindingRef() execution.ProviderBindingRef {
	return execution.ProviderBindingRef{
		IsolationDomainID: "iso-a",
		GatewayID:         "gateway-a",
		ID:                "provider-id",
		Name:              "dg-canary-provider-0123456789abcdef0123456789abcdef",
		ResourceVersion:   7,
	}
}

func bindingJSON(t *testing.T, bindings ...providerBindingView) []byte {
	t.Helper()

	encoded, err := json.Marshal(bindings)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return encoded
}
