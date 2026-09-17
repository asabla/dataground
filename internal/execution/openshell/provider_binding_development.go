package openshell

import (
	"context"

	"github.com/asabla/dataground/internal/execution"
)

// CreateDevelopmentProvider is the separate operator boundary for the fixed
// codex profile on a dedicated development gateway. It grants no workload access.
func (provider *Provider) CreateDevelopmentProvider(ctx context.Context, isolationID, gatewayID string, credentials execution.RuntimeConformanceCredentials) (execution.ProviderBinding, error) {
	return provider.createCodexProvider(ctx, execution.RuntimeConformanceProviderRequest{
		IsolationDomainID: isolationID, GatewayID: gatewayID, Name: "codex", Credentials: credentials,
	})
}

func (provider *Provider) ObserveDevelopmentProvider(ctx context.Context, isolationID, gatewayID string) (execution.ProviderBindingObservation, error) {
	return provider.observeCodexProvider(ctx, execution.RuntimeConformanceProviderRef{IsolationDomainID: isolationID, GatewayID: gatewayID, Name: "codex"})
}

// CheckDevelopmentProviderProfiles observes the gateway opt-in without changing it.
func (provider *Provider) CheckDevelopmentProviderProfiles(ctx context.Context, isolationID, gatewayID string) error {
	if provider == nil || ctx == nil || provider.expected != credentialEvidenceOpenShellVersion {
		return ErrProviderFailure
	}
	gateway, err := provider.executionContext(ctx, isolationID, gatewayID)
	if err != nil {
		return ErrProviderFailure
	}
	enabled, err := provider.providerProfilesEnabled(ctx, gateway.Endpoint)
	if err != nil || !enabled {
		return ErrProviderSettingsVerification
	}
	return nil
}
