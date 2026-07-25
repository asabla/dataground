package canaryprovider

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"sync"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/security/canaryevidence"
)

const providerNamePrefix = "dg-canary-provider-"

var (
	ErrInvalidConfiguration = errors.New("invalid credential provider cleanup configuration")
	ErrCleanupFailure       = errors.New("credential provider cleanup failed")
	ErrCleanupUncertain     = errors.New("credential provider cleanup is uncertain")
	ErrSerialization        = errors.New("credential provider cleanup cannot be serialized")
	runIDPattern            = regexp.MustCompile("^[a-f0-9]{32}$")
)

type Manager interface {
	DeleteProviderBinding(context.Context, execution.ProviderBindingRef) error
	ObserveProviderBinding(
		context.Context,
		execution.ProviderBindingRef,
	) (execution.ProviderBindingObservation, error)
}

type Config struct {
	RunID   string
	Binding execution.ProviderBinding
}

// Adapter owns cleanup of one temporary, run-derived provider binding. Native
// provider and gateway identities never enter the evidence record.
type Adapter struct {
	state *adapterState
}

type adapterState struct {
	mu      sync.Mutex
	runID   string
	ref     execution.ProviderBindingRef
	manager Manager
	removed bool
}

// New binds cleanup to the exact provider identity returned by the pinned
// OpenShell gateway. The provider name is derived from the evidence run.
func New(config Config, manager Manager) (*Adapter, error) {
	binding := config.Binding
	if isNilManager(manager) ||
		!runIDPattern.MatchString(config.RunID) ||
		binding.IsolationDomainID == "" ||
		binding.GatewayID == "" ||
		binding.ID == "" ||
		binding.Name != providerNamePrefix+config.RunID ||
		binding.ResourceVersion == 0 {
		return nil, ErrInvalidConfiguration
	}
	return &Adapter{state: &adapterState{
		runID: config.RunID,
		ref: execution.ProviderBindingRef{
			IsolationDomainID: binding.IsolationDomainID,
			GatewayID:         binding.GatewayID,
			ID:                binding.ID,
			Name:              binding.Name,
			ResourceVersion:   binding.ResourceVersion,
		},
		manager: manager,
	}}, nil
}

func (adapter *Adapter) Name() string {
	if adapter == nil || adapter.state == nil {
		return ""
	}
	return adapter.state.ref.Name
}

// Cleanup observes the exact binding before deletion and requires timestamped
// absence afterward. A lost delete acknowledgement is safe only when absence is
// observed; a replacement under the same name is never deleted.
func (adapter *Adapter) Cleanup(
	ctx context.Context,
	request canaryevidence.CleanupRequest,
) error {
	if adapter == nil || adapter.state == nil || ctx == nil {
		return ErrInvalidConfiguration
	}
	state := adapter.state
	if request.RunID != state.runID ||
		request.ResourceKind != "provider" ||
		request.ResourceName != state.ref.Name {
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

	before, err := state.manager.ObserveProviderBinding(ctx, state.ref)
	if err != nil {
		return cleanupError(ctx)
	}
	if !validObservationScope(before, state.ref) {
		return ErrCleanupUncertain
	}
	if !before.Exists {
		state.removed = true
		return nil
	}
	if !matchesRef(before, state.ref) {
		return ErrCleanupUncertain
	}

	deleteErr := state.manager.DeleteProviderBinding(ctx, state.ref)
	after, observeErr := state.manager.ObserveProviderBinding(ctx, state.ref)
	if observeErr != nil {
		return cleanupError(ctx)
	}
	if !validObservationScope(after, state.ref) {
		return ErrCleanupUncertain
	}
	if after.Exists {
		if !matchesRef(after, state.ref) {
			return ErrCleanupUncertain
		}
		if deleteErr != nil {
			return cleanupError(ctx)
		}
		return ErrCleanupUncertain
	}
	state.removed = true
	return nil
}

func validObservationScope(
	observation execution.ProviderBindingObservation,
	ref execution.ProviderBindingRef,
) bool {
	return observation.IsolationDomainID == ref.IsolationDomainID &&
		observation.GatewayID == ref.GatewayID &&
		observation.Name == ref.Name &&
		!observation.ObservedAt.IsZero()
}

func matchesRef(
	observation execution.ProviderBindingObservation,
	ref execution.ProviderBindingRef,
) bool {
	return observation.Exists &&
		validObservationScope(observation, ref) &&
		observation.ID == ref.ID &&
		observation.ResourceVersion == ref.ResourceVersion
}

func cleanupError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrCleanupFailure, err)
	}
	return ErrCleanupFailure
}

func isNilManager(manager Manager) bool {
	if manager == nil {
		return true
	}
	value := reflect.ValueOf(manager)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (Adapter) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var (
	_ json.Marshaler             = Adapter{}
	_ canaryevidence.CleanupFunc = (&Adapter{}).Cleanup
)
