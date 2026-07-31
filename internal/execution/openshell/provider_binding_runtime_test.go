package openshell

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/execution"
)

func TestCreateRuntimeConformanceProviderUsesExactSecretFreeCommand(t *testing.T) {
	t.Parallel()

	view := testRuntimeConformanceProviderView()
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("openshell 0.0.86")}},
		{result: CommandResult{Stdout: []byte("[]")}},
		{result: CommandResult{Stdout: bindingJSON(t, view)}},
	}}
	credentialRunner := &scriptedCredentialProviderRunner{}
	provider := credentialProviderTestProvider(t, runner, credentialRunner)
	request := testRuntimeConformanceProviderRequest()
	binding, err := provider.CreateRuntimeConformanceProvider(context.Background(), request)
	if err != nil {
		t.Fatalf("CreateRuntimeConformanceProvider() error = %v", err)
	}
	if binding != (execution.ProviderBinding{
		IsolationDomainID: request.IsolationDomainID,
		GatewayID:         request.GatewayID,
		ID:                view.ID,
		Name:              view.Name,
		ResourceVersion:   view.ResourceVersion,
	}) {
		t.Fatalf("binding = %+v", binding)
	}
	expectedArgs := []string{
		"--gateway-endpoint", "http://127.0.0.1:8080",
		"provider", "create",
		"--name", request.Name,
		"--type", "codex",
		"--credential", "access_token",
		"--credential", "refresh_token",
		"--credential", "account_id",
		"--credential", "id_token",
	}
	if credentialRunner.calls != 1 || !slices.Equal(credentialRunner.args, expectedArgs) {
		t.Fatalf("provider create args = %#v", credentialRunner.args)
	}
	for key, expected := range map[string][]byte{
		"access_token":  request.Credentials.AccessToken,
		"refresh_token": request.Credentials.RefreshToken,
		"account_id":    request.Credentials.AccountID,
		"id_token":      request.Credentials.IDToken,
	} {
		if !slices.Equal(credentialRunner.observed[key], expected) {
			t.Fatalf("credential %q did not receive the exact value", key)
		}
		if strings.Contains(strings.Join(credentialRunner.args, " "), string(expected)) {
			t.Fatalf("credential %q entered provider argv", key)
		}
	}
	for _, retained := range credentialRunner.retained {
		if !runtimeCredentialsCleared(retained) {
			t.Fatal("credential runner retained runtime credential plaintext")
		}
	}
}

func TestCreateRuntimeConformanceProviderRecoversLostAcknowledgement(t *testing.T) {
	t.Parallel()

	view := testRuntimeConformanceProviderView()
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("openshell 0.0.86")}},
		{result: CommandResult{Stdout: []byte("[]")}},
		{result: CommandResult{Stdout: bindingJSON(t, view)}},
	}}
	provider := credentialProviderTestProvider(t, runner, &scriptedCredentialProviderRunner{
		err: errors.New("private create acknowledgement"),
	})
	binding, err := provider.CreateRuntimeConformanceProvider(
		context.Background(),
		testRuntimeConformanceProviderRequest(),
	)
	if err != nil || binding.ID != view.ID {
		t.Fatalf("binding = %+v, error = %v", binding, err)
	}
}

func TestObserveRuntimeConformanceProviderRejectsMetadataSubstitution(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*providerBindingView){
		"type": func(view *providerBindingView) { view.Type = "openai" },
		"keys": func(view *providerBindingView) { view.CredentialKeys = []string{"access_token"} },
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			view := testRuntimeConformanceProviderView()
			mutate(&view)
			provider := credentialProviderTestProvider(t, &scriptedRunner{results: []scriptedResult{{
				result: CommandResult{Stdout: bindingJSON(t, view)},
			}}}, &scriptedCredentialProviderRunner{})
			_, err := provider.ObserveRuntimeConformanceProvider(
				context.Background(),
				execution.RuntimeConformanceProviderRef{
					IsolationDomainID: "iso-a",
					GatewayID:         "gateway-a",
					Name:              view.Name,
				},
			)
			if !errors.Is(err, execution.ErrStateConflict) {
				t.Fatalf("ObserveRuntimeConformanceProvider() error = %v", err)
			}
		})
	}
}

func TestCreateRuntimeConformanceProviderValidatesBeforeMutation(t *testing.T) {
	t.Parallel()

	request := testRuntimeConformanceProviderRequest()
	request.Credentials.AccessToken = nil
	runner := &scriptedRunner{}
	credentialRunner := &scriptedCredentialProviderRunner{}
	provider := credentialProviderTestProvider(t, runner, credentialRunner)
	_, err := provider.CreateRuntimeConformanceProvider(context.Background(), request)
	if !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("CreateRuntimeConformanceProvider() error = %v", err)
	}
	if len(runner.calls) != 0 || credentialRunner.calls != 0 {
		t.Fatal("invalid runtime credentials reached native runners")
	}
}

func testRuntimeConformanceProviderRequest() execution.RuntimeConformanceProviderRequest {
	return execution.RuntimeConformanceProviderRequest{
		IsolationDomainID: "iso-a",
		GatewayID:         "gateway-a",
		Name:              "dg-runtime-provider-0123456789abcdef0123456789abcdef",
		Credentials: execution.RuntimeConformanceCredentials{
			AccessToken:  []byte("access-value"),
			RefreshToken: []byte("refresh-value"),
			AccountID:    []byte("account-value"),
			IDToken:      []byte("id-value"),
		},
	}
}

func testRuntimeConformanceProviderView() providerBindingView {
	return providerBindingView{
		ID:              "runtime-provider-id",
		Name:            "dg-runtime-provider-0123456789abcdef0123456789abcdef",
		Type:            "codex",
		CredentialKeys:  []string{"refresh_token", "id_token", "account_id", "access_token"},
		ResourceVersion: 7,
	}
}

func runtimeCredentialsCleared(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}
