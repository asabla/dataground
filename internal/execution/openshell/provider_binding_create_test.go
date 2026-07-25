package openshell

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
)

func TestCreateCredentialEvidenceProviderUsesExactSecretFreeCommand(t *testing.T) {
	t.Parallel()

	view := testCreatedProviderView()
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("openshell 0.0.86")}},
		{result: CommandResult{Stdout: []byte("[]")}},
		{result: CommandResult{Stdout: bindingJSON(t, view)}},
	}}
	credentialRunner := &scriptedCredentialProviderRunner{}
	provider := credentialProviderTestProvider(t, runner, credentialRunner)
	request := testCredentialProviderRequest()
	binding, err := provider.CreateCredentialEvidenceProvider(context.Background(), request)
	if err != nil {
		t.Fatalf("CreateCredentialEvidenceProvider() error = %v", err)
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
	if credentialRunner.calls != 1 {
		t.Fatalf("credential runner calls = %d", credentialRunner.calls)
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
	if !slices.Equal(credentialRunner.args, expectedArgs) {
		t.Fatalf("create args = %#v", credentialRunner.args)
	}
	if strings.Contains(strings.Join(credentialRunner.args, " "), string(request.Canary)) {
		t.Fatal("canary plaintext entered provider argv")
	}
	for _, key := range credentialEvidenceProviderKeys() {
		if string(credentialRunner.observed[key]) != string(request.Canary) {
			t.Fatalf("credential %q did not receive the exact canary", key)
		}
	}
	for _, retained := range credentialRunner.retained {
		for _, value := range retained {
			if value != 0 {
				t.Fatal("credential runner retained canary plaintext")
			}
		}
	}
}

func TestCreateCredentialEvidenceProviderRecoversLostAcknowledgement(t *testing.T) {
	t.Parallel()

	view := testCreatedProviderView()
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("openshell 0.0.86")}},
		{result: CommandResult{Stdout: []byte("[]")}},
		{result: CommandResult{Stdout: bindingJSON(t, view)}},
	}}
	credentialRunner := &scriptedCredentialProviderRunner{
		err: errors.New("sensitive create acknowledgement"),
	}
	provider := credentialProviderTestProvider(t, runner, credentialRunner)
	binding, err := provider.CreateCredentialEvidenceProvider(
		context.Background(),
		testCredentialProviderRequest(),
	)
	if err != nil {
		t.Fatalf("CreateCredentialEvidenceProvider() error = %v", err)
	}
	if binding.ID != view.ID || binding.ResourceVersion != view.ResourceVersion {
		t.Fatalf("binding = %+v", binding)
	}
}

func TestCreateCredentialEvidenceProviderObservesAfterCancellation(t *testing.T) {
	t.Parallel()

	view := testCreatedProviderView()
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("openshell 0.0.86")}},
		{result: CommandResult{Stdout: []byte("[]")}},
		{result: CommandResult{Stdout: bindingJSON(t, view)}},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	credentialRunner := &scriptedCredentialProviderRunner{
		after: cancel,
		err:   context.Canceled,
	}
	provider := credentialProviderTestProvider(t, runner, credentialRunner)
	binding, err := provider.CreateCredentialEvidenceProvider(ctx, testCredentialProviderRequest())
	if err != nil {
		t.Fatalf("CreateCredentialEvidenceProvider() error = %v", err)
	}
	if binding.ID != view.ID || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("binding = %+v, context error = %v", binding, ctx.Err())
	}
}

func TestCreateCredentialEvidenceProviderRejectsPreexistingName(t *testing.T) {
	t.Parallel()

	view := testCreatedProviderView()
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("openshell 0.0.86")}},
		{result: CommandResult{Stdout: bindingJSON(t, view)}},
	}}
	credentialRunner := &scriptedCredentialProviderRunner{}
	provider := credentialProviderTestProvider(t, runner, credentialRunner)
	_, err := provider.CreateCredentialEvidenceProvider(
		context.Background(),
		testCredentialProviderRequest(),
	)
	if !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("CreateCredentialEvidenceProvider() error = %v", err)
	}
	if credentialRunner.calls != 0 {
		t.Fatalf("preexisting binding triggered %d create calls", credentialRunner.calls)
	}
}

func TestCreateCredentialEvidenceProviderRejectsMetadataDrift(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*providerBindingView){
		"type": func(view *providerBindingView) {
			view.Type = "other"
		},
		"credentials": func(view *providerBindingView) {
			view.CredentialKeys = []string{"access_token"}
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			view := testCreatedProviderView()
			mutate(&view)
			runner := &scriptedRunner{results: []scriptedResult{
				{result: CommandResult{Stdout: []byte("openshell 0.0.86")}},
				{result: CommandResult{Stdout: []byte("[]")}},
				{result: CommandResult{Stdout: bindingJSON(t, view)}},
			}}
			provider := credentialProviderTestProvider(
				t,
				runner,
				&scriptedCredentialProviderRunner{},
			)
			_, err := provider.CreateCredentialEvidenceProvider(
				context.Background(),
				testCredentialProviderRequest(),
			)
			if !errors.Is(err, execution.ErrStateConflict) {
				t.Fatalf("CreateCredentialEvidenceProvider() error = %v", err)
			}
		})
	}
}

func TestCreateCredentialEvidenceProviderFailsWhenPostStateIsUncertain(t *testing.T) {
	t.Parallel()

	for name, result := range map[string]scriptedResult{
		"absent":  {result: CommandResult{Stdout: []byte("[]")}},
		"failure": {err: errors.New("sensitive observation payload")},
	} {
		name, result := name, result
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runner := &scriptedRunner{results: []scriptedResult{
				{result: CommandResult{Stdout: []byte("openshell 0.0.86")}},
				{result: CommandResult{Stdout: []byte("[]")}},
				result,
			}}
			provider := credentialProviderTestProvider(
				t,
				runner,
				&scriptedCredentialProviderRunner{},
			)
			_, err := provider.CreateCredentialEvidenceProvider(
				context.Background(),
				testCredentialProviderRequest(),
			)
			if !errors.Is(err, ErrProviderFailure) {
				t.Fatalf("CreateCredentialEvidenceProvider() error = %v", err)
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("provider error leaked details: %v", err)
			}
		})
	}
}

func TestCreateCredentialEvidenceProviderValidatesBeforeMutation(t *testing.T) {
	t.Parallel()

	var typedNil *scriptedCredentialProviderRunner
	for name, mutate := range map[string]func(*Provider, *execution.CredentialEvidenceProviderRequest){
		"version": func(provider *Provider, _ *execution.CredentialEvidenceProviderRequest) {
			provider.expected = "0.0.85"
		},
		"name": func(_ *Provider, request *execution.CredentialEvidenceProviderRequest) {
			request.Name = "other"
		},
		"canary": func(_ *Provider, request *execution.CredentialEvidenceProviderRequest) {
			request.Canary = []byte("invalid")
		},
		"runner": func(provider *Provider, _ *execution.CredentialEvidenceProviderRequest) {
			provider.credentialProvider = typedNil
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runner := &scriptedRunner{}
			credentialRunner := &scriptedCredentialProviderRunner{}
			provider := credentialProviderTestProvider(t, runner, credentialRunner)
			request := testCredentialProviderRequest()
			mutate(provider, &request)
			_, err := provider.CreateCredentialEvidenceProvider(context.Background(), request)
			if !errors.Is(err, execution.ErrStateConflict) {
				t.Fatalf("CreateCredentialEvidenceProvider() error = %v", err)
			}
			if len(runner.calls) != 0 || credentialRunner.calls != 0 {
				t.Fatalf("invalid request reached native runners")
			}
		})
	}
}

func TestCreateCredentialEvidenceProviderHonorsCancellation(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{}
	credentialRunner := &scriptedCredentialProviderRunner{}
	provider := credentialProviderTestProvider(t, runner, credentialRunner)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := provider.CreateCredentialEvidenceProvider(ctx, testCredentialProviderRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateCredentialEvidenceProvider() error = %v", err)
	}
	if len(runner.calls) != 0 || credentialRunner.calls != 0 {
		t.Fatal("cancelled request reached native runners")
	}
}

type scriptedCredentialProviderRunner struct {
	calls    int
	args     []string
	observed map[string][]byte
	retained [][]byte
	result   CommandResult
	err      error
	after    func()
}

func (runner *scriptedCredentialProviderRunner) RunWithCredentials(
	_ context.Context,
	_ string,
	credentials map[string][]byte,
	args ...string,
) (CommandResult, error) {
	runner.calls++
	runner.args = slices.Clone(args)
	runner.observed = make(map[string][]byte, len(credentials))
	for key, value := range credentials {
		runner.observed[key] = append([]byte(nil), value...)
		runner.retained = append(runner.retained, value)
	}
	if runner.after != nil {
		runner.after()
	}
	return runner.result, runner.err
}

func credentialProviderTestProvider(
	t *testing.T,
	runner *scriptedRunner,
	credentialRunner CredentialProviderRunner,
) *Provider {
	t.Helper()

	provider := New(Config{
		ExpectedVersion:          credentialEvidenceOpenShellVersion,
		CredentialProviderRunner: credentialRunner,
		Now: func() time.Time {
			return time.Date(2026, time.July, 26, 8, 0, 0, 0, time.UTC)
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

func testCredentialProviderRequest() execution.CredentialEvidenceProviderRequest {
	entropy := make([]byte, 32)
	canary := "dataground-canary-v1:" + base64.RawURLEncoding.EncodeToString(entropy)
	return execution.CredentialEvidenceProviderRequest{
		IsolationDomainID: "iso-a",
		GatewayID:         "gateway-a",
		Name:              "dg-canary-provider-0123456789abcdef0123456789abcdef",
		Canary:            []byte(canary),
	}
}

func testCreatedProviderView() providerBindingView {
	return providerBindingView{
		ID:              "provider-id",
		Name:            "dg-canary-provider-0123456789abcdef0123456789abcdef",
		Type:            "codex",
		CredentialKeys:  []string{"refresh_token", "id_token", "account_id", "access_token"},
		ResourceVersion: 7,
	}
}
