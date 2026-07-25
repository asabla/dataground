package canarysandbox

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sync"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/security/canaryevidence"
)

var (
	ErrInvalidConfiguration = errors.New("invalid credential sandbox cleanup configuration")
	ErrCleanupFailure       = errors.New("credential sandbox cleanup failed")
	ErrCleanupUncertain     = errors.New("credential sandbox cleanup is uncertain")
	ErrSerialization        = errors.New("credential sandbox cleanup cannot be serialized")
	runIDPattern            = regexp.MustCompile("^[a-f0-9]{32}$")
	resourceNamePattern     = regexp.MustCompile("^[a-z0-9][a-z0-9._-]{0,127}$")
)

type Provider interface {
	Terminate(context.Context, execution.ExecutionRef) error
	Observe(context.Context, execution.ExecutionRef) (execution.Observation, error)
}

type Config struct {
	RunID     string
	Execution execution.Execution
}

// Adapter owns cleanup of one DataGround execution returned by the OpenShell
// provider. Native sandbox names and gateway coordinates remain provider-private.
type Adapter struct {
	state *adapterState
}

type adapterState struct {
	mu       sync.Mutex
	runID    string
	ref      execution.ExecutionRef
	provider Provider
	removed  bool
}

// New binds cleanup to one run and the exact persisted DataGround execution.
// The execution ID is the only sandbox identity exposed to evidence.
func New(config Config, provider Provider) (*Adapter, error) {
	if provider == nil ||
		!runIDPattern.MatchString(config.RunID) ||
		config.Execution.IsolationDomainID == "" ||
		!resourceNamePattern.MatchString(config.Execution.ID) ||
		config.Execution.GatewayID == "" {
		return nil, ErrInvalidConfiguration
	}
	return &Adapter{
		state: &adapterState{
			runID: config.RunID,
			ref: execution.ExecutionRef{
				IsolationDomainID: config.Execution.IsolationDomainID,
				ID:                config.Execution.ID,
			},
			provider: provider,
		},
	}, nil
}

func (adapter *Adapter) Name() string {
	if adapter == nil || adapter.state == nil {
		return ""
	}
	return adapter.state.ref.ID
}

// Cleanup terminates the bound execution and then requires an exact terminal
// observation. A successful result is cached so concurrent or repeated cleanup
// cannot cause another provider mutation.
func (adapter *Adapter) Cleanup(ctx context.Context, request canaryevidence.CleanupRequest) error {
	if adapter == nil || adapter.state == nil || ctx == nil {
		return ErrInvalidConfiguration
	}
	state := adapter.state
	if request.RunID != state.runID ||
		request.ResourceKind != "sandbox" ||
		request.ResourceName != state.ref.ID {
		return ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrCleanupFailure, err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.removed {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrCleanupFailure, err)
	}
	if err := state.provider.Terminate(ctx, state.ref); err != nil {
		return cleanupError(ctx)
	}
	observation, err := state.provider.Observe(ctx, state.ref)
	if err != nil {
		return cleanupError(ctx)
	}
	if observation.IsolationDomainID != state.ref.IsolationDomainID ||
		observation.ExecutionID != state.ref.ID ||
		observation.State != "terminated" {
		return ErrCleanupUncertain
	}
	state.removed = true
	return nil
}

func cleanupError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrCleanupFailure, err)
	}
	return ErrCleanupFailure
}

func (Adapter) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var (
	_ json.Marshaler             = Adapter{}
	_ canaryevidence.CleanupFunc = (&Adapter{}).Cleanup
)
