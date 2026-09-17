package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/policy/rosetta"
)

func testConfiguration(t *testing.T) configuration {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return configuration{root: root, domainID: "iso_00000000000000000001", revisionID: "rev_00000000000000000001", actorID: "operator", correlationID: "installation-1", rosettaEndpoint: "http://127.0.0.1:18089", s3Endpoint: "http://127.0.0.1:8333", bucket: "dataground-conformance"}
}
func arguments(config configuration) []string {
	return []string{"--repository-root", config.root, "--isolation-domain", config.domainID, "--revision", config.revisionID, "--actor", config.actorID, "--correlation-id", config.correlationID, "--rosetta-endpoint", config.rosettaEndpoint, "--s3-endpoint", config.s3Endpoint, "--s3-bucket", config.bucket}
}

type fakeCompiler struct {
	material                         rosetta.Materialization
	compatibilityError, compileError error
	calls                            int
}

func (compiler *fakeCompiler) VerifyCompatibility(context.Context) (rosetta.Compatibility, error) {
	compiler.calls++
	return rosetta.Compatibility{}, compiler.compatibilityError
}
func (compiler *fakeCompiler) Materialize(context.Context, rosetta.MaterializeRequest) (rosetta.Materialization, error) {
	compiler.calls++
	return compiler.material, compiler.compileError
}

type fakeFinalizer struct {
	binding execution.EnforcementBundleBinding
	calls   int
	err     error
}

func (finalizer *fakeFinalizer) Finalize(_ context.Context, v execution.EnforcementBundleFinalization) (execution.EnforcementBundleRecord, error) {
	finalizer.calls++
	finalizer.binding = v.Binding
	if execution.VerifyEnforcementPolicy(v.Content, developmentPolicyDigest) != nil {
		return execution.EnforcementBundleRecord{}, errors.New("wrong content")
	}
	return v.Binding.Record, finalizer.err
}

func materialization(t *testing.T, request rosetta.MaterializeRequest) rosetta.Materialization {
	t.Helper()
	content, err := os.ReadFile("../../deploy/openshell/codex-compatibility/rosetta-runtime-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return rosetta.Materialization{Context: request.Context, BundleID: "rosetta-" + strings.Repeat("a", 64), Content: content, MediaType: execution.EnforcementBundleMediaType, Provenance: rosetta.Provenance{CompilerVersion: rosetta.CompilerVersionV1, CatalogVersion: rosetta.CatalogVersion, TargetContractVersion: rosetta.OpenShellTargetContractV1, Mode: rosetta.ModeStrict, InputDigest: developmentCompilerInputDigest, ArtifactDigest: developmentPolicyDigest, BindingDigest: "sha256:" + strings.Repeat("a", 64)}}
}

func TestInstallationUsesCheckedCompilerInputAndExactScopedOutput(t *testing.T) {
	config := testConfiguration(t)
	parsed, err := parseConfiguration(arguments(config))
	if err != nil || parsed != config {
		t.Fatal(err)
	}
	request, err := readCheckedRequest(config)
	if err != nil {
		t.Fatal(err)
	}
	if request.Context.IsolationDomainID != config.domainID || request.Context.ResourceID != config.revisionID || len(request.Catalog.Capabilities) != 5 {
		t.Fatal("fixture scope or denials changed")
	}
	compiler := &fakeCompiler{material: materialization(t, request)}
	finalizer := &fakeFinalizer{}
	record, err := install(context.Background(), config, request, compiler, finalizer)
	if err != nil || finalizer.calls != 1 || compiler.calls != 2 || record.Digest != developmentPolicyDigest || record.Provenance.SourceRevision != rosetta.CandidateSourceRevisionV1 || finalizer.binding.ActorID != config.actorID || finalizer.binding.CorrelationID != config.correlationID {
		t.Fatal("binding lost verified provenance", err)
	}
	if strings.Contains(record.ObjectKey, config.domainID+"/"+config.revisionID) == false {
		t.Fatal("object escaped scope")
	}
	for _, value := range compiler.material.Content {
		if value != 0 {
			t.Fatal("materialization bytes retained")
		}
	}
}

func TestInstallationRejectsCompilerDriftBeforeStorage(t *testing.T) {
	for _, mode := range []string{"scope", "request scope", "policy", "input", "version", "target", "mode", "compatibility", "compiler", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			config := testConfiguration(t)
			request, err := readCheckedRequest(config)
			if err != nil {
				t.Fatal(err)
			}
			compiler := &fakeCompiler{material: materialization(t, request)}
			finalizer := &fakeFinalizer{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "scope":
				compiler.material.Context.IsolationDomainID = "iso_00000000000000000002"
			case "request scope":
				request.Context.IsolationDomainID = "iso_00000000000000000002"
			case "policy":
				compiler.material.Content = []byte("private widened material")
			case "input":
				compiler.material.Provenance.InputDigest = "sha256:" + strings.Repeat("b", 64)
			case "version":
				compiler.material.Provenance.CompilerVersion = "other"
			case "target":
				compiler.material.Provenance.TargetContractVersion = "other"
			case "mode":
				compiler.material.Provenance.Mode = "permissive"
			case "compatibility":
				compiler.compatibilityError = errors.New("private compiler failure")
			case "compiler":
				compiler.compileError = errors.New("private compiler response")
			case "cancelled":
				cancel()
			}
			if _, err := install(ctx, config, request, compiler, finalizer); err != errInstallation || finalizer.calls != 0 {
				t.Fatal("unverified material reached storage", err)
			}
		})
	}
}

func TestInstallationRejectsAmbiguousConfigurationAndInputChanges(t *testing.T) {
	config := testConfiguration(t)
	for _, mutate := range []func([]string) []string{
		func(v []string) []string { return append(v, "--actor", "other") }, func(v []string) []string { return append(v, "positional") }, func(v []string) []string { return v[:len(v)-2] }, func(v []string) []string { v[3] = "foreign"; return v }, func(v []string) []string { v[7] = "operator\nsecret"; return v }, func(v []string) []string { v[11] = "http://localhost:8080"; return v }, func(v []string) []string { v[13] = "https://remote.example:443"; return v }, func(v []string) []string { v[13] = "http://127.0.0.1:8333?"; return v },
	} {
		if _, err := parseConfiguration(mutate(arguments(config))); err == nil {
			t.Fatal("invalid flags accepted")
		}
	}
	for _, mode := range []string{"changed", "symlink", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, developmentInputPath)
			if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
				t.Fatal("fixture")
			}
			content, err := os.ReadFile(filepath.Join(config.root, developmentInputPath))
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "changed":
				content = append(content, ' ')
			case "oversized":
				content = []byte(strings.Repeat("x", (16<<10)+1))
			case "symlink":
				if os.Symlink(filepath.Join(config.root, developmentInputPath), path) != nil {
					t.Fatal("fixture")
				}
			}
			if mode != "symlink" {
				if os.WriteFile(path, content, 0o600) != nil {
					t.Fatal("fixture")
				}
			}
			other := config
			other.root = root
			if _, err := readCheckedRequest(other); err != errInstallation {
				t.Fatal("changed fixture accepted", err)
			}
		})
	}
}

func TestInstallationWithholdsStorageFailureDetails(t *testing.T) {
	config := testConfiguration(t)
	request, err := readCheckedRequest(config)
	if err != nil {
		t.Fatal(err)
	}
	compiler := &fakeCompiler{material: materialization(t, request)}
	finalizer := &fakeFinalizer{err: errors.New("private database or object-store state")}
	if _, err := install(context.Background(), config, request, compiler, finalizer); err != errInstallation || finalizer.calls != 1 {
		t.Fatal("storage error disclosed", err)
	}
}
