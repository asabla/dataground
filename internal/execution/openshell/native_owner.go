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
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrRuntimeTransport
	}
	// The SSH channel must survive cancellation long enough for the original
	// owner to acknowledge shutdown. Startup remains caller-bound; after the
	// handshake nativehost owns bounded cleanup and closes this private context.
	ownerCtx, cancelOwner := context.WithCancel(context.WithoutCancel(ctx))
	stopStartup := context.AfterFunc(ctx, cancelOwner)
	defer stopStartup()
	raw, err := provider.startSandboxCommand(ownerCtx, ref, "/usr/local/bin/dataground-native-owner")
	if err != nil {
		cancelOwner()
		return nil, ErrRuntimeTransport
	}
	transport := &ownedRuntimeTransport{RuntimeSession: raw, cancel: cancelOwner}
	session, err := nativehost.NewSession(ctx, transport)
	if err != nil {
		_ = transport.Close()
		return nil, ErrRuntimeTransport
	}
	if !stopStartup() || ctx.Err() != nil {
		_ = session.Close()
		return nil, ErrRuntimeTransport
	}
	return session, nil
}

type ownedRuntimeTransport struct {
	execution.RuntimeSession
	cancel context.CancelFunc
}

func (transport *ownedRuntimeTransport) Close() error {
	defer transport.cancel()
	return transport.RuntimeSession.Close()
}
