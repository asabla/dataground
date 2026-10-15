package execution

import (
	"context"
	"errors"
	"github.com/asabla/dataground/internal/workspace"
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

func sourceExecutionPlan() ExecutionPlan {
	plan := testExecutionPlan()
	plan.SchemaVersion = ExecutionPlanSchemaV2
	plan.WorkspaceSource = &workspace.SourceRecord{SchemaVersion: workspace.SourceRecordV1, IsolationDomainID: plan.IsolationDomainID, WorkspaceID: "wsp_" + strings.Repeat("a", 20), ID: "src_" + strings.Repeat("b", 20), ArchiveDigest: "sha256:" + strings.Repeat("c", 64), StorageProfileDigest: "sha256:" + strings.Repeat("d", 64), ArchiveBytes: 2048, EntryCount: 1, SourceBytes: 3}
	return plan
}
func TestSourceExecutionPlanPinsAndCopiesCompleteRecord(t *testing.T) {
	plan := sourceExecutionPlan()
	normalized, err := NormalizeExecutionPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := DigestExecutionPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ExecutionPlan){
		"workspace": func(p *ExecutionPlan) { p.WorkspaceSource.WorkspaceID = "wsp_" + strings.Repeat("b", 20) },
		"source":    func(p *ExecutionPlan) { p.WorkspaceSource.ID = "src_" + strings.Repeat("c", 20) },
		"archive":   func(p *ExecutionPlan) { p.WorkspaceSource.ArchiveDigest = "sha256:" + strings.Repeat("d", 64) },
		"storage":   func(p *ExecutionPlan) { p.WorkspaceSource.StorageProfileDigest = "sha256:" + strings.Repeat("e", 64) },
		"bytes":     func(p *ExecutionPlan) { p.WorkspaceSource.ArchiveBytes += 512 },
		"entries":   func(p *ExecutionPlan) { p.WorkspaceSource.EntryCount++ },
		"contents":  func(p *ExecutionPlan) { p.WorkspaceSource.SourceBytes++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := CloneExecutionPlan(plan)
			change(&changed)
			other, err := DigestExecutionPlan(changed)
			if err != nil || other == digest || EqualExecutionPlans(changed, plan) {
				t.Fatal("source changed without changing binding", err)
			}
		})
	}
	plan.WorkspaceSource.ID = "src_" + strings.Repeat("z", 20)
	if normalized.WorkspaceSource.ID == plan.WorkspaceSource.ID {
		t.Fatal("normalized source aliases input")
	}
	store := NewMemoryExecutionPlanStore()
	binding := ExecutionPlanBinding{Plan: normalized, ActorID: "operator", CorrelationID: "correlation"}
	saved, err := store.BindExecutionPlan(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	saved.WorkspaceSource.EntryCount++
	loaded, err := store.GetExecutionPlan(context.Background(), normalized.IsolationDomainID, normalized.RevisionID)
	if err != nil || !EqualExecutionPlans(loaded, normalized) {
		t.Fatal("source aliases memory store", err)
	}
	for name, change := range map[string]func(*ExecutionPlan){
		"v1 with source":  func(p *ExecutionPlan) { p.SchemaVersion = ExecutionPlanSchemaV1 },
		"missing source":  func(p *ExecutionPlan) { p.WorkspaceSource = nil },
		"cross domain":    func(p *ExecutionPlan) { p.WorkspaceSource.IsolationDomainID = "iso_" + strings.Repeat("z", 20) },
		"record version":  func(p *ExecutionPlan) { p.WorkspaceSource.SchemaVersion = "dataground.workspace-source/v2" },
		"invalid archive": func(p *ExecutionPlan) { p.WorkspaceSource.ArchiveDigest = "latest" },
		"invalid bounds":  func(p *ExecutionPlan) { p.WorkspaceSource.ArchiveBytes++ },
	} {
		t.Run(name, func(t *testing.T) {
			p := sourceExecutionPlan()
			change(&p)
			if _, err := NormalizeExecutionPlan(p); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
}
func TestAdmissionRejectsUnimplementedSourceProvisioning(t *testing.T) {
	plan := sourceExecutionPlan()
	plans := NewMemoryExecutionPlanStore()
	if _, err := plans.BindExecutionPlan(context.Background(), ExecutionPlanBinding{Plan: plan, ActorID: "operator", CorrelationID: "correlation"}); err != nil {
		t.Fatal(err)
	}
	source, provider := &admissionBundleSource{}, &admissionProviderStub{}
	admission, err := NewAdmission(plans, source, provider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admission.Admit(context.Background(), AdmissionRequest{IsolationDomainID: plan.IsolationDomainID, RevisionID: plan.RevisionID, OperationID: "op_" + strings.Repeat("a", 20)}); !errors.Is(err, ErrWorkspaceSourceProvisioningUnavailable) {
		t.Fatal("source plan entered unsupported provisioning", err)
	}
	if source.calls != 0 || len(provider.placements) != 0 || len(provider.creates) != 0 {
		t.Fatal("source plan reached external effects")
	}
}

func TestExecutionPlanV1CanonicalDigestRemainsStable(t *testing.T) {
	digest, err := DigestExecutionPlan(testExecutionPlan())
	if err != nil || digest != "sha256:ff9dc025f1bca10cd80e3b4cd3ca52357eacd0f37cd5dbc0b0a3654375229c90" {
		t.Fatal("version 1 canonical digest changed", digest, err)
	}
}
