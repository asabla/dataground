package canaryevidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/security/canarycollect"
)

func TestRunOwnsFinalEvidenceAndCleanup(t *testing.T) {
	t.Parallel()

	var cleanupRequests []CleanupRequest
	config := validConfig(func(_ context.Context, request CleanupRequest) error {
		cleanupRequests = append(cleanupRequests, request)
		return nil
	})
	result, err := Run(context.Background(), config)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	if bytes.Contains(encoded, []byte("inspected source")) {
		t.Fatalf("evidence retained source content: %s", encoded)
	}

	var record map[string]any
	if err := json.Unmarshal(encoded, &record); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if record["schemaVersion"] != schemaVersion || record["result"] != "passed" {
		t.Fatalf("unexpected final evidence = %v", record)
	}
	checks, ok := record["checks"].([]any)
	if !ok || len(checks) != 7 {
		t.Fatalf("checks = %v", record["checks"])
	}
	runRecord := record["run"].(map[string]any)
	if runRecord["id"] != config.RunID ||
		runRecord["canaryCommitment"] != config.CanaryCommitment {
		t.Fatalf("run = %v", runRecord)
	}
	resourcesRecord := runRecord["resources"].(map[string]any)
	if len(resourcesRecord) != 5 ||
		resourcesRecord["gateway"] != config.Resources.Gateway ||
		resourcesRecord["sandbox"] != config.Resources.Sandbox ||
		resourcesRecord["provider"] != config.Resources.Provider ||
		resourcesRecord["runtime"] != config.Resources.Runtime ||
		resourcesRecord["workspace"] != config.Resources.Workspace {
		t.Fatalf("resources = %v", resourcesRecord)
	}
	verifierRecord := runRecord["verifier"].(map[string]any)
	if verifierRecord["name"] != verifierName || verifierRecord["version"] != verifierVersion {
		t.Fatalf("verifier = %v", verifierRecord)
	}
	profileRecord := record["profile"].(map[string]any)
	if len(profileRecord) != 8 ||
		profileRecord["openshellCommit"] != openshellCommit ||
		profileRecord["providerProfileSourceSHA256"] != providerProfileSHA ||
		profileRecord["runtimeVersion"] != runtimeVersion {
		t.Fatalf("profile = %v", profileRecord)
	}
	cleanupRecord := record["cleanup"].(map[string]any)
	for _, field := range []string{"sandbox", "providerBinding", "workspace"} {
		receipt := cleanupRecord[field].(map[string]any)
		if len(receipt) != 2 || receipt["status"] != cleanupStatusRemoved {
			t.Fatalf("cleanup %s = %v", field, receipt)
		}
	}
	if len(cleanupRequests) != 3 ||
		cleanupRequests[0].ResourceKind != "sandbox" ||
		cleanupRequests[1].ResourceKind != "provider" ||
		cleanupRequests[2].ResourceKind != "workspace" {
		t.Fatalf("cleanup requests = %v", cleanupRequests)
	}
	for _, request := range cleanupRequests {
		if request.RunID != config.RunID {
			t.Fatalf("cleanup request = %v", request)
		}
	}
}

func TestRunCleansUpAfterCollectionFailureAndStaysOpaque(t *testing.T) {
	t.Parallel()

	cleanupCalls := 0
	config := validConfig(func(context.Context, CleanupRequest) error {
		cleanupCalls++
		return nil
	})
	config.Sources[0].Acquire = func(context.Context, canarycollect.SourceRequest) (io.ReadCloser, error) {
		return nil, errors.New("sensitive acquisition failure")
	}

	result, err := Run(context.Background(), config)
	if !errors.Is(err, ErrRunIncomplete) || !errors.Is(err, ErrCollection) {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("Run() leaked source error: %v", err)
	}
	if cleanupCalls != 3 {
		t.Fatalf("cleanup calls = %d", cleanupCalls)
	}
	if _, marshalErr := json.Marshal(result); !errors.Is(marshalErr, ErrRunIncomplete) {
		t.Fatalf("marshal incomplete run error = %v", marshalErr)
	}
}

func TestRunCleansUpAfterCancellationWithRunContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cleanupCalls := 0
	config := validConfig(func(cleanupCtx context.Context, request CleanupRequest) error {
		cleanupCalls++
		if err := cleanupCtx.Err(); err != nil {
			t.Fatalf("cleanup context error = %v", err)
		}
		if request.RunID != "0123456789abcdef0123456789abcdef" {
			t.Fatalf("cleanup request = %v", request)
		}
		return nil
	})

	_, err := Run(ctx, config)
	if !errors.Is(err, ErrRunIncomplete) ||
		!errors.Is(err, ErrCollection) ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if cleanupCalls != 3 {
		t.Fatalf("cleanup calls = %d", cleanupCalls)
	}
}

func TestRunAttemptsEveryCleanupAndRejectsUncertainty(t *testing.T) {
	t.Parallel()

	var cleanupKinds []string
	config := validConfig(func(_ context.Context, request CleanupRequest) error {
		cleanupKinds = append(cleanupKinds, request.ResourceKind)
		if request.ResourceKind == "provider" {
			return errors.New("sensitive cleanup failure")
		}
		return nil
	})

	result, err := Run(context.Background(), config)
	if !errors.Is(err, ErrRunIncomplete) || !errors.Is(err, ErrCleanup) {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("Run() leaked cleanup error: %v", err)
	}
	if strings.Join(cleanupKinds, ",") != "sandbox,provider,workspace" {
		t.Fatalf("cleanup order = %v", cleanupKinds)
	}
	if _, marshalErr := json.Marshal(result); !errors.Is(marshalErr, ErrRunIncomplete) {
		t.Fatalf("marshal uncertain run error = %v", marshalErr)
	}
}

func TestRunRejectsInvalidPlanBeforeCollectionOrCleanup(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*Config){
		"workspace": func(config *Config) {
			config.Resources.Workspace = "Invalid Workspace"
		},
		"unbound workspace": func(config *Config) {
			config.Resources.Workspace = "dg-canary-fedcba9876543210fedcba9876543210"
		},
		"cleanup": func(config *Config) {
			config.Cleanup.Workspace = nil
		},
		"source": func(config *Config) {
			config.Sources = config.Sources[:6]
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			acquisitions := 0
			cleanups := 0
			config := validConfig(func(context.Context, CleanupRequest) error {
				cleanups++
				return nil
			})
			for index := range config.Sources {
				config.Sources[index].Acquire = func(context.Context, canarycollect.SourceRequest) (io.ReadCloser, error) {
					acquisitions++
					return io.NopCloser(strings.NewReader("safe")), nil
				}
			}
			mutate(&config)

			if _, err := Run(context.Background(), config); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("Run() error = %v", err)
			}
			if acquisitions != 0 || cleanups != 0 {
				t.Fatalf("side effects before validation: acquisitions=%d cleanups=%d", acquisitions, cleanups)
			}
		})
	}
}

func TestRunRejectsNilContext(t *testing.T) {
	t.Parallel()

	if _, err := Run(nil, validConfig(func(context.Context, CleanupRequest) error {
		return nil
	})); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunRejectsClockRegression(t *testing.T) {
	t.Parallel()

	times := []time.Time{
		time.Date(2026, time.July, 25, 12, 0, 1, 0, time.UTC),
		time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC),
	}
	index := 0
	result, err := runEvidence(context.Background(), validConfig(func(context.Context, CleanupRequest) error {
		return nil
	}), func() time.Time {
		value := times[index]
		index++
		return value
	})
	if !errors.Is(err, ErrClock) || !errors.Is(err, ErrRunIncomplete) {
		t.Fatalf("runEvidence() error = %v", err)
	}
	if _, marshalErr := json.Marshal(result); !errors.Is(marshalErr, ErrRunIncomplete) {
		t.Fatalf("marshal regressed run error = %v", marshalErr)
	}
}

func validConfig(cleanup CleanupFunc) Config {
	resources := Resources{
		Gateway:   "dataground-gateway",
		Sandbox:   "sandbox-credential-check",
		Provider:  "provider-credential-check",
		Runtime:   "runtime-invocation",
		Workspace: "dg-canary-0123456789abcdef0123456789abcdef",
	}
	surfaces := []string{
		"sandbox-process",
		"sandbox-environment",
		"sandbox-filesystem",
		"provider-arguments",
		"gateway-logs",
		"sandbox-logs",
		"runtime-errors",
	}
	sources := make([]canarycollect.Source, 0, len(surfaces))
	for _, surface := range surfaces {
		surface := surface
		sources = append(sources, canarycollect.Source{
			Surface: surface,
			Acquire: func(context.Context, canarycollect.SourceRequest) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("inspected source " + surface)), nil
			},
		})
	}
	digest := sha256.Sum256([]byte("test canary"))
	return Config{
		RunID:            "0123456789abcdef0123456789abcdef",
		CanaryCommitment: "sha256:" + hex.EncodeToString(digest[:]),
		Resources:        resources,
		Sources:          sources,
		Cleanup: Cleanup{
			Sandbox:         cleanup,
			ProviderBinding: cleanup,
			Workspace:       cleanup,
		},
	}
}
