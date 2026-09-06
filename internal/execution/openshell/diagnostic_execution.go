package openshell

import (
	"context"
	"regexp"

	"github.com/asabla/dataground/internal/execution"
)

var localDiagnosticImagePattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// CreateLocalDiagnostic accepts an immutable local Docker image ID only in the
// pinned loopback development topology. Ordinary Create still requires a
// registry digest. Placement, policy, provider selection and effect recovery use
// the same implementation; callers must never label this as certified evidence.
func (provider *Provider) CreateLocalDiagnostic(ctx context.Context, request execution.CreateRequest) (execution.Execution, error) {
	if provider == nil || ctx == nil || provider.expected != credentialEvidenceOpenShellVersion ||
		!localDiagnosticImagePattern.MatchString(request.Image) {
		return execution.Execution{}, execution.ErrStateConflict
	}
	gateway, err := provider.executionContext(ctx, request.IsolationDomainID, request.Placement.GatewayID)
	if err != nil || gateway.Gateway.Driver != "docker" || gateway.Endpoint != runtimeSSHGatewayEndpoint {
		return execution.Execution{}, execution.ErrStateConflict
	}
	return provider.create(ctx, request, true)
}
