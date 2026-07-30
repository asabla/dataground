package openshell

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/asabla/dataground/internal/execution"
)

const providerSettingsEndpoint = "http://127.0.0.1:8080"

func TestEnableProviderProfilesObservesMutatesAndReobserves(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte(`{"scope":"global","settings_revision":0,"settings":{}}`)}},
		{result: CommandResult{Stdout: []byte("updated")}},
		{result: CommandResult{Stdout: []byte(`{"scope":"global","settings_revision":1,"settings":{"providers_v2_enabled":"true"}}`)}},
	}}
	provider := providerWithGateway(t, runner)

	if err := provider.EnableProviderProfiles(context.Background(), "iso-a", "gateway-a"); err != nil {
		t.Fatalf("enable provider profiles: %v", err)
	}

	expected := [][]string{
		{
			"--gateway-endpoint", providerSettingsEndpoint,
			"settings", "get", "--global", "--json",
		},
		{
			"--gateway-endpoint", providerSettingsEndpoint,
			"settings", "set", "--global",
			"--key", providerProfilesSetting,
			"--value", "true",
			"--yes",
		},
		{
			"--gateway-endpoint", providerSettingsEndpoint,
			"settings", "get", "--global", "--json",
		},
	}
	if len(runner.calls) != len(expected) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, expected)
	}
	for index := range expected {
		if runner.calls[index].binary != "openshell" ||
			!reflect.DeepEqual(runner.calls[index].args, expected[index]) {
			t.Fatalf("call %d = %#v, want %#v", index, runner.calls[index], expected[index])
		}
	}
}

func TestEnableProviderProfilesIsReadOnlyWhenAlreadyEnabled(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte(`{"scope":"global","settings_revision":4,"settings":{"providers_v2_enabled":"true"}}`)}},
	}}
	provider := providerWithGateway(t, runner)

	if err := provider.EnableProviderProfiles(context.Background(), "iso-a", "gateway-a"); err != nil {
		t.Fatalf("observe enabled provider profiles: %v", err)
	}
	if len(runner.calls) != 1 ||
		!containsSequence(runner.calls[0].args, "settings", "get", "--global", "--json") {
		t.Fatalf("preconfigured gateway mutated: %#v", runner.calls)
	}
}

func TestEnableProviderProfilesRecoversLostAcknowledgementByObservation(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte(`{"scope":"global","settings_revision":0,"settings":{}}`)}},
		{result: CommandResult{FailureClass: NativeFailureNetwork}, err: errors.New("lost acknowledgement")},
		{result: CommandResult{Stdout: []byte(`{"scope":"global","settings_revision":1,"settings":{"providers_v2_enabled":"true"}}`)}},
	}}
	provider := providerWithGateway(t, runner)

	if err := provider.EnableProviderProfiles(context.Background(), "iso-a", "gateway-a"); err != nil {
		t.Fatalf("recover enabled provider profiles: %v", err)
	}
}

func TestEnableProviderProfilesRejectsUnobservedMutation(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte(`{"scope":"global","settings_revision":0,"settings":{}}`)}},
		{result: CommandResult{}},
		{result: CommandResult{Stdout: []byte(`{"scope":"global","settings_revision":1,"settings":{"providers_v2_enabled":"false"}}`)}},
	}}
	provider := providerWithGateway(t, runner)

	if err := provider.EnableProviderProfiles(context.Background(), "iso-a", "gateway-a"); !errors.Is(err, ErrProviderFailure) || !errors.Is(err, ErrProviderSettingsVerification) {
		t.Fatalf("unobserved setting = %v, want settings verification failure", err)
	}
}

func TestEnableProviderProfilesRejectsCancellationBeforeMutation(t *testing.T) {
	runner := &scriptedRunner{}
	provider := providerWithGateway(t, runner)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := provider.EnableProviderProfiles(ctx, "iso-a", "gateway-a"); err == nil {
		t.Fatal("cancelled provider setting accepted")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("cancelled provider setting reached native command: %#v", runner.calls)
	}
}

func TestEnableProviderProfilesPreservesObservationFailure(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{{
		result: CommandResult{FailureClass: NativeFailureNetwork},
		err:    errors.New("observation failed"),
	}}}
	provider := providerWithGateway(t, runner)

	err := provider.EnableProviderProfiles(context.Background(), "iso-a", "gateway-a")
	if !errors.Is(err, ErrProviderSettingsObservation) ||
		NativeFailureClassOf(err) != NativeFailureNetwork {
		t.Fatalf("settings observation error = %v", err)
	}
}

func TestEnableProviderProfilesPreservesMutationFailure(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte(`{"scope":"global","settings_revision":0,"settings":{}}`)}},
		{result: CommandResult{FailureClass: NativeFailurePermission}, err: errors.New("mutation failed")},
		{result: CommandResult{Stdout: []byte(`{"scope":"global","settings_revision":0,"settings":{}}`)}},
	}}
	provider := providerWithGateway(t, runner)

	err := provider.EnableProviderProfiles(context.Background(), "iso-a", "gateway-a")
	if !errors.Is(err, ErrProviderSettingsMutation) ||
		NativeFailureClassOf(err) != NativeFailurePermission {
		t.Fatalf("settings mutation error = %v", err)
	}
}

func providerWithGateway(t *testing.T, runner *scriptedRunner) *Provider {
	t.Helper()
	provider := New(Config{}, runner)
	if _, err := provider.RegisterGateway(context.Background(), execution.GatewayRegistration{
		IsolationDomainID: "iso-a",
		ID:                "gateway-a",
		Endpoint:          providerSettingsEndpoint,
		Driver:            "docker",
		Capabilities:      []string{"codex.app-server"},
	}); err != nil {
		t.Fatalf("register gateway: %v", err)
	}
	return provider
}
