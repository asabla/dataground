package canaryevidence

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/asabla/dataground/internal/security/canarycollect"
	"github.com/asabla/dataground/internal/security/canaryprofile"
	"github.com/asabla/dataground/internal/security/canarysource"
)

const (
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
	Sources          *canarysource.Adapter
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
	ComposeSHA256               string `json:"composeSHA256"`
	GatewayConfigSHA256         string `json:"gatewayConfigSHA256"`
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
	if err := validate(config); err != nil {
		return Result{}, err
	}

	startedAt := now().UTC()
	collection, collectionErr := config.Sources.Collect(ctx)
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

	profileIdentity := canaryprofile.Current()
	return Result{
		evidence: evidence{
			SchemaVersion: canaryprofile.SchemaVersion,
			Profile: profile{
				OpenShellCommit:             profileIdentity.OpenShellCommit,
				GatewayImage:                profileIdentity.GatewayImage,
				SupervisorImage:             profileIdentity.SupervisorImage,
				SandboxImage:                profileIdentity.SandboxImage,
				ProviderProfileSourceSHA256: profileIdentity.ProviderProfileSHA256,
				RuntimeVersion:              profileIdentity.RuntimeVersion,
				GatewayEndpoint:             profileIdentity.GatewayEndpoint,
				Driver:                      profileIdentity.Driver,
				ComposeSHA256:               profileIdentity.ComposeSHA256,
				GatewayConfigSHA256:         profileIdentity.GatewayConfigSHA256,
			},
			Run: run{
				ID:               config.RunID,
				Resources:        config.Resources,
				StartedAt:        startedAt.Format(time.RFC3339Nano),
				FinishedAt:       finishedAt.Format(time.RFC3339Nano),
				Verifier:         verifier{Name: canaryprofile.VerifierName, Version: canaryprofile.VerifierVersion},
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

func validate(config Config) error {
	if config.Cleanup.Sandbox == nil ||
		config.Cleanup.ProviderBinding == nil ||
		config.Cleanup.Workspace == nil ||
		!resourceNamePattern.MatchString(config.Resources.Workspace) ||
		config.Resources.Workspace != workspaceNamePrefix+config.RunID ||
		config.Resources.Provider != providerNamePrefix+config.RunID {
		return ErrInvalidConfiguration
	}

	if err := config.Sources.ValidateBinding(
		config.RunID,
		config.CanaryCommitment,
		canarysource.ResourceNames{
			Gateway:  config.Resources.Gateway,
			Sandbox:  config.Resources.Sandbox,
			Provider: config.Resources.Provider,
			Runtime:  config.Resources.Runtime,
		},
	); err != nil {
		return ErrInvalidConfiguration
	}
	return nil
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
