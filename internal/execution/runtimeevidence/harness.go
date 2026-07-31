package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
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
	RunID       string
	Provenance  Provenance
	ExecutionID string
	Store       HarnessStore
	Provider    HarnessProvider
	Cleanup     Cleanup
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
		RunID:       config.RunID,
		ExecutionID: config.ExecutionID,
		Store:       config.Store,
		Provider:    config.Provider,
		Now:         now,
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
		RunID:      config.RunID,
		Provenance: config.Provenance,
		Cases:      cases,
		Cleanup:    config.Cleanup,
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
	return runIDPattern.MatchString(config.RunID) &&
		commitPattern.MatchString(config.Provenance.SourceCommit) &&
		config.Provenance.WorkflowRunID > 0 &&
		config.Provenance.WorkflowRunID <= maxSafeInteger &&
		config.ExecutionID != "" &&
		config.Store != nil &&
		config.Provider != nil &&
		config.Cleanup.Sandbox != nil &&
		config.Cleanup.ProviderBinding != nil &&
		config.Cleanup.Workspace != nil
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
