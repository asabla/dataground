package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asabla/dataground/internal/execution/runtimeevidence"
)

// The signature and published-image proofs are real. Gateway observation is a
// fixture; this test cannot activate a worker or grant operational acceptance.
func TestOwnedWorkspaceAcceptanceActualEvidence(t *testing.T) {
	directory := os.Getenv("DATAGROUND_OWNED_RUNTIME_ACCEPTANCE_TEST_DIRECTORY")
	if directory == "" {
		t.Skip("explicit owned acceptance test fixture is not configured")
	}
	data, err := os.ReadFile(filepath.Join(directory, "fixture.json"))
	if err != nil {
		t.Fatal("read test fixture metadata")
	}
	var fixture struct {
		Contract               string `json:"contract"`
		TestTrust              bool   `json:"testTrust"`
		Profile                string `json:"profile"`
		EnvelopeSHA256         string `json:"envelopeSHA256"`
		TrustSHA256            string `json:"trustSHA256"`
		SourceRevision         string `json:"sourceRevision"`
		Image                  string `json:"image"`
		Model                  string `json:"model"`
		SupervisorImage        string `json:"supervisorImage"`
		SupervisorLocalImageID string `json:"supervisorLocalImageId"`
		GatewayConfigSHA256    string `json:"gatewayConfigSHA256"`
		Scope                  struct {
			IsolationDomainID string `json:"isolationDomainId"`
			ServiceID         string `json:"serviceId"`
			RevisionID        string `json:"revisionId"`
		} `json:"scope"`
	}
	if json.Unmarshal(data, &fixture) != nil || fixture.Contract != "dataground.test.local-acceptance-fixture/v2" || !fixture.TestTrust || fixture.Profile != ownedWorkspaceRuntimeProfile {
		t.Fatal("owned ephemeral test fixture required")
	}
	values := validStrictAcceptanceEnvironment()
	values["DATAGROUND_LOCAL_RUNTIME_MINIMUM_GENERATION"] = "1"
	for key, value := range map[string]string{
		"DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE":             ownedWorkspaceRuntimeProfile,
		"DATAGROUND_DEVELOPMENT_ISOLATION_DOMAIN_ID":         fixture.Scope.IsolationDomainID,
		"DATAGROUND_LOCAL_RUNTIME_SERVICE_ID":                fixture.Scope.ServiceID,
		"DATAGROUND_LOCAL_RUNTIME_REVISION_ID":               fixture.Scope.RevisionID,
		"DATAGROUND_LOCAL_RUNTIME_ENVELOPE":                  filepath.Join(directory, "envelope.json"),
		"DATAGROUND_LOCAL_RUNTIME_TRUST":                     filepath.Join(directory, "trust.json"),
		"DATAGROUND_LOCAL_RUNTIME_EVIDENCE_DIRECTORY":        directory,
		"DATAGROUND_LOCAL_RUNTIME_ENVELOPE_SHA256":           fixture.EnvelopeSHA256,
		"DATAGROUND_LOCAL_RUNTIME_TRUST_SHA256":              fixture.TrustSHA256,
		"DATAGROUND_LOCAL_RUNTIME_SOURCE_REVISION":           fixture.SourceRevision,
		"DATAGROUND_LOCAL_RUNTIME_IMAGE":                     fixture.Image,
		"DATAGROUND_LOCAL_RUNTIME_MODEL":                     fixture.Model,
		"DATAGROUND_LOCAL_RUNTIME_SUPERVISOR_IMAGE":          fixture.SupervisorImage,
		"DATAGROUND_LOCAL_RUNTIME_SUPERVISOR_LOCAL_IMAGE_ID": fixture.SupervisorLocalImageID,
		"DATAGROUND_LOCAL_RUNTIME_GATEWAY_CONFIG_SHA256":     fixture.GatewayConfigSHA256,
	} {
		values[key] = value
	}
	for key, binary := range map[string]string{"DATAGROUND_LOCAL_RUNTIME_NODE_BINARY": "node", "DATAGROUND_LOCAL_RUNTIME_GITHUB_BINARY": "gh"} {
		path, err := exec.LookPath(binary)
		if err != nil {
			t.Fatal("missing verification binary")
		}
		values[key] = path
	}
	config, err := loadLocalRuntimeAcceptanceConfig(mapEnvironment(values))
	if err != nil {
		t.Fatal("test configuration", err)
	}
	deployment := &fakeRuntimeDeployment{}
	checker := &localRuntimeAcceptanceChecker{config: *config, run: func(ctx context.Context, binary string, args, env []string) ([]byte, error) {
		args[0] = filepath.Join("../..", args[0])
		return executeLocalAcceptance(ctx, binary, args, env)
	}, observe: func(runtimeevidence.ObservedDockerTopologyConfig) (runtimeDeploymentObservation, error) {
		return deployment, nil
	}}
	defer checker.Close()
	if err := checker.Check(context.Background()); err != nil || deployment.checks.Load() != 1 {
		t.Fatal("actual v4 receipt did not reach deployment check", err)
	}
	checker.config.minimumGeneration = 2
	if err := checker.Check(context.Background()); err == nil || deployment.checks.Load() != 1 {
		t.Fatal("stale test generation accepted")
	}
	checker.config.minimumGeneration = 1
	checker.config.workspaceExport = false
	if err := checker.Check(context.Background()); err == nil || deployment.checks.Load() != 1 {
		t.Fatal("owned receipt accepted by source-only consumer")
	}
}
