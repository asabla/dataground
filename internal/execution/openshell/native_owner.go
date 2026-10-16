package openshell

import (
	"context"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/runtime/nativehost"
)

// StartOwnedRuntime is an internal opt-in transport for an exact sandbox. Its
// caller must select an accepted owner-capable image and prevent other workspace
// writers. It does not change the default runtime, grant export authority or
// permit teardown. An unconfirmed terminal exchange must retain the workspace.
func (provider *Provider) StartOwnedRuntime(ctx context.Context, ref execution.ExecutionRef) (*nativehost.Session, error) {
	raw, err := provider.startSandboxCommand(ctx, ref, "/usr/local/bin/dataground-native-owner")
	if err != nil {
		return nil, ErrRuntimeTransport
	}
	session, err := nativehost.NewSession(ctx, raw)
	if err != nil {
		return nil, ErrRuntimeTransport
	}
	return session, nil
}
