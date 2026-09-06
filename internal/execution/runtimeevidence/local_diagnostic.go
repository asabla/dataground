package runtimeevidence

import (
	"context"
	"encoding/json"
	"regexp"
	"time"
)

const localDiagnosticTimeout = 10 * time.Minute

const LocalDiagnosticSchemaVersion = "dataground.dev.openshell-runtime-diagnostic/v1"
const CandidateDiagnosticSchemaVersion = "dataground.dev.openshell-runtime-diagnostic/v2"

var diagnosticModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// LocalDiagnosticConfig deliberately has no workflow identity or acceptance
// fields. A local run cannot produce the CI evidence used for certification.
type LocalDiagnosticConfig struct {
	RepositoryRoot      string
	WorkspaceRoot       string
	CredentialDirectory string
	OpenShellBinary     string
	DockerBinary        string
	SourceCommit        string
	Model               string
	CandidateImage      string
}

type LocalDiagnosticResult struct{ result Result }

// LaunchLocalDiagnostic exercises the same live cases and cleanup gates with
// local provenance and an explicit model. It returns no certifiable evidence.
func LaunchLocalDiagnostic(ctx context.Context, config LocalDiagnosticConfig) (LocalDiagnosticResult, error) {
	return launchLocalDiagnostic(ctx, config, defaultLauncherDependencies())
}

func launchLocalDiagnostic(ctx context.Context, config LocalDiagnosticConfig, dependencies launcherDependencies) (LocalDiagnosticResult, error) {
	if ctx == nil || !diagnosticModelPattern.MatchString(config.Model) || !validCandidateSelection(config.CandidateImage, config.Model) {
		return LocalDiagnosticResult{}, ErrLauncherConfiguration
	}
	runCtx, cancel := context.WithTimeout(ctx, localDiagnosticTimeout)
	defer cancel()
	result, err := launch(runCtx, LauncherConfig{
		RepositoryRoot: config.RepositoryRoot, WorkspaceRoot: config.WorkspaceRoot,
		CredentialDirectory: config.CredentialDirectory, OpenShellBinary: config.OpenShellBinary,
		DockerBinary: config.DockerBinary, Provenance: Provenance{SourceCommit: config.SourceCommit},
		diagnosticModel: config.Model,
		candidateImage:  config.CandidateImage,
	}, dependencies)
	if err != nil {
		return LocalDiagnosticResult{}, err
	}
	if !result.complete || result.diagnosticModel != config.Model || result.candidateImage != config.CandidateImage || result.record.Run.Provenance.WorkflowRunID != 0 || result.record.Run.Provenance.Workflow != "" || result.record.Run.Provenance.ArtifactName != "" {
		return LocalDiagnosticResult{}, ErrLauncherRun
	}
	return LocalDiagnosticResult{result: result}, nil
}

func validCandidateSelection(image, model string) bool {
	return image == "" || (commitmentPattern.MatchString(image) && diagnosticModelPattern.MatchString(model))
}

func validRunProvenance(provenance Provenance, diagnosticModel string) bool {
	if !commitPattern.MatchString(provenance.SourceCommit) {
		return false
	}
	if diagnosticModel != "" {
		return diagnosticModelPattern.MatchString(diagnosticModel) && provenance.WorkflowRunID == 0
	}
	return provenance.WorkflowRunID > 0 && provenance.WorkflowRunID <= maxSafeInteger
}

func (result LocalDiagnosticResult) MarshalJSON() ([]byte, error) {
	value := result.result
	if !value.complete || !diagnosticModelPattern.MatchString(value.diagnosticModel) || !validCandidateSelection(value.candidateImage, value.diagnosticModel) || value.record.Run.Provenance.WorkflowRunID != 0 || value.record.Run.Provenance.Workflow != "" || value.record.Run.Provenance.ArtifactName != "" {
		return nil, ErrRunIncomplete
	}
	schema, credentialCheck := LocalDiagnosticSchemaVersion, ""
	expectedProfile := currentProfile()
	if value.candidateImage != "" {
		schema, credentialCheck = CandidateDiagnosticSchemaVersion, "passed"
		expectedProfile.SandboxImage = value.candidateImage
		expectedProfile.CredentialEvidenceSHA256 = ""
	}
	if value.record.Profile != expectedProfile {
		return nil, ErrRunIncomplete
	}
	// Use a closed local shape rather than serializing and editing CI evidence.
	return json.Marshal(localDiagnosticRecord{
		SchemaVersion: schema, Profile: value.record.Profile, CandidateCredentialCheck: credentialCheck,
		Run: localDiagnosticRun{ID: value.record.Run.ID, Resources: value.record.Run.Resources,
			StartedAt: value.record.Run.StartedAt, FinishedAt: value.record.Run.FinishedAt,
			Origin: "local", SourceCommit: value.record.Run.Provenance.SourceCommit, Model: value.diagnosticModel},
		Checks: value.record.Checks, Cleanup: value.record.Cleanup, Result: value.record.Result,
	})
}

type localDiagnosticRecord struct {
	CandidateCredentialCheck string             `json:"candidateCredentialCheck,omitempty"`
	SchemaVersion            string             `json:"schemaVersion"`
	CertificationEligible    bool               `json:"certificationEligible"`
	Profile                  profile            `json:"profile"`
	Run                      localDiagnosticRun `json:"run"`
	Checks                   []check            `json:"checks"`
	Cleanup                  cleanup            `json:"cleanup"`
	Result                   string             `json:"result"`
}
type localDiagnosticRun struct {
	ID           string    `json:"id"`
	Resources    Resources `json:"resources"`
	StartedAt    string    `json:"startedAt"`
	FinishedAt   string    `json:"finishedAt"`
	Origin       string    `json:"origin"`
	SourceCommit string    `json:"sourceCommit"`
	Model        string    `json:"model"`
}

func (LocalDiagnosticConfig) MarshalJSON() ([]byte, error) { return nil, ErrSerialization }

// LocalDiagnosticError exposes only the closed launcher phase, never upstream
// output or credential-source details.
type LocalDiagnosticError struct{ stage string }

func (failure *LocalDiagnosticError) Error() string {
	return "local runtime diagnostic failed at " + failure.stage
}
func (failure *LocalDiagnosticError) Unwrap() error { return ErrLauncherRun }
