package openshell

import (
	"context"
	"regexp"
	"slices"

	"github.com/asabla/dataground/internal/execution"
)

const runtimeConformanceCredentialMaxBytes = 64 << 10

var runtimeConformanceProviderNamePattern = regexp.MustCompile(
	"^dg-runtime-provider-[a-f0-9]{32}$",
)

// CreateRuntimeConformanceProvider creates only the run-derived Codex provider
// consumed by runtime conformance. Credential values enter the isolated child
// environment, never argv or the inherited launcher environment.
func (provider *Provider) CreateRuntimeConformanceProvider(
	ctx context.Context,
	request execution.RuntimeConformanceProviderRequest,
) (execution.ProviderBinding, error) {
	if provider == nil ||
		ctx == nil ||
		isNilCredentialProviderRunner(provider.credentialProvider) ||
		provider.expected != credentialEvidenceOpenShellVersion ||
		request.IsolationDomainID == "" ||
		request.GatewayID == "" ||
		!runtimeConformanceProviderNamePattern.MatchString(request.Name) ||
		!validRuntimeConformanceCredentials(request.Credentials) {
		return execution.ProviderBinding{}, execution.ErrStateConflict
	}
	if err := ctx.Err(); err != nil {
		return execution.ProviderBinding{}, err
	}
	if err := provider.Check(ctx); err != nil {
		return execution.ProviderBinding{}, ErrProviderFailure
	}

	ref := execution.RuntimeConformanceProviderRef{
		IsolationDomainID: request.IsolationDomainID,
		GatewayID:         request.GatewayID,
		Name:              request.Name,
	}
	before, err := provider.ObserveRuntimeConformanceProvider(ctx, ref)
	if err != nil {
		return execution.ProviderBinding{}, ErrProviderFailure
	}
	if before.Exists {
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

	credentials := runtimeConformanceCredentialMap(request.Credentials)
	defer clearRuntimeConformanceCredentialMap(credentials)
	keys := runtimeConformanceProviderKeys()
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
	result, _ := provider.credentialProvider.RunWithCredentials(
		ctx,
		provider.binary,
		credentials,
		args...,
	)
	clear(result.Stdout)

	recoveryCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		credentialProviderRecoveryTimeout,
	)
	defer cancel()
	after, err := provider.ObserveRuntimeConformanceProvider(recoveryCtx, ref)
	if err != nil || !after.Exists {
		return execution.ProviderBinding{}, credentialProviderError(ctx)
	}
	return execution.ProviderBinding{
		IsolationDomainID: after.IsolationDomainID,
		GatewayID:         after.GatewayID,
		ID:                after.ID,
		Name:              after.Name,
		ResourceVersion:   after.ResourceVersion,
	}, nil
}

// ObserveRuntimeConformanceProvider returns only credential-safe immutable
// metadata and rejects a same-name provider with a different type or key set.
func (provider *Provider) ObserveRuntimeConformanceProvider(
	ctx context.Context,
	ref execution.RuntimeConformanceProviderRef,
) (execution.ProviderBindingObservation, error) {
	if provider == nil ||
		ctx == nil ||
		ref.IsolationDomainID == "" ||
		ref.GatewayID == "" ||
		!runtimeConformanceProviderNamePattern.MatchString(ref.Name) {
		return execution.ProviderBindingObservation{}, execution.ErrStateConflict
	}
	binding, exists, observedAt, err := provider.observeProviderBindingName(
		ctx,
		ref.IsolationDomainID,
		ref.GatewayID,
		ref.Name,
	)
	if err != nil {
		return execution.ProviderBindingObservation{}, err
	}
	if !exists {
		return execution.ProviderBindingObservation{
			IsolationDomainID: ref.IsolationDomainID,
			GatewayID:         ref.GatewayID,
			Name:              ref.Name,
			ObservedAt:        observedAt,
		}, nil
	}
	if !validCreatedCredentialProvider(binding, runtimeConformanceProviderKeys()) {
		return execution.ProviderBindingObservation{}, execution.ErrStateConflict
	}
	return execution.ProviderBindingObservation{
		IsolationDomainID: ref.IsolationDomainID,
		GatewayID:         ref.GatewayID,
		ID:                binding.ID,
		Name:              binding.Name,
		ResourceVersion:   binding.ResourceVersion,
		Exists:            true,
		ObservedAt:        observedAt,
	}, nil
}

func runtimeConformanceProviderKeys() [4]string {
	return [4]string{"access_token", "refresh_token", "account_id", "id_token"}
}

func validRuntimeConformanceCredentials(credentials execution.RuntimeConformanceCredentials) bool {
	values := [...][]byte{
		credentials.AccessToken,
		credentials.RefreshToken,
		credentials.AccountID,
		credentials.IDToken,
	}
	for _, value := range values {
		if len(value) == 0 || len(value) > runtimeConformanceCredentialMaxBytes {
			return false
		}
	}
	return true
}

func runtimeConformanceCredentialMap(
	credentials execution.RuntimeConformanceCredentials,
) map[string][]byte {
	return map[string][]byte{
		"access_token":  slices.Clone(credentials.AccessToken),
		"refresh_token": slices.Clone(credentials.RefreshToken),
		"account_id":    slices.Clone(credentials.AccountID),
		"id_token":      slices.Clone(credentials.IDToken),
	}
}

func clearRuntimeConformanceCredentialMap(credentials map[string][]byte) {
	for key := range credentials {
		clear(credentials[key])
		delete(credentials, key)
	}
}

var _ execution.RuntimeConformanceProviderProvisioner = (*Provider)(nil)
