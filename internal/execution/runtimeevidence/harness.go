package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"
)

var (
	ErrHarnessConfiguration = errors.New("invalid runtime conformance harness configuration")
	ErrHarnessRun           = errors.New("runtime conformance harness failed")
)

type HarnessStore interface {
	OpenShellProbeStore
	CodexProbeStore
}

type HarnessProvider interface {
	OpenShellProbeProvider
	CodexProbeProvider
}

type HarnessConfig struct {
	diagnosticModel string
	candidateImage  string
	RunID           string
	Provenance      Provenance
	ExecutionID     string
	Store           HarnessStore
	Provider        HarnessProvider
	Cleanup         Cleanup
}

type Harness struct {
	run *EvidenceRun
}

// NewHarness closes the live evidence chain around one already-created,
// run-bound execution. Docker topology and immutable sandbox creation remain
// launcher responsibilities outside this composition boundary.
func NewHarness(config HarnessConfig) (*Harness, error) {
	return newHarness(config, time.Now)
}

func newHarness(config HarnessConfig, now func() time.Time) (*Harness, error) {
	if now == nil || !validHarnessConfig(config) {
		return nil, ErrHarnessConfiguration
	}
	providerProbes, err := NewOpenShellProbes(OpenShellProbeConfig{
		RunID:       config.RunID,
		ExecutionID: config.ExecutionID,
		Store:       config.Store,
		Provider:    config.Provider,
		Now:         now,
	})
	if err != nil {
		return nil, ErrHarnessConfiguration
	}
	runtimeProbes, err := NewCodexProbes(CodexProbeConfig{
		diagnosticModel: config.diagnosticModel,
		RunID:           config.RunID,
		ExecutionID:     config.ExecutionID,
		Store:           config.Store,
		Provider:        config.Provider,
		Now:             now,
	})
	if err != nil {
		return nil, ErrHarnessConfiguration
	}
	scenario, err := NewConcreteScenario(ScenarioConfig{
		RunID:    config.RunID,
		Provider: providerProbes,
		Runtime:  runtimeProbes,
	})
	if err != nil {
		return nil, ErrHarnessConfiguration
	}
	cases, err := NewLiveCases(config.RunID, scenario)
	if err != nil {
		return nil, ErrHarnessConfiguration
	}
	run, err := newEvidenceRun(Config{
		diagnosticModel: config.diagnosticModel,
		candidateImage:  config.candidateImage,
		RunID:           config.RunID,
		Provenance:      config.Provenance,
		Cases:           cases,
		Cleanup:         config.Cleanup,
	}, now)
	if err != nil {
		return nil, ErrHarnessConfiguration
	}
	return &Harness{run: run}, nil
}

func (harness *Harness) Run(ctx context.Context) (Result, error) {
	if harness == nil || harness.run == nil || ctx == nil {
		return Result{}, ErrHarnessConfiguration
	}
	result, err := harness.run.Execute(ctx)
	if err != nil {
		return Result{}, errors.Join(ErrHarnessRun, err)
	}
	return result, nil
}

func validHarnessConfig(config HarnessConfig) bool {
	return validCandidateSelection(config.candidateImage, config.diagnosticModel) && runIDPattern.MatchString(config.RunID) &&
		validRunProvenance(config.Provenance, config.diagnosticModel) &&
		config.ExecutionID != "" &&
		!isNilHarnessPort(config.Store) &&
		!isNilHarnessPort(config.Provider) &&
		config.Cleanup.Sandbox != nil &&
		config.Cleanup.ProviderBinding != nil &&
		config.Cleanup.Workspace != nil
}

func isNilHarnessPort(value any) bool {
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

func (HarnessConfig) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

func (Harness) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var (
	_ json.Marshaler = HarnessConfig{}
	_ json.Marshaler = Harness{}
)
