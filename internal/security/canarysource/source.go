package canarysource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"

	"github.com/asabla/dataground/internal/security/canarycollect"
)

var (
	ErrInvalidConfiguration = errors.New("invalid credential source adapter configuration")
	ErrAlreadyStarted       = errors.New("credential source acquisition already started")
	ErrSerialization        = errors.New("credential source adapter cannot be serialized")
)

type ResourceNames struct {
	Gateway  string
	Sandbox  string
	Provider string
	Runtime  string
}

type Request struct {
	RunID        string
	Surface      string
	ResourceName string
}

// OpenShellSources owns sandbox-visible acquisition through an exact
// run-bound OpenShell backend. Implementations must not retain source bytes.
type OpenShellSources interface {
	OpenSandboxProcess(context.Context, Request) (io.ReadCloser, error)
	OpenSandboxEnvironment(context.Context, Request) (io.ReadCloser, error)
	OpenSandboxFilesystem(context.Context, Request) (io.ReadCloser, error)
	OpenSandboxLogs(context.Context, Request) (io.ReadCloser, error)
}

// DockerSources owns host-visible acquisition from the dedicated evidence
// topology. Implementations must bind portable names to exact live containers.
type DockerSources interface {
	OpenProviderArguments(context.Context, Request) (io.ReadCloser, error)
	OpenGatewayLogs(context.Context, Request) (io.ReadCloser, error)
}

// RuntimeSources owns the exact invocation error stream. Implementations must
// bind the portable runtime name to one native invocation before construction.
type RuntimeSources interface {
	OpenRuntimeErrors(context.Context, Request) (io.ReadCloser, error)
}

type Config struct {
	RunID            string
	CanaryCommitment string
	Resources        ResourceNames
	OpenShell        OpenShellSources
	Docker           DockerSources
	Runtime          RuntimeSources
}

// Adapter binds the complete seven-source plan to one evidence run. It is
// single-use because live streams cannot be safely replayed after acquisition
// begins, even when the first attempt fails before producing a report.
type Adapter struct {
	state *state
}

type state struct {
	mu      sync.Mutex
	config  Config
	started bool
}

func New(config Config) (*Adapter, error) {
	if isNil(config.OpenShell) || isNil(config.Docker) || isNil(config.Runtime) {
		return nil, ErrInvalidConfiguration
	}
	adapter := &Adapter{state: &state{config: config}}
	if err := canarycollect.ValidateConfig(adapter.collectorConfig()); err != nil {
		return nil, ErrInvalidConfiguration
	}
	return adapter, nil
}

func (adapter *Adapter) ValidateBinding(
	runID string,
	canaryCommitment string,
	resources ResourceNames,
) error {
	if adapter == nil ||
		adapter.state == nil ||
		adapter.state.config.RunID != runID ||
		adapter.state.config.CanaryCommitment != canaryCommitment ||
		adapter.state.config.Resources != resources {
		return ErrInvalidConfiguration
	}
	return nil
}

// Collect acquires the seven live sources exactly once in the collector's
// canonical order and passes each stream directly into the scanner boundary.
func (adapter *Adapter) Collect(ctx context.Context) (canarycollect.Collection, error) {
	if adapter == nil || adapter.state == nil || ctx == nil {
		return canarycollect.Collection{}, ErrInvalidConfiguration
	}

	adapter.state.mu.Lock()
	if adapter.state.started {
		adapter.state.mu.Unlock()
		return canarycollect.Collection{}, ErrAlreadyStarted
	}
	adapter.state.started = true
	adapter.state.mu.Unlock()

	return canarycollect.Collect(ctx, adapter.collectorConfig())
}

func (Adapter) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

func (adapter *Adapter) collectorConfig() canarycollect.Config {
	return canarycollect.Config{
		RunID:            adapter.state.config.RunID,
		CanaryCommitment: adapter.state.config.CanaryCommitment,
		Resources: canarycollect.ResourceNames{
			Gateway:  adapter.state.config.Resources.Gateway,
			Sandbox:  adapter.state.config.Resources.Sandbox,
			Provider: adapter.state.config.Resources.Provider,
			Runtime:  adapter.state.config.Resources.Runtime,
		},
		Sources: []canarycollect.Source{
			adapter.source("sandbox-process", adapter.state.config.Resources.Sandbox, adapter.state.config.OpenShell.OpenSandboxProcess),
			adapter.source("sandbox-environment", adapter.state.config.Resources.Sandbox, adapter.state.config.OpenShell.OpenSandboxEnvironment),
			adapter.source("sandbox-filesystem", adapter.state.config.Resources.Sandbox, adapter.state.config.OpenShell.OpenSandboxFilesystem),
			adapter.source("provider-arguments", adapter.state.config.Resources.Provider, adapter.state.config.Docker.OpenProviderArguments),
			adapter.source("gateway-logs", adapter.state.config.Resources.Gateway, adapter.state.config.Docker.OpenGatewayLogs),
			adapter.source("sandbox-logs", adapter.state.config.Resources.Sandbox, adapter.state.config.OpenShell.OpenSandboxLogs),
			adapter.source("runtime-errors", adapter.state.config.Resources.Runtime, adapter.state.config.Runtime.OpenRuntimeErrors),
		},
	}
}

func (adapter *Adapter) source(
	surface string,
	resourceName string,
	open func(context.Context, Request) (io.ReadCloser, error),
) canarycollect.Source {
	return canarycollect.Source{
		Surface: surface,
		Acquire: func(
			ctx context.Context,
			request canarycollect.SourceRequest,
		) (io.ReadCloser, error) {
			if request.RunID != adapter.state.config.RunID ||
				request.Surface != surface ||
				request.ResourceName != resourceName {
				return nil, ErrInvalidConfiguration
			}
			source, err := open(ctx, Request{
				RunID:        request.RunID,
				Surface:      request.Surface,
				ResourceName: request.ResourceName,
			})
			if err != nil {
				if !isNil(source) {
					_ = source.Close()
				}
				return nil, canarycollect.ErrAcquisition
			}
			if isNil(source) {
				return nil, canarycollect.ErrAcquisition
			}
			return source, nil
		},
	}
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ json.Marshaler = (*Adapter)(nil)
