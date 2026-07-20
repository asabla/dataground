package execution

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestNormalizeExecutionPlanCreatesStablePortableContract(t *testing.T) {
	plan := testExecutionPlan()
	plan.ProviderProfiles = []string{"anthropic", "codex", "anthropic"}
	plan.RequiredCapabilities = []string{"runtime.codex", "artifact.export", "runtime.codex"}

	normalized, err := NormalizeExecutionPlan(plan)
	if err != nil {
		t.Fatalf("normalize execution plan: %v", err)
	}
	if got, want := normalized.ProviderProfiles, []string{"anthropic", "codex"}; !slicesEqual(got, want) {
		t.Fatalf("provider profiles = %#v, want %#v", got, want)
	}
	if got, want := normalized.RequiredCapabilities, []string{"artifact.export", "runtime.codex"}; !slicesEqual(got, want) {
		t.Fatalf("required capabilities = %#v, want %#v", got, want)
	}
	firstDigest, err := DigestExecutionPlan(plan)
	if err != nil {
		t.Fatalf("digest execution plan: %v", err)
	}
	secondDigest, err := DigestExecutionPlan(normalized)
	if err != nil {
		t.Fatalf("digest normalized execution plan: %v", err)
	}
	if firstDigest != secondDigest || !strings.HasPrefix(firstDigest, "sha256:") || len(firstDigest) != 71 {
		t.Fatalf("unstable execution plan digests: %q and %q", firstDigest, secondDigest)
	}
	plan.ProviderProfiles[0] = "changed"
	if normalized.ProviderProfiles[0] != "anthropic" {
		t.Fatal("normalization retained the caller's provider profile slice")
	}
}

func TestNormalizeExecutionPlanRejectsMutableOrNonPortableInputs(t *testing.T) {
	tests := map[string]func(*ExecutionPlan){
		"schema":                 func(plan *ExecutionPlan) { plan.SchemaVersion = "dataground.execution-plan/v2" },
		"domain":                 func(plan *ExecutionPlan) { plan.IsolationDomainID = "another-domain" },
		"revision":               func(plan *ExecutionPlan) { plan.RevisionID = "revision" },
		"runtime whitespace":     func(plan *ExecutionPlan) { plan.RuntimeProfile = "codex app-server" },
		"mutable image":          func(plan *ExecutionPlan) { plan.ImageReference = "registry.invalid/dataground:latest" },
		"local enforcement path": func(plan *ExecutionPlan) { plan.EnforcementBundleID = "../policy.yaml" },
		"uppercase digest": func(plan *ExecutionPlan) {
			plan.RuntimeMatrixDigest = "sha256:" + strings.Repeat("A", 64)
		},
		"provider assignment": func(plan *ExecutionPlan) { plan.ProviderProfiles = []string{"TOKEN=value"} },
		"provider newline":    func(plan *ExecutionPlan) { plan.ProviderProfiles = []string{"codex\nother"} },
		"empty capability":    func(plan *ExecutionPlan) { plan.RequiredCapabilities = []string{""} },
		"too many providers": func(plan *ExecutionPlan) {
			plan.ProviderProfiles = make([]string, 65)
			for index := range plan.ProviderProfiles {
				plan.ProviderProfiles[index] = "provider"
			}
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			plan := testExecutionPlan()
			change(&plan)
			if _, err := NormalizeExecutionPlan(plan); err == nil {
				t.Fatal("invalid execution plan was accepted")
			}
		})
	}
}

func TestMemoryExecutionPlanStoreIsImmutableScopedAndConcurrent(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryExecutionPlanStore()
	plan := testExecutionPlan()
	plan.ProviderProfiles = []string{"codex", "anthropic", "codex"}
	binding := ExecutionPlanBinding{Plan: plan, ActorID: "worker:resolver", CorrelationID: "correlation-1"}

	start := make(chan struct{})
	errorsByWorker := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := store.BindExecutionPlan(ctx, binding)
			errorsByWorker <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatalf("concurrent plan replay: %v", err)
		}
	}

	stored, err := store.GetExecutionPlan(ctx, plan.IsolationDomainID, plan.RevisionID)
	if err != nil {
		t.Fatalf("get execution plan: %v", err)
	}
	stored.ProviderProfiles[0] = "changed"
	reloaded, err := store.GetExecutionPlan(ctx, plan.IsolationDomainID, plan.RevisionID)
	if err != nil {
		t.Fatalf("reload execution plan: %v", err)
	}
	if slicesEqual(stored.ProviderProfiles, reloaded.ProviderProfiles) {
		t.Fatal("returned execution plan aliases stored provider profiles")
	}

	changed := plan
	changed.RuntimeMatrixID = "matrix-v2"
	if _, err := store.BindExecutionPlan(ctx, ExecutionPlanBinding{
		Plan: changed, ActorID: binding.ActorID, CorrelationID: binding.CorrelationID,
	}); !errors.Is(err, ErrExecutionPlanConflict) {
		t.Fatalf("replace execution plan = %v, want ErrExecutionPlanConflict", err)
	}
	if _, err := store.GetExecutionPlan(ctx, "iso_"+strings.Repeat("b", 20), plan.RevisionID); !errors.Is(err, ErrExecutionPlanMissing) {
		t.Fatalf("cross-domain execution plan lookup = %v, want ErrExecutionPlanMissing", err)
	}
}

func testExecutionPlan() ExecutionPlan {
	return ExecutionPlan{
		SchemaVersion:             ExecutionPlanSchemaV1,
		IsolationDomainID:         "iso_" + strings.Repeat("a", 20),
		RevisionID:                "rev_" + strings.Repeat("b", 20),
		RuntimeProfile:            "codex.app-server/v1",
		EnvironmentRevisionID:     "environment-v1",
		ImageReference:            "registry.invalid/dataground/runtime@sha256:" + strings.Repeat("c", 64),
		EnvironmentManifestDigest: "sha256:" + strings.Repeat("d", 64),
		EnforcementBundleID:       "enforcement-bundle-v1",
		EnforcementBundleDigest:   "sha256:" + strings.Repeat("e", 64),
		RuntimeMatrixID:           "runtime-matrix-v1",
		RuntimeMatrixDigest:       "sha256:" + strings.Repeat("f", 64),
		ProviderProfiles:          []string{"codex"},
		RequiredCapabilities:      []string{"runtime.codex"},
	}
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
