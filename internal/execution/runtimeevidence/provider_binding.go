package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/asabla/dataground/internal/execution"
)

const (
	runtimeProviderCredentialMaxBytes = 64 << 10
	runtimeProviderRecoveryTimeout    = 30 * time.Second
	runtimeProviderCleanupTimeout     = time.Minute
)

var (
	ErrRuntimeProviderConfiguration = errors.New("invalid runtime conformance provider configuration")
	ErrRuntimeProviderProvision     = errors.New("runtime conformance provider provisioning failed")
	ErrRuntimeProviderOrder         = errors.New("runtime conformance provider order is invalid")
	ErrRuntimeProviderCleanup       = errors.New("runtime conformance provider cleanup failed")
)

type RuntimeProviderPort interface {
	execution.RuntimeConformanceProviderProvisioner
	execution.ProviderBindingManager
}

type RuntimeProviderConfig struct {
	RunID       string
	Credentials execution.RuntimeConformanceCredentials
	Provider    RuntimeProviderPort
}

// RuntimeProvider owns one real, run-derived Codex provider binding. It keeps
// credential values only until the first provisioning attempt and retains only
// exact immutable identity needed for cleanup afterward.
type RuntimeProvider struct {
	state *runtimeProviderState
}

type runtimeProviderState struct {
	mu              sync.Mutex
	runID           string
	name            string
	isolationID     string
	gatewayID       string
	credentials     execution.RuntimeConformanceCredentials
	provider        RuntimeProviderPort
	ref             execution.ProviderBindingRef
	started         bool
	running         bool
	created         bool
	mutationStarted bool
	cleaning        bool
	removed         bool
	failed          bool
}

func NewRuntimeProvider(config RuntimeProviderConfig) (*RuntimeProvider, error) {
	if !runIDPattern.MatchString(config.RunID) ||
		isNilHarnessPort(config.Provider) ||
		!validRuntimeProviderCredentials(config.Credentials) {
		return nil, ErrRuntimeProviderConfiguration
	}
	resources := namesForRun(config.RunID)
	return &RuntimeProvider{state: &runtimeProviderState{
		runID:       config.RunID,
		name:        resources.Provider,
		isolationID: runtimeIsolationDomain(config.RunID),
		gatewayID:   resources.Gateway,
		credentials: cloneRuntimeProviderCredentials(config.Credentials),
		provider:    config.Provider,
	}}, nil
}

func (provider *RuntimeProvider) Name() string {
	if provider == nil || provider.state == nil {
		return ""
	}
	return provider.state.name
}

func (provider *RuntimeProvider) Provision(ctx context.Context) error {
	if provider == nil || provider.state == nil || ctx == nil {
		return ErrRuntimeProviderConfiguration
	}
	state := provider.state
	state.mu.Lock()
	if state.started || state.failed || state.removed {
		state.failed = true
		clearRuntimeProviderCredentials(&state.credentials)
		state.mu.Unlock()
		return errors.Join(ErrRuntimeProviderProvision, ErrRuntimeProviderOrder)
	}
	state.started = true
	state.running = true
	credentials := state.credentials
	state.credentials = execution.RuntimeConformanceCredentials{}
	state.mu.Unlock()
	defer clearRuntimeProviderCredentials(&credentials)

	if err := ctx.Err(); err != nil {
		state.failProvision()
		return errors.Join(ErrRuntimeProviderProvision, err)
	}
	ref := execution.RuntimeConformanceProviderRef{
		IsolationDomainID: state.isolationID,
		GatewayID:         state.gatewayID,
		Name:              state.name,
	}
	preStartedAt := time.Now().UTC()
	before, err := state.provider.ObserveRuntimeConformanceProvider(ctx, ref)
	preFinishedAt := time.Now().UTC()
	if err != nil ||
		!validRuntimeProviderScope(before, ref, preStartedAt, preFinishedAt) ||
		before.Exists ||
		state.invalidAfter(ctx) {
		state.failProvision()
		return state.provisionError(ctx)
	}

	state.mu.Lock()
	state.mutationStarted = true
	state.mu.Unlock()
	createStartedAt := time.Now().UTC()
	binding, createErr := state.provider.CreateRuntimeConformanceProvider(
		ctx,
		execution.RuntimeConformanceProviderRequest{
			IsolationDomainID: state.isolationID,
			GatewayID:         state.gatewayID,
			Name:              state.name,
			Credentials:       credentials,
		},
	)
	recoveryCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		runtimeProviderRecoveryTimeout,
	)
	defer cancel()
	after, observeErr := state.provider.ObserveRuntimeConformanceProvider(recoveryCtx, ref)
	createFinishedAt := time.Now().UTC()
	if observeErr != nil ||
		!validRuntimeProviderScope(after, ref, createStartedAt, createFinishedAt) ||
		!after.Exists ||
		!validRuntimeProviderBindingObservation(after) {
		state.failProvision()
		return state.provisionError(ctx)
	}

	state.mu.Lock()
	state.ref = execution.ProviderBindingRef{
		IsolationDomainID: after.IsolationDomainID,
		GatewayID:         after.GatewayID,
		ID:                after.ID,
		Name:              after.Name,
		ResourceVersion:   after.ResourceVersion,
	}
	state.created = true
	state.running = false
	poisoned := state.failed
	state.mu.Unlock()
	if !emptyRuntimeProviderBinding(binding) && !runtimeProviderBindingMatches(binding, after) {
		state.failProvision()
		return ErrRuntimeProviderProvision
	}
	if poisoned || ctx.Err() != nil {
		state.failProvision()
		return errors.Join(ErrRuntimeProviderProvision, ErrRuntimeProviderOrder, ctx.Err())
	}
	if createErr != nil {
		// Exact absence before mutation and exact metadata afterward make this a
		// safe lost-acknowledgement recovery; upstream details remain sealed.
	}
	return nil
}

func (provider *RuntimeProvider) Cleanup(ctx context.Context, request CleanupRequest) error {
	if provider == nil || provider.state == nil || ctx == nil {
		return ErrRuntimeProviderConfiguration
	}
	state := provider.state
	state.mu.Lock()
	if request.RunID != state.runID ||
		request.ResourceKind != "provider" ||
		request.ResourceName != state.name ||
		!state.started ||
		(!state.created && state.mutationStarted) ||
		state.running ||
		state.cleaning {
		if state.running {
			state.failed = true
		}
		state.mu.Unlock()
		return ErrRuntimeProviderCleanup
	}
	if state.removed || !state.created {
		state.mu.Unlock()
		return nil
	}
	state.cleaning = true
	ref := state.ref
	state.mu.Unlock()

	cleanupCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		runtimeProviderCleanupTimeout,
	)
	defer cancel()
	err := state.cleanupBinding(cleanupCtx, ref)
	state.mu.Lock()
	state.cleaning = false
	if err != nil {
		state.failed = true
		state.mu.Unlock()
		return ErrRuntimeProviderCleanup
	}
	state.removed = true
	state.mu.Unlock()
	return nil
}

func (state *runtimeProviderState) cleanupBinding(
	ctx context.Context,
	ref execution.ProviderBindingRef,
) error {
	startedAt := time.Now().UTC()
	before, err := state.provider.ObserveProviderBinding(ctx, ref)
	finishedAt := time.Now().UTC()
	if err != nil || !validExactRuntimeProviderObservation(before, ref, startedAt, finishedAt) {
		return ErrRuntimeProviderCleanup
	}
	if !before.Exists {
		return nil
	}

	deleteErr := state.provider.DeleteProviderBinding(ctx, ref)
	observeStartedAt := time.Now().UTC()
	after, observeErr := state.provider.ObserveProviderBinding(ctx, ref)
	observeFinishedAt := time.Now().UTC()
	if observeErr != nil ||
		!validRuntimeProviderScope(
			after,
			execution.RuntimeConformanceProviderRef{
				IsolationDomainID: ref.IsolationDomainID,
				GatewayID:         ref.GatewayID,
				Name:              ref.Name,
			},
			observeStartedAt,
			observeFinishedAt,
		) ||
		after.Exists {
		return ErrRuntimeProviderCleanup
	}
	if deleteErr != nil {
		// Exact timestamped absence is authoritative after a lost delete
		// acknowledgement.
	}
	return nil
}

func validRuntimeProviderCredentials(credentials execution.RuntimeConformanceCredentials) bool {
	values := [...][]byte{
		credentials.AccessToken,
		credentials.RefreshToken,
		credentials.AccountID,
		credentials.IDToken,
	}
	for _, value := range values {
		if len(value) == 0 || len(value) > runtimeProviderCredentialMaxBytes {
			return false
		}
	}
	return true
}

func cloneRuntimeProviderCredentials(
	credentials execution.RuntimeConformanceCredentials,
) execution.RuntimeConformanceCredentials {
	return execution.RuntimeConformanceCredentials{
		AccessToken:  slices.Clone(credentials.AccessToken),
		RefreshToken: slices.Clone(credentials.RefreshToken),
		AccountID:    slices.Clone(credentials.AccountID),
		IDToken:      slices.Clone(credentials.IDToken),
	}
}

func clearRuntimeProviderCredentials(credentials *execution.RuntimeConformanceCredentials) {
	if credentials == nil {
		return
	}
	clear(credentials.AccessToken)
	clear(credentials.RefreshToken)
	clear(credentials.AccountID)
	clear(credentials.IDToken)
	*credentials = execution.RuntimeConformanceCredentials{}
}

func validRuntimeProviderScope(
	observation execution.ProviderBindingObservation,
	ref execution.RuntimeConformanceProviderRef,
	startedAt time.Time,
	finishedAt time.Time,
) bool {
	return observation.IsolationDomainID == ref.IsolationDomainID &&
		observation.GatewayID == ref.GatewayID &&
		observation.Name == ref.Name &&
		!observation.ObservedAt.IsZero() &&
		!observation.ObservedAt.Before(startedAt) &&
		!observation.ObservedAt.After(finishedAt)
}

func validRuntimeProviderBindingObservation(observation execution.ProviderBindingObservation) bool {
	return observation.ID != "" && observation.ResourceVersion > 0
}

func validExactRuntimeProviderObservation(
	observation execution.ProviderBindingObservation,
	ref execution.ProviderBindingRef,
	startedAt time.Time,
	finishedAt time.Time,
) bool {
	if !validRuntimeProviderScope(
		observation,
		execution.RuntimeConformanceProviderRef{
			IsolationDomainID: ref.IsolationDomainID,
			GatewayID:         ref.GatewayID,
			Name:              ref.Name,
		},
		startedAt,
		finishedAt,
	) {
		return false
	}
	return !observation.Exists ||
		(observation.ID == ref.ID && observation.ResourceVersion == ref.ResourceVersion)
}

func emptyRuntimeProviderBinding(binding execution.ProviderBinding) bool {
	return binding == (execution.ProviderBinding{})
}

func runtimeProviderBindingMatches(
	binding execution.ProviderBinding,
	observation execution.ProviderBindingObservation,
) bool {
	return binding.IsolationDomainID == observation.IsolationDomainID &&
		binding.GatewayID == observation.GatewayID &&
		binding.ID == observation.ID &&
		binding.Name == observation.Name &&
		binding.ResourceVersion == observation.ResourceVersion
}

func (state *runtimeProviderState) invalidAfter(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return true
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.failed
}

func (state *runtimeProviderState) failProvision() {
	state.mu.Lock()
	state.failed = true
	state.running = false
	clearRuntimeProviderCredentials(&state.credentials)
	state.mu.Unlock()
}

func (state *runtimeProviderState) provisionError(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return errors.Join(ErrRuntimeProviderProvision, err)
		}
	}
	return ErrRuntimeProviderProvision
}

func (RuntimeProviderConfig) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

func (RuntimeProvider) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var (
	_ json.Marshaler = RuntimeProviderConfig{}
	_ json.Marshaler = RuntimeProvider{}
	_ CleanupFunc    = (&RuntimeProvider{}).Cleanup
)
