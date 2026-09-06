package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/reconcile"
)

func validLocalAcceptanceEnvironment() map[string]string {
	values := validGovernedEnvironment()
	for name, value := range map[string]string{
		"DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE":      localRuntimeProfile,
		"DATAGROUND_LOCAL_RUNTIME_SERVICE_ID":         "svc_0123456789abcdefghij",
		"DATAGROUND_LOCAL_RUNTIME_REVISION_ID":        "rev_0123456789abcdefghij",
		"DATAGROUND_LOCAL_RUNTIME_ENVELOPE":           "/private/tmp/acceptance/envelope.json",
		"DATAGROUND_LOCAL_RUNTIME_TRUST":              "/private/tmp/acceptance/trust.json",
		"DATAGROUND_LOCAL_RUNTIME_EVIDENCE_DIRECTORY": "/private/tmp/acceptance/evidence",
		"DATAGROUND_LOCAL_RUNTIME_ENVELOPE_SHA256":    strings.Repeat("a", 64),
		"DATAGROUND_LOCAL_RUNTIME_TRUST_SHA256":       strings.Repeat("b", 64),
		"DATAGROUND_LOCAL_RUNTIME_SOURCE_REVISION":    strings.Repeat("c", 40),
		"DATAGROUND_LOCAL_RUNTIME_MINIMUM_GENERATION": "3",
		"DATAGROUND_LOCAL_RUNTIME_IMAGE":              "ghcr.io/asabla/dataground-codex-candidate@sha256:" + strings.Repeat("d", 64),
		"DATAGROUND_LOCAL_RUNTIME_MODEL":              "gpt-6-astra",
		"DATAGROUND_LOCAL_RUNTIME_NODE_BINARY":        "/usr/bin/node",
		"DATAGROUND_LOCAL_RUNTIME_GITHUB_BINARY":      "/usr/bin/gh",
	} {
		values[name] = value
	}
	return values
}

func localAcceptanceConfig(t *testing.T) localRuntimeAcceptanceConfig {
	t.Helper()
	config, err := loadLocalRuntimeAcceptanceConfig(mapEnvironment(validLocalAcceptanceEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	return *config
}

func acceptanceReceipt(config localRuntimeAcceptanceConfig) map[string]any {
	return map[string]any{
		"acceptanceId": "rtlocal_0123456789abcdefghij", "generation": config.minimumGeneration,
		"scope":   map[string]any{"isolationDomainId": config.target.isolationDomainID, "serviceId": config.target.serviceID, "revisionId": config.target.revisionID},
		"profile": localRuntimeProfile, "image": config.image, "model": config.model,
		"expiresAt": time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), "certificationEligible": false, "deploymentScope": "loopback-development-only",
	}
}

func TestLocalAcceptanceRequiresExplicitCompleteDevelopmentProfile(t *testing.T) {
	values := validLocalAcceptanceEnvironment()
	config, err := loadWorkerConfig(mapEnvironment(values))
	if err != nil || config.localAcceptance == nil || config.runtimeTarget() != config.localAcceptance.target || config.certification.target.valid() {
		t.Fatalf("local acceptance selection failed: %v", err)
	}
	store, ok := workerReconcileStore(nil, config).(*isolationScopedReconcileStore)
	if !ok || store.target != config.localAcceptance.target {
		t.Fatal("local worker leasing did not use the accepted target")
	}
	for name := range values {
		if !strings.HasPrefix(name, "DATAGROUND_LOCAL_RUNTIME_") {
			continue
		}
		changed := validLocalAcceptanceEnvironment()
		delete(changed, name)
		if _, err := loadWorkerConfig(mapEnvironment(changed)); err == nil {
			t.Errorf("missing %s accepted", name)
		}
	}
	for name, value := range map[string]string{
		"DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE":      "",
		"DATAGROUND_LOCAL_RUNTIME_SERVICE_ID":         "svc_invalid",
		"DATAGROUND_LOCAL_RUNTIME_MINIMUM_GENERATION": "9007199254740992",
		"DATAGROUND_LOCAL_RUNTIME_ENVELOPE":           "relative.json",
		"DATAGROUND_LOCAL_RUNTIME_TRUST":              "/private/tmp/acceptance/envelope.json",
		"DATAGROUND_LOCAL_RUNTIME_EVIDENCE_DIRECTORY": "/private/tmp/../tmp/evidence",
		"DATAGROUND_LOCAL_RUNTIME_IMAGE":              "ghcr.io/asabla/dataground-codex-candidate:latest",
		"DATAGROUND_LOCAL_RUNTIME_MODEL":              "model with spaces",
		"DATAGROUND_LOCAL_RUNTIME_GITHUB_BINARY":      "/private/tmp/unsafe:/bin/gh",
		"DATAGROUND_LOCAL_RUNTIME_NODE_BINARY":        "node",
		"DATAGROUND_LOCAL_RUNTIME_REJECTED_IDS":       "",
	} {
		changed := validLocalAcceptanceEnvironment()
		changed[name] = value
		if _, err := loadWorkerConfig(mapEnvironment(changed)); err == nil {
			t.Errorf("unsafe %s accepted", name)
		}
	}
}

func TestLocalAcceptanceCommandPinsScopeAndInheritsOnlyExecutableSearchPath(t *testing.T) {
	config := localAcceptanceConfig(t)
	config.rejectedIDs = []string{"rtlocal_abcdefghij0123456789"}
	calls := 0
	checker := &localRuntimeAcceptanceChecker{config: config, run: func(ctx context.Context, binary string, args, environment []string) ([]byte, error) {
		calls++
		if binary != config.nodeBinary || !reflect.DeepEqual(environment, []string{"PATH=/usr/bin:/usr/bin:/bin"}) {
			t.Fatal("verifier inherited unexpected executable or environment")
		}
		want := []string{localRuntimeVerifier, "verify", config.envelopeFile, config.trustFile, config.evidenceDirectory, config.trustSHA256, config.sourceRevision, config.envelopeSHA256, config.target.isolationDomainID, config.target.serviceID, config.target.revisionID, "3", config.rejectedIDs[0]}
		if !slices.Equal(args, want) {
			t.Fatal("verifier scope or evidence arguments changed")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 75*time.Second {
			t.Fatal("verifier deadline missing or too broad")
		}
		return json.Marshal(acceptanceReceipt(config))
	}}
	for range 2 {
		if err := checker.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("acceptance was cached instead of rechecked")
	}
}

func TestLocalAcceptanceRejectsSubstitutedOrExpiredVerifierReceipts(t *testing.T) {
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { v["image"] = governedSandboxImage },
		func(v map[string]any) { v["model"] = "other-model" },
		func(v map[string]any) { v["profile"] = governedCertificationProfile },
		func(v map[string]any) { v["expiresAt"] = time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano) },
		func(v map[string]any) { v["generation"] = 2 },
		func(v map[string]any) { v["certificationEligible"] = true },
		func(v map[string]any) { delete(v, "certificationEligible") },
		func(v map[string]any) { v["deploymentScope"] = "production" },
		func(v map[string]any) { v["scope"].(map[string]any)["isolationDomainId"] = "iso_abcdefghij0123456789" },
		func(v map[string]any) { v["scope"].(map[string]any)["serviceId"] = "svc_abcdefghij0123456789" },
		func(v map[string]any) { v["scope"].(map[string]any)["revisionId"] = "rev_abcdefghij0123456789" },
		func(v map[string]any) { v["extra"] = true },
	} {
		config := localAcceptanceConfig(t)
		checker := &localRuntimeAcceptanceChecker{config: config, run: func(context.Context, string, []string, []string) ([]byte, error) {
			receipt := acceptanceReceipt(config)
			mutate(receipt)
			return json.Marshal(receipt)
		}}
		if err := checker.Check(context.Background()); !errors.Is(err, ErrRuntimeCertificationUnavailable) {
			t.Fatal("substituted acceptance receipt was accepted")
		}
	}
	for _, output := range []string{"", "{}{}", strings.Repeat("x", maximumAcceptanceOutput+1)} {
		checker := &localRuntimeAcceptanceChecker{config: localAcceptanceConfig(t), run: func(context.Context, string, []string, []string) ([]byte, error) { return []byte(output), nil }}
		if checker.Check(context.Background()) == nil {
			t.Fatal("malformed receipt was accepted")
		}
	}
	config := localAcceptanceConfig(t)
	config.rejectedIDs = []string{"rtlocal_0123456789abcdefghij"}
	checker := &localRuntimeAcceptanceChecker{config: config, run: func(context.Context, string, []string, []string) ([]byte, error) {
		return json.Marshal(acceptanceReceipt(config))
	}}
	if checker.Check(context.Background()) == nil {
		t.Fatal("revoked acceptance was accepted")
	}
}

func TestLocalAcceptanceProcessOutputIsBoundedAndFailureOutputIsWithheld(t *testing.T) {
	if _, err := executeLocalAcceptance(context.Background(), "/bin/echo", []string{strings.Repeat("x", maximumAcceptanceOutput+1)}, []string{}); !errors.Is(err, ErrRuntimeCertificationUnavailable) {
		t.Fatal("oversized process output was accepted")
	}
	checker := &localRuntimeAcceptanceChecker{config: localAcceptanceConfig(t), run: func(context.Context, string, []string, []string) ([]byte, error) {
		return nil, errors.New("synthetic private upstream details")
	}}
	if err := checker.Check(context.Background()); err == nil || strings.Contains(err.Error(), "synthetic private") {
		t.Fatal("upstream failure was accepted or disclosed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if checker.Check(ctx) == nil {
		t.Fatal("cancelled verification was accepted")
	}
}

func TestLocalAcceptancePinsCandidatePolicyAndRuntimeModel(t *testing.T) {
	config := localAcceptanceConfig(t)
	plan := execution.ExecutionPlan{RuntimeProfile: reconcile.CodexAppServerRuntimeProfileV1, ImageReference: config.image, EnforcementBundleDigest: localEnforcementDigest, ProviderProfiles: []string{governedProviderProfile}, RequiredCapabilities: []string{reconcile.CodexAppServerRuntimeProfileV1}}
	if !validGovernedDevelopmentPlan(plan, config.image) || validGovernedDevelopmentPlan(plan, "") {
		t.Fatal("candidate plan did not require explicit image binding")
	}
	for _, mutate := range []func(*execution.ExecutionPlan){
		func(p *execution.ExecutionPlan) { p.ImageReference = governedSandboxImage },
		func(p *execution.ExecutionPlan) { p.EnforcementBundleDigest = governedEnforcementDigest },
		func(p *execution.ExecutionPlan) { p.EnforcementBundleDigest = "" },
		func(p *execution.ExecutionPlan) { p.ProviderProfiles = []string{"other"} },
	} {
		changed := plan
		mutate(&changed)
		if validGovernedDevelopmentPlan(changed, config.image) {
			t.Fatal("candidate plan substitution was accepted")
		}
	}
	policy, err := os.ReadFile("../../deploy/openshell/codex-compatibility/runtime-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(policy)
	if "sha256:"+hex.EncodeToString(digest[:]) != localEnforcementDigest {
		t.Fatal("candidate policy digest drift")
	}
	target := governedArtifactTarget("/sandbox/result.json")
	request, err := (governedCodexRuntimeRequestBuilder{model: config.model}).BuildInvocationRuntimeRequest(target)
	if err != nil || request.Model != config.model || request.WorkingDir != "/sandbox" {
		t.Fatal("accepted model or workspace was not bound")
	}
	target.Input["model"] = "caller-override"
	if _, err := (governedCodexRuntimeRequestBuilder{model: config.model}).BuildInvocationRuntimeRequest(target); !errors.Is(err, reconcile.ErrInvocationRuntimeInputInvalid) {
		t.Fatal("caller model override was accepted")
	}
}

func TestLocalRuntimeAcceptanceActualEvidence(t *testing.T) {
	directory := os.Getenv("DATAGROUND_LOCAL_RUNTIME_ACCEPTANCE_TEST_DIRECTORY")
	if directory == "" {
		t.Skip("explicit local acceptance evidence directory is not configured")
	}
	envelope, err := os.ReadFile(filepath.Join(directory, "envelope.json"))
	if err != nil {
		t.Fatal("read envelope")
	}
	trust, err := os.ReadFile(filepath.Join(directory, "trust.json"))
	if err != nil {
		t.Fatal("read trust")
	}
	var document struct {
		Statement struct {
			SourceRevision string `json:"sourceRevision"`
			Model          string `json:"model"`
			Publication    struct {
				Digest string `json:"digest"`
			} `json:"publication"`
			Scope struct {
				IsolationDomainID string `json:"isolationDomainId"`
				ServiceID         string `json:"serviceId"`
				RevisionID        string `json:"revisionId"`
			} `json:"scope"`
		} `json:"statement"`
	}
	if json.Unmarshal(envelope, &document) != nil {
		t.Fatal("parse envelope")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal(err)
	}
	envelopeDigest, trustDigest := sha256.Sum256(envelope), sha256.Sum256(trust)
	config := localRuntimeAcceptanceConfig{target: runtimeCertificationTarget{isolationDomainID: document.Statement.Scope.IsolationDomainID, serviceID: document.Statement.Scope.ServiceID, revisionID: document.Statement.Scope.RevisionID}, envelopeFile: filepath.Join(directory, "envelope.json"), trustFile: filepath.Join(directory, "trust.json"), evidenceDirectory: filepath.Join(directory, "evidence"), envelopeSHA256: hex.EncodeToString(envelopeDigest[:]), trustSHA256: hex.EncodeToString(trustDigest[:]), sourceRevision: document.Statement.SourceRevision, minimumGeneration: 1, image: "ghcr.io/asabla/dataground-codex-candidate@" + document.Statement.Publication.Digest, model: document.Statement.Model, nodeBinary: node, githubBinary: gh}
	checker := &localRuntimeAcceptanceChecker{config: config, run: func(ctx context.Context, binary string, args, env []string) ([]byte, error) {
		args[0] = filepath.Join("../..", args[0])
		return executeLocalAcceptance(ctx, binary, args, env)
	}}
	if err := checker.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	checker.config.model = "different-model"
	if checker.Check(context.Background()) == nil {
		t.Fatal("actual evidence accepted a different deployment model")
	}
}
