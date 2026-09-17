package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/runtimeevidence"
	"github.com/asabla/dataground/internal/reconcile"
)

func validStrictAcceptanceEnvironment() map[string]string {
	values := validLocalAcceptanceEnvironment()
	for key, value := range map[string]string{
		"DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE":             strictLocalRuntimeProfile,
		"DATAGROUND_LOCAL_RUNTIME_SUPERVISOR_IMAGE":          "ghcr.io/asabla/dataground-supervisor-candidate@sha256:" + strings.Repeat("e", 64),
		"DATAGROUND_LOCAL_RUNTIME_SUPERVISOR_LOCAL_IMAGE_ID": "sha256:" + strings.Repeat("f", 64),
		"DATAGROUND_LOCAL_RUNTIME_GATEWAY_CONFIG_SHA256":     strings.Repeat("1", 64),
		"DATAGROUND_LOCAL_RUNTIME_TOPOLOGY_RUN_ID":           strings.Repeat("2", 32),
		"DATAGROUND_LOCAL_RUNTIME_GATEWAY_CONTAINER_ID":      strings.Repeat("3", 64),
		"DATAGROUND_LOCAL_RUNTIME_GATEWAY_STARTED_AT":        "2026-01-01T00:00:00Z",
		"DATAGROUND_LOCAL_RUNTIME_TOPOLOGY_ROOT":             "/private/tmp/topology",
		"DATAGROUND_LOCAL_RUNTIME_DOCKER_BINARY":             "/usr/bin/docker",
	} {
		values[key] = value
	}
	return values
}
func strictAcceptanceConfig(t *testing.T) localRuntimeAcceptanceConfig {
	t.Helper()
	config, err := loadWorkerConfig(mapEnvironment(validStrictAcceptanceEnvironment()))
	if err != nil || config.localAcceptance == nil || config.localAcceptance.strict == nil {
		t.Fatal("strict profile not selected", err)
	}
	return *config.localAcceptance
}
func strictAcceptanceReceipt(config localRuntimeAcceptanceConfig) map[string]any {
	receipt := acceptanceReceipt(config)
	receipt["profile"] = strictLocalRuntimeProfile
	receipt["supervisorImage"] = config.strict.supervisorImage
	receipt["supervisorLocalImageId"] = config.strict.topology.SupervisorLocalImageID
	receipt["gatewayConfigSHA256"] = config.strict.topology.GatewayConfigSHA256
	receipt["enforcementDigest"] = strictLocalEnforcementDigest
	return receipt
}

type fakeRuntimeDeployment struct {
	checks atomic.Int32
	closes atomic.Int32
	check  func(context.Context) error
}

func (deployment *fakeRuntimeDeployment) Check(ctx context.Context) error {
	deployment.checks.Add(1)
	if deployment.check != nil {
		return deployment.check(ctx)
	}
	return nil
}
func (deployment *fakeRuntimeDeployment) Close() error { deployment.closes.Add(1); return nil }

func TestStrictWorkerRequiresCompleteIndependentDeploymentPins(t *testing.T) {
	config := strictAcceptanceConfig(t)
	if config.acceptanceProfile() != strictLocalRuntimeProfile {
		t.Fatal("strict profile downgraded")
	}
	for key := range validStrictAcceptanceEnvironment() {
		if !strings.HasPrefix(key, "DATAGROUND_LOCAL_RUNTIME_") {
			continue
		}
		values := validStrictAcceptanceEnvironment()
		delete(values, key)
		if _, err := loadWorkerConfig(mapEnvironment(values)); err == nil {
			t.Errorf("missing %s accepted", key)
		}
	}
	for key, value := range map[string]string{
		"DATAGROUND_LOCAL_RUNTIME_SUPERVISOR_IMAGE":          "supervisor:latest",
		"DATAGROUND_LOCAL_RUNTIME_SUPERVISOR_LOCAL_IMAGE_ID": "sha256:invalid",
		"DATAGROUND_LOCAL_RUNTIME_GATEWAY_CONFIG_SHA256":     "invalid",
		"DATAGROUND_LOCAL_RUNTIME_TOPOLOGY_RUN_ID":           "foreign-run",
		"DATAGROUND_LOCAL_RUNTIME_GATEWAY_CONTAINER_ID":      "named-gateway",
		"DATAGROUND_LOCAL_RUNTIME_GATEWAY_STARTED_AT":        time.Now().Add(time.Hour).Format(time.RFC3339Nano),
		"DATAGROUND_LOCAL_RUNTIME_TOPOLOGY_ROOT":             "relative",
		"DATAGROUND_LOCAL_RUNTIME_DOCKER_BINARY":             "/private/tmp/unsafe:/bin/docker",
	} {
		values := validStrictAcceptanceEnvironment()
		values[key] = value
		if _, err := loadWorkerConfig(mapEnvironment(values)); err == nil {
			t.Errorf("unsafe %s accepted", key)
		}
	}
}

func TestStrictAcceptanceRejectsReceiptSubstitutionBeforeDeploymentAccess(t *testing.T) {
	config := strictAcceptanceConfig(t)
	for name, mutate := range map[string]func(map[string]any){
		"legacy profile": func(v map[string]any) { v["profile"] = localRuntimeProfile },
		"supervisor publication": func(v map[string]any) {
			v["supervisorImage"] = "ghcr.io/asabla/dataground-supervisor-candidate@sha256:" + strings.Repeat("a", 64)
		},
		"supervisor identity":   func(v map[string]any) { v["supervisorLocalImageId"] = "sha256:" + strings.Repeat("a", 64) },
		"gateway configuration": func(v map[string]any) { v["gatewayConfigSHA256"] = strings.Repeat("a", 64) },
		"legacy policy":         func(v map[string]any) { v["enforcementDigest"] = localEnforcementDigest },
		"missing supervisor":    func(v map[string]any) { delete(v, "supervisorImage") },
		"null supervisor":       func(v map[string]any) { v["supervisorImage"] = nil },
		"certification claim":   func(v map[string]any) { v["certificationEligible"] = true },
		"wrong scope":           func(v map[string]any) { v["scope"].(map[string]any)["isolationDomainId"] = "iso_abcdefghij0123456789" },
	} {
		t.Run(name, func(t *testing.T) {
			receipt := strictAcceptanceReceipt(config)
			mutate(receipt)
			checker := &localRuntimeAcceptanceChecker{config: config, run: func(context.Context, string, []string, []string) ([]byte, error) { return json.Marshal(receipt) }, observe: func(runtimeevidence.ObservedDockerTopologyConfig) (runtimeDeploymentObservation, error) {
				t.Fatal("invalid receipt reached deployment")
				return nil, nil
			}}
			if checker.Check(context.Background()) != ErrRuntimeCertificationUnavailable {
				t.Fatal("invalid strict receipt accepted")
			}
		})
	}
}
func TestLegacyReceiptDoesNotAcceptStrictFieldsIncludingNull(t *testing.T) {
	config := localAcceptanceConfig(t)
	for _, field := range []string{"supervisorImage", "supervisorLocalImageId", "gatewayConfigSHA256", "enforcementDigest"} {
		receipt := acceptanceReceipt(config)
		receipt[field] = nil
		checker := &localRuntimeAcceptanceChecker{config: config, run: func(context.Context, string, []string, []string) ([]byte, error) { return json.Marshal(receipt) }}
		if checker.Check(context.Background()) == nil {
			t.Fatalf("legacy receipt accepted %s", field)
		}
	}
}

func TestStrictAcceptanceReverifiesEvidenceAndDeploymentUntilClosed(t *testing.T) {
	config := strictAcceptanceConfig(t)
	var verifications, opens int
	deployment := &fakeRuntimeDeployment{}
	checker := &localRuntimeAcceptanceChecker{config: config, run: func(context.Context, string, []string, []string) ([]byte, error) {
		verifications++
		return json.Marshal(strictAcceptanceReceipt(config))
	}, observe: func(pins runtimeevidence.ObservedDockerTopologyConfig) (runtimeDeploymentObservation, error) {
		opens++
		if pins != config.strict.topology {
			t.Fatal("independent deployment pins changed")
		}
		return deployment, nil
	}}
	resources := &workerResources{readiness: checker, runtimeAcceptance: checker}
	for range 2 {
		if err := resources.Ready(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if verifications != 2 || opens != 1 || deployment.checks.Load() != 2 {
		t.Fatal("evidence or deployment verification was cached")
	}
	if err := resources.Close(); err != nil {
		t.Fatal(err)
	}
	if err := resources.Close(); err != nil {
		t.Fatal(err)
	}
	if deployment.closes.Load() != 1 || resources.Ready(context.Background()) == nil || verifications != 2 {
		t.Fatal("closed worker retained observation authority")
	}
}
func TestStrictDeploymentFailurePoisonsWorkerWithoutLeakingNativeErrors(t *testing.T) {
	config := strictAcceptanceConfig(t)
	deployment := &fakeRuntimeDeployment{check: func(context.Context) error { return errors.New("private native state") }}
	var opens, verifications int
	checker := &localRuntimeAcceptanceChecker{config: config, run: func(context.Context, string, []string, []string) ([]byte, error) {
		verifications++
		return json.Marshal(strictAcceptanceReceipt(config))
	}, observe: func(runtimeevidence.ObservedDockerTopologyConfig) (runtimeDeploymentObservation, error) {
		opens++
		return deployment, nil
	}}
	if err := checker.Check(context.Background()); err != ErrRuntimeCertificationUnavailable {
		t.Fatal("unsafe deployment accepted or disclosed", err)
	}
	deployment.check = nil
	if checker.Check(context.Background()) == nil || opens != 1 || deployment.checks.Load() != 1 || verifications != 1 {
		t.Fatal("poisoned deployment retried or recovered")
	}
	_ = checker.Close()
	if deployment.closes.Load() != 1 {
		t.Fatal("observation handles not released")
	}
}
func TestStrictAcceptanceChecksExpiryAndShutdownAfterObservation(t *testing.T) {
	for _, mode := range []string{"expired", "closed", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			config := strictAcceptanceConfig(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checker := &localRuntimeAcceptanceChecker{config: config}
			expires := time.Now().Add(100 * time.Millisecond)
			deployment := &fakeRuntimeDeployment{check: func(context.Context) error {
				switch mode {
				case "expired":
					time.Sleep(time.Until(expires) + time.Millisecond)
				case "closed":
					_ = checker.Close()
				case "cancelled":
					cancel()
				}
				return nil
			}}
			checker.run = func(context.Context, string, []string, []string) ([]byte, error) {
				receipt := strictAcceptanceReceipt(config)
				if mode == "expired" {
					receipt["expiresAt"] = expires.UTC().Format(time.RFC3339Nano)
				}
				return json.Marshal(receipt)
			}
			checker.observe = func(runtimeevidence.ObservedDockerTopologyConfig) (runtimeDeploymentObservation, error) {
				return deployment, nil
			}
			if checker.Check(ctx) == nil {
				t.Fatal("expired or closed observation accepted")
			}
			_ = checker.Close()
		})
	}
}
func TestStrictPlanRequiresExactPolicyAndProfile(t *testing.T) {
	config := strictAcceptanceConfig(t)
	plan := execution.ExecutionPlan{RuntimeProfile: reconcile.CodexAppServerRuntimeProfileV1, ImageReference: config.image, EnforcementBundleDigest: strictLocalEnforcementDigest, ProviderProfiles: []string{governedProviderProfile}, RequiredCapabilities: []string{reconcile.CodexAppServerRuntimeProfileV1}}
	if !validGovernedDevelopmentPlan(plan, config.image, strictLocalRuntimeProfile) {
		t.Fatal("strict plan rejected")
	}
	for _, profile := range []string{"", localRuntimeProfile, governedCertificationProfile} {
		if validGovernedDevelopmentPlan(plan, config.image, profile) {
			t.Fatal("strict plan used under another profile")
		}
	}
	plan.EnforcementBundleDigest = localEnforcementDigest
	if validGovernedDevelopmentPlan(plan, config.image, strictLocalRuntimeProfile) {
		t.Fatal("legacy policy accepted for strict execution")
	}
	content, err := os.ReadFile("../../deploy/openshell/codex-compatibility/rosetta-runtime-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	if "sha256:"+hex.EncodeToString(digest[:]) != strictLocalEnforcementDigest {
		t.Fatal("strict policy digest drift")
	}
}

func TestStrictWorkerDoesNotInferSupportForAnUntestedHost(t *testing.T) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		t.Skip("supported host; live deployment observation is tested separately")
	}
	if observer, err := observeStrictRuntimeDeployment(strictAcceptanceConfig(t).strict.topology); observer != nil || err != ErrRuntimeCertificationUnavailable {
		t.Fatal("untested host platform accepted")
	}
}
func TestStrictObserverConstructionFailureReleasesHandlesAndPoisonsWorker(t *testing.T) {
	config := strictAcceptanceConfig(t)
	deployment := &fakeRuntimeDeployment{}
	var opens int
	checker := &localRuntimeAcceptanceChecker{config: config, run: func(context.Context, string, []string, []string) ([]byte, error) {
		return json.Marshal(strictAcceptanceReceipt(config))
	}, observe: func(runtimeevidence.ObservedDockerTopologyConfig) (runtimeDeploymentObservation, error) {
		opens++
		return deployment, errors.New("private deployment path")
	}}
	if checker.Check(context.Background()) != ErrRuntimeCertificationUnavailable || checker.Check(context.Background()) == nil || deployment.checks.Load() != 0 || deployment.closes.Load() != 1 || opens != 1 {
		t.Fatal("failed observer construction retained handles or retried")
	}
}

func TestStrictConcurrentChecksCannotOutliveWorkerClose(t *testing.T) {
	config := strictAcceptanceConfig(t)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	deployment := &fakeRuntimeDeployment{check: func(context.Context) error { entered <- struct{}{}; <-release; return nil }}
	var opens atomic.Int32
	checker := &localRuntimeAcceptanceChecker{config: config, run: func(context.Context, string, []string, []string) ([]byte, error) {
		return json.Marshal(strictAcceptanceReceipt(config))
	}, observe: func(runtimeevidence.ObservedDockerTopologyConfig) (runtimeDeploymentObservation, error) {
		opens.Add(1)
		return deployment, nil
	}}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- checker.Check(context.Background()) }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("concurrent check did not reach deployment")
		}
	}
	if err := checker.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	for range 2 {
		if <-results != ErrRuntimeCertificationUnavailable {
			t.Fatal("verification escaped shutdown")
		}
	}
	if opens.Load() != 1 || deployment.closes.Load() != 1 {
		t.Fatal("concurrent checks duplicated observation handles")
	}
}
