package openshell

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/execution"
)

func TestDevelopmentProviderUsesFixedProfileAndClearsTransferredCredentials(t *testing.T) {
	view := testRuntimeConformanceProviderView()
	view.Name = "codex"
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("openshell 0.0.86")}},
		{result: CommandResult{Stdout: []byte("[]")}},
		{result: CommandResult{Stdout: bindingJSON(t, view)}},
	}}
	credentialRunner := &scriptedCredentialProviderRunner{err: errors.New("private lost acknowledgement")}
	provider := credentialProviderTestProvider(t, runner, credentialRunner)
	request := testRuntimeConformanceProviderRequest()
	binding, err := provider.CreateDevelopmentProvider(context.Background(), request.IsolationDomainID, request.GatewayID, request.Credentials)
	if err != nil || binding.Name != "codex" || binding.ID != view.ID {
		t.Fatal(binding, err)
	}
	expected := []string{"--gateway-endpoint", "http://127.0.0.1:8080", "provider", "create", "--name", "codex", "--type", "codex", "--credential", "access_token", "--credential", "refresh_token", "--credential", "account_id", "--credential", "id_token"}
	if !slices.Equal(credentialRunner.args, expected) {
		t.Fatal("unexpected provider command")
	}
	for _, secret := range credentialRunner.observed {
		if strings.Contains(strings.Join(credentialRunner.args, " "), string(secret)) {
			t.Fatal("credential exposure")
		}
	}
	for _, retained := range credentialRunner.retained {
		if !runtimeCredentialsCleared(retained) {
			t.Fatal("credential retained")
		}
	}
	// The additional entry point does not widen conformance's allowed names.
	request.Name = "codex"
	if _, err := provider.CreateRuntimeConformanceProvider(context.Background(), request); !errors.Is(err, execution.ErrStateConflict) {
		t.Fatal("conformance scope widened", err)
	}
}

func TestDevelopmentProviderRejectsProfileMetadataAndDisabledSetting(t *testing.T) {
	for _, mutate := range []func(*providerBindingView){
		func(v *providerBindingView) { v.Type = "openai" },
		func(v *providerBindingView) { v.CredentialKeys = []string{"access_token"} },
		func(v *providerBindingView) { v.ResourceVersion = 0 },
	} {
		view := testRuntimeConformanceProviderView()
		view.Name = "codex"
		mutate(&view)
		provider := credentialProviderTestProvider(t, &scriptedRunner{results: []scriptedResult{{result: CommandResult{Stdout: bindingJSON(t, view)}}}}, &scriptedCredentialProviderRunner{})
		if _, err := provider.ObserveDevelopmentProvider(context.Background(), "iso-a", "gateway-a"); err == nil {
			t.Fatal("substituted metadata accepted")
		}
	}
	for _, enabled := range []string{"true", "false"} {
		runner := &scriptedRunner{results: []scriptedResult{{result: CommandResult{Stdout: []byte(`{"scope":"global","settings":{"providers_v2_enabled":"` + enabled + `"}}`)}}}}
		provider := credentialProviderTestProvider(t, runner, &scriptedCredentialProviderRunner{})
		err := provider.CheckDevelopmentProviderProfiles(context.Background(), "iso-a", "gateway-a")
		if (err == nil) != (enabled == "true") {
			t.Fatal("wrong settings outcome", err)
		}
	}
}
