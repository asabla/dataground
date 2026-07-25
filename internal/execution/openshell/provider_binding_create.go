package openshell

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os/exec"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/asabla/dataground/internal/execution"
)

var credentialEvidenceProviderNamePattern = regexp.MustCompile(
	"^dg-canary-provider-[a-f0-9]{32}$",
)

type CredentialProviderRunner interface {
	RunWithCredentials(
		context.Context,
		string,
		map[string][]byte,
		...string,
	) (CommandResult, error)
}

// CreateCredentialEvidenceProvider creates only the run-derived Codex provider
// used by the credential evidence harness. The canary is supplied through bare
// credential keys in an isolated child environment and never enters argv.
func (provider *Provider) CreateCredentialEvidenceProvider(
	ctx context.Context,
	request execution.CredentialEvidenceProviderRequest,
) (execution.ProviderBinding, error) {
	if provider == nil ||
		ctx == nil ||
		isNilCredentialProviderRunner(provider.credentialProvider) ||
		provider.expected != credentialEvidenceOpenShellVersion ||
		request.IsolationDomainID == "" ||
		request.GatewayID == "" ||
		!credentialEvidenceProviderNamePattern.MatchString(request.Name) ||
		!validCredentialEvidenceCanary(request.Canary) {
		return execution.ProviderBinding{}, execution.ErrStateConflict
	}
	if err := ctx.Err(); err != nil {
		return execution.ProviderBinding{}, err
	}
	if err := provider.Check(ctx); err != nil {
		return execution.ProviderBinding{}, ErrProviderFailure
	}

	_, exists, _, err := provider.observeProviderBindingName(
		ctx,
		request.IsolationDomainID,
		request.GatewayID,
		request.Name,
	)
	if err != nil {
		return execution.ProviderBinding{}, ErrProviderFailure
	}
	if exists {
		return execution.ProviderBinding{}, execution.ErrStateConflict
	}
	gateway, err := provider.executionContext(
		ctx,
		request.IsolationDomainID,
		request.GatewayID,
	)
	if err != nil {
		return execution.ProviderBinding{}, ErrProviderFailure
	}

	keys := credentialEvidenceProviderKeys()
	credentials := make(map[string][]byte, len(keys))
	for _, key := range keys {
		credentials[key] = append([]byte(nil), request.Canary...)
	}
	args := provider.gatewayArgs(
		gateway.Endpoint,
		"provider",
		"create",
		"--name",
		request.Name,
		"--type",
		"codex",
	)
	for _, key := range keys {
		args = append(args, "--credential", key)
	}
	result, createErr := provider.credentialProvider.RunWithCredentials(
		ctx,
		provider.binary,
		credentials,
		args...,
	)
	for key := range credentials {
		clear(credentials[key])
		delete(credentials, key)
	}
	clear(result.Stdout)

	binding, exists, _, observeErr := provider.observeProviderBindingName(
		ctx,
		request.IsolationDomainID,
		request.GatewayID,
		request.Name,
	)
	if observeErr != nil {
		return execution.ProviderBinding{}, credentialProviderError(ctx)
	}
	if !exists {
		return execution.ProviderBinding{}, credentialProviderError(ctx)
	}
	if !validCreatedCredentialProvider(binding, keys) {
		return execution.ProviderBinding{}, execution.ErrStateConflict
	}
	if createErr != nil || result.ExitCode != 0 {
		// The deterministic name was absent before creation and is now bound to
		// the exact expected metadata. On the dedicated evidence gateway this
		// is the only safe lost-acknowledgement recovery signal.
	}

	return execution.ProviderBinding{
		IsolationDomainID: request.IsolationDomainID,
		GatewayID:         request.GatewayID,
		ID:                binding.ID,
		Name:              binding.Name,
		ResourceVersion:   binding.ResourceVersion,
	}, nil
}

func credentialEvidenceProviderKeys() [4]string {
	return [4]string{"access_token", "refresh_token", "account_id", "id_token"}
}

func validCreatedCredentialProvider(
	binding providerBindingView,
	expected [4]string,
) bool {
	if binding.ID == "" ||
		binding.Name == "" ||
		binding.Type != "codex" ||
		binding.ResourceVersion == 0 ||
		len(binding.CredentialKeys) != len(expected) {
		return false
	}
	actualKeys := slices.Clone(binding.CredentialKeys)
	sort.Strings(actualKeys)
	expectedKeys := expected[:]
	sort.Strings(expectedKeys)
	return slices.Equal(actualKeys, expectedKeys)
}

func validCredentialEvidenceCanary(canary []byte) bool {
	prefix := []byte("dataground-canary-v1:")
	if len(canary) != len(prefix)+43 || !bytes.Equal(canary[:len(prefix)], prefix) {
		return false
	}
	decoded := make([]byte, 32)
	count, err := base64.RawURLEncoding.Decode(decoded, canary[len(prefix):])
	valid := err == nil && count == len(decoded)
	clear(decoded)
	return valid
}

func credentialProviderError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrProviderFailure, err)
	}
	return ErrProviderFailure
}

func isNilCredentialProviderRunner(runner CredentialProviderRunner) bool {
	if runner == nil {
		return true
	}
	value := reflect.ValueOf(runner)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// ExecCredentialProviderRunner gives the OpenShell CLI only the four canary
// values it must resolve. It does not inherit the harness environment.
type ExecCredentialProviderRunner struct{}

func (ExecCredentialProviderRunner) RunWithCredentials(
	ctx context.Context,
	binary string,
	credentials map[string][]byte,
	args ...string,
) (CommandResult, error) {
	if ctx == nil || binary == "" || len(credentials) == 0 {
		return CommandResult{}, ErrProviderFailure
	}
	keys := make([]string, 0, len(credentials))
	for key, value := range credentials {
		if key == "" ||
			strings.ContainsRune(key, '=') ||
			len(value) == 0 {
			return CommandResult{}, ErrProviderFailure
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	command := exec.CommandContext(ctx, binary, args...)
	command.Env = make([]string, 0, len(keys))
	for _, key := range keys {
		command.Env = append(command.Env, key+"="+string(credentials[key]))
	}
	output, err := command.Output()
	for index := range command.Env {
		command.Env[index] = ""
	}
	command.Env = nil
	if err == nil {
		return CommandResult{Stdout: output}, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return CommandResult{Stdout: output, ExitCode: exitError.ExitCode()}, ErrProviderFailure
	}
	return CommandResult{Stdout: output, ExitCode: -1}, ErrProviderFailure
}

var (
	_ execution.CredentialEvidenceProviderProvisioner = (*Provider)(nil)
	_ CredentialProviderRunner                        = ExecCredentialProviderRunner{}
)
