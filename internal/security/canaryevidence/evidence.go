package canaryevidence

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/asabla/dataground/internal/security/canarycollect"
)

const (
	schemaVersion        = "dataground.dev.openshell-credential-non-exposure-evidence/v1"
	verifierName         = "dataground-openshell-canary"
	verifierVersion      = "1.0.0"
	openshellCommit      = "d556748771c41cbbd4e4dd7cd9030c798afe2b7d"
	gatewayImage         = "ghcr.io/nvidia/openshell/gateway@sha256:e21f520a0678ba3cfe749957338b5fa78c75e8e52de13e4559ccbb582f781a0b"
	supervisorImage      = "ghcr.io/nvidia/openshell/supervisor@sha256:a15222ac18c1afd0ee51b9dda785a29067c13f61a2002a29d41f691f5e817f19"
	sandboxImage         = "ghcr.io/nvidia/openshell-community/sandboxes/base@sha256:aeef1c63f00e2913ea002ccb3aaf925f338b5c5d70e63576f0d95c16a138044e"
	providerProfileSHA   = "d9c7f48d96916dcaca319e396d75e30ff6ad3bf2474f38f54ab37f37cabbca8f"
	runtimeVersion       = "0.117.0"
	gatewayEndpoint      = "http://127.0.0.1:8080"
	driver               = "docker"
	cleanupStatusRemoved = "removed"
	workspaceNamePrefix  = "dg-canary-"
	providerNamePrefix   = "dg-canary-provider-"
)

var (
	ErrInvalidConfiguration = errors.New("invalid credential evidence run configuration")
	ErrCollection           = errors.New("credential evidence collection failed")
	ErrCleanup              = errors.New("credential evidence cleanup failed")
	ErrRunIncomplete        = errors.New("credential evidence run is incomplete")
	ErrClock                = errors.New("credential evidence run clock moved backwards")
	resourceNamePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

type Resources struct {
	Gateway   string `json:"gateway"`
	Sandbox   string `json:"sandbox"`
	Provider  string `json:"provider"`
	Runtime   string `json:"runtime"`
	Workspace string `json:"workspace"`
}

type CleanupRequest struct {
	RunID        string
	ResourceKind string
	ResourceName string
}

type CleanupFunc func(context.Context, CleanupRequest) error

type Cleanup struct {
	Sandbox         CleanupFunc
	ProviderBinding CleanupFunc
	Workspace       CleanupFunc
}

type Config struct {
	RunID            string
	CanaryCommitment string
	Resources        Resources
	Sources          []canarycollect.Source
	Cleanup          Cleanup
}

// Result is an opaque final evidence record. Failed collection, uncertain
// cleanup, or clock regression prevents its JSON representation.
type Result struct {
	evidence evidence
	complete bool
}

type evidence struct {
	SchemaVersion string                   `json:"schemaVersion"`
	Profile       profile                  `json:"profile"`
	Run           run                      `json:"run"`
	Checks        canarycollect.Collection `json:"checks"`
	Cleanup       cleanup                  `json:"cleanup"`
	Result        string                   `json:"result"`
}

type profile struct {
	OpenShellCommit             string `json:"openshellCommit"`
	GatewayImage                string `json:"gatewayImage"`
	SupervisorImage             string `json:"supervisorImage"`
	SandboxImage                string `json:"sandboxImage"`
	ProviderProfileSourceSHA256 string `json:"providerProfileSourceSHA256"`
	RuntimeVersion              string `json:"runtimeVersion"`
	GatewayEndpoint             string `json:"gatewayEndpoint"`
	Driver                      string `json:"driver"`
}

type run struct {
	ID               string    `json:"id"`
	Resources        Resources `json:"resources"`
	StartedAt        string    `json:"startedAt"`
	FinishedAt       string    `json:"finishedAt"`
	Verifier         verifier  `json:"verifier"`
	CanaryCommitment string    `json:"canaryCommitment"`
}

type verifier struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type cleanup struct {
	Sandbox         cleanupReceipt `json:"sandbox"`
	ProviderBinding cleanupReceipt `json:"providerBinding"`
	Workspace       cleanupReceipt `json:"workspace"`
}

type cleanupReceipt struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Run collects every profile-owned source, executes cleanup for the exact
// run-owned resources, and seals the final evidence only after both succeed.
func Run(ctx context.Context, config Config) (Result, error) {
	return runEvidence(ctx, config, time.Now)
}

func runEvidence(ctx context.Context, config Config, now func() time.Time) (Result, error) {
	if ctx == nil || now == nil {
		return Result{}, ErrInvalidConfiguration
	}
	collectionConfig, err := validate(config)
	if err != nil {
		return Result{}, err
	}

	startedAt := now().UTC()
	collection, collectionErr := canarycollect.Collect(ctx, collectionConfig)
	cleanupRecord, cleanupErr := cleanupResources(context.WithoutCancel(ctx), config)
	finishedAt := now().UTC()

	var outcome error
	if collectionErr != nil {
		outcome = errors.Join(outcome, ErrCollection)
		if err := ctx.Err(); err != nil {
			outcome = errors.Join(outcome, err)
		}
	}
	if cleanupErr != nil {
		outcome = errors.Join(outcome, ErrCleanup)
	}
	if finishedAt.Before(startedAt) {
		outcome = errors.Join(outcome, ErrClock)
	}
	if outcome != nil {
		return Result{}, errors.Join(ErrRunIncomplete, outcome)
	}

	return Result{
		evidence: evidence{
			SchemaVersion: schemaVersion,
			Profile: profile{
				OpenShellCommit:             openshellCommit,
				GatewayImage:                gatewayImage,
				SupervisorImage:             supervisorImage,
				SandboxImage:                sandboxImage,
				ProviderProfileSourceSHA256: providerProfileSHA,
				RuntimeVersion:              runtimeVersion,
				GatewayEndpoint:             gatewayEndpoint,
				Driver:                      driver,
			},
			Run: run{
				ID:               config.RunID,
				Resources:        config.Resources,
				StartedAt:        startedAt.Format(time.RFC3339Nano),
				FinishedAt:       finishedAt.Format(time.RFC3339Nano),
				Verifier:         verifier{Name: verifierName, Version: verifierVersion},
				CanaryCommitment: config.CanaryCommitment,
			},
			Checks:  collection,
			Cleanup: cleanupRecord,
			Result:  "passed",
		},
		complete: true,
	}, nil
}

func (result Result) MarshalJSON() ([]byte, error) {
	if !result.complete {
		return nil, ErrRunIncomplete
	}
	return json.Marshal(result.evidence)
}

func validate(config Config) (canarycollect.Config, error) {
	if config.Cleanup.Sandbox == nil ||
		config.Cleanup.ProviderBinding == nil ||
		config.Cleanup.Workspace == nil ||
		!resourceNamePattern.MatchString(config.Resources.Workspace) ||
		config.Resources.Workspace != workspaceNamePrefix+config.RunID ||
		config.Resources.Provider != providerNamePrefix+config.RunID {
		return canarycollect.Config{}, ErrInvalidConfiguration
	}

	collectionConfig := canarycollect.Config{
		RunID:            config.RunID,
		CanaryCommitment: config.CanaryCommitment,
		Resources: canarycollect.ResourceNames{
			Gateway:  config.Resources.Gateway,
			Sandbox:  config.Resources.Sandbox,
			Provider: config.Resources.Provider,
			Runtime:  config.Resources.Runtime,
		},
		Sources: slices.Clone(config.Sources),
	}
	if err := canarycollect.ValidateConfig(collectionConfig); err != nil {
		return canarycollect.Config{}, ErrInvalidConfiguration
	}
	return collectionConfig, nil
}

func cleanupResources(ctx context.Context, config Config) (cleanup, error) {
	record := cleanup{
		Sandbox:         cleanupReceipt{Name: config.Resources.Sandbox},
		ProviderBinding: cleanupReceipt{Name: config.Resources.Provider},
		Workspace:       cleanupReceipt{Name: config.Resources.Workspace},
	}
	steps := []struct {
		kind   string
		name   string
		remove CleanupFunc
		status *string
	}{
		{kind: "sandbox", name: config.Resources.Sandbox, remove: config.Cleanup.Sandbox, status: &record.Sandbox.Status},
		{kind: "provider", name: config.Resources.Provider, remove: config.Cleanup.ProviderBinding, status: &record.ProviderBinding.Status},
		{kind: "workspace", name: config.Resources.Workspace, remove: config.Cleanup.Workspace, status: &record.Workspace.Status},
	}

	var outcome error
	for _, step := range steps {
		request := CleanupRequest{
			RunID:        config.RunID,
			ResourceKind: step.kind,
			ResourceName: step.name,
		}
		if err := step.remove(ctx, request); err != nil {
			outcome = errors.Join(outcome, ErrCleanup)
			continue
		}
		*step.status = cleanupStatusRemoved
	}
	return record, outcome
}
