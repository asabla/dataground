package canaryharness

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/openshell"
	"github.com/asabla/dataground/internal/security/canarydocker"
	"github.com/asabla/dataground/internal/security/canaryevidence"
	"github.com/asabla/dataground/internal/security/canaryprovider"
	"github.com/asabla/dataground/internal/security/canaryruntime"
	"github.com/asabla/dataground/internal/security/canarysource"
	"github.com/asabla/dataground/internal/security/canaryworkspace"
)

const testRunID = "0123456789abcdef0123456789abcdef"

type fakeProvisioner struct {
	binding execution.ProviderBinding
}

func (provisioner fakeProvisioner) CreateCredentialEvidenceProvider(
	_ context.Context,
	request execution.CredentialEvidenceProviderRequest,
) (execution.ProviderBinding, error) {
	if request.Name != provisioner.binding.Name || len(request.Canary) == 0 {
		return execution.ProviderBinding{}, errors.New("unexpected provisioning request")
	}
	return provisioner.binding, nil
}

func TestHarnessComposesExactRunOnce(t *testing.T) {
	config := validConfig(t)
	harness, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	copyOfHarness := *harness

	called := false
	_, err = harness.runWith(context.Background(), func(
		_ context.Context,
		evidenceConfig canaryevidence.Config,
	) (canaryevidence.Result, error) {
		called = true
		names, namesErr := NamesForRun(testRunID)
		if namesErr != nil {
			t.Fatal(namesErr)
		}
		if evidenceConfig.RunID != testRunID ||
			evidenceConfig.CanaryCommitment != config.Provisioned.Commitment() ||
			evidenceConfig.Resources.Gateway != names.Gateway ||
			evidenceConfig.Resources.Sandbox != config.Execution.ID ||
			evidenceConfig.Resources.Provider != names.Provider ||
			evidenceConfig.Resources.Runtime != names.Runtime ||
			evidenceConfig.Resources.Workspace != names.Workspace {
			t.Fatalf("unexpected evidence config: %#v", evidenceConfig)
		}
		if evidenceConfig.Cleanup.Sandbox == nil ||
			evidenceConfig.Cleanup.ProviderBinding == nil ||
			evidenceConfig.Cleanup.Workspace == nil {
			t.Fatal("cleanup plan is incomplete")
		}
		if err := evidenceConfig.Sources.ValidateBinding(
			testRunID,
			config.Provisioned.Commitment(),
			structResourceNames(evidenceConfig),
		); err != nil {
			t.Fatalf("source binding: %v", err)
		}
		return canaryevidence.Result{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("evidence runner was not called")
	}
	if _, err := copyOfHarness.runWith(
		context.Background(),
		func(context.Context, canaryevidence.Config) (canaryevidence.Result, error) {
			return canaryevidence.Result{}, nil
		},
	); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("copied Run() error = %v", err)
	}
}

func TestHarnessRejectsIdentityDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"nil provider", func(config *Config) { config.Provider = nil }},
		{"nil OpenShell sources", func(config *Config) { config.OpenShell = nil }},
		{"nil Docker sources", func(config *Config) { config.Docker = nil }},
		{"nil runtime sources", func(config *Config) { config.Runtime = nil }},
		{"wrong execution domain", func(config *Config) { config.Execution.IsolationDomainID = "other" }},
		{"wrong execution gateway", func(config *Config) { config.Execution.GatewayID = "other" }},
		{"unready execution", func(config *Config) { config.Execution.State = "provisioning" }},
		{"invalid execution name", func(config *Config) { config.Execution.ID = "INVALID" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig(t)
			test.mutate(&config)
			if _, err := New(config); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("New() error = %v", err)
			}
		})
	}
}

func TestHarnessDiscardsRuntimeCaptureWhenCompositionFails(t *testing.T) {
	config := validConfig(t)
	names, err := NamesForRun(testRunID)
	if err != nil {
		t.Fatal(err)
	}
	runtimeSources, err := canaryruntime.New(canaryruntime.Config{
		RunID:       testRunID,
		RuntimeName: names.Runtime,
		Session:     &fakeSession{errors: "captured runtime diagnostics"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := runtimeSources.Errors()
	if _, err := io.ReadAll(stream); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	config.Runtime = runtimeSources
	config.Execution.State = "provisioning"
	if _, err := New(config); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := runtimeSources.OpenRuntimeErrors(context.Background(), canarysource.Request{
		RunID: testRunID, Surface: "runtime-errors", ResourceName: names.Runtime,
	}); !errors.Is(err, canaryruntime.ErrCredentialSource) {
		t.Fatalf("OpenRuntimeErrors() error = %v", err)
	}
}

func TestRuntimeBoundaryClosesBeforeScanningOnce(t *testing.T) {
	names, err := NamesForRun(testRunID)
	if err != nil {
		t.Fatal(err)
	}
	session := &fakeSession{errors: "captured runtime diagnostics"}
	runtimeSources, err := canaryruntime.New(canaryruntime.Config{
		RunID: testRunID, RuntimeName: names.Runtime, Session: session,
	})
	if err != nil {
		t.Fatal(err)
	}
	drain := runtimeSources.Errors()
	if _, err := io.ReadAll(drain); err != nil {
		t.Fatal(err)
	}
	if err := drain.Close(); err != nil {
		t.Fatal(err)
	}

	boundary := &runtimeBoundary{runtime: runtimeSources}
	request := canarysource.Request{
		RunID: testRunID, Surface: "runtime-errors", ResourceName: names.Runtime,
	}
	source, err := boundary.OpenRuntimeErrors(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(source); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if session.closeCalls != 1 {
		t.Fatalf("runtime Close() calls = %d", session.closeCalls)
	}
	if _, err := boundary.OpenRuntimeErrors(context.Background(), request); !errors.Is(
		err,
		canaryruntime.ErrCredentialSource,
	) {
		t.Fatalf("second OpenRuntimeErrors() error = %v", err)
	}
	if session.closeCalls != 1 {
		t.Fatalf("runtime Close() calls after retry = %d", session.closeCalls)
	}
}

func TestRuntimeBoundarySanitizesCloseFailure(t *testing.T) {
	names, err := NamesForRun(testRunID)
	if err != nil {
		t.Fatal(err)
	}
	session := &fakeSession{errors: "captured runtime diagnostics", closeErr: errors.New("native detail")}
	runtimeSources, err := canaryruntime.New(canaryruntime.Config{
		RunID: testRunID, RuntimeName: names.Runtime, Session: session,
	})
	if err != nil {
		t.Fatal(err)
	}
	drain := runtimeSources.Errors()
	if _, err := io.ReadAll(drain); err != nil {
		t.Fatal(err)
	}
	if err := drain.Close(); err != nil {
		t.Fatal(err)
	}

	boundary := &runtimeBoundary{runtime: runtimeSources}
	request := canarysource.Request{
		RunID: testRunID, Surface: "runtime-errors", ResourceName: names.Runtime,
	}
	_, err = boundary.OpenRuntimeErrors(context.Background(), request)
	if !errors.Is(err, canaryruntime.ErrCredentialSource) ||
		errors.Is(err, session.closeErr) ||
		err.Error() == session.closeErr.Error() {
		t.Fatalf("OpenRuntimeErrors() error = %v", err)
	}
	_, _ = boundary.OpenRuntimeErrors(context.Background(), request)
	if session.closeCalls != 1 {
		t.Fatalf("runtime Close() calls = %d", session.closeCalls)
	}
}

func TestHarnessRejectsInvalidRunNamesAndSerialization(t *testing.T) {
	if _, err := NamesForRun("not-a-run"); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("NamesForRun() error = %v", err)
	}
	if _, err := json.Marshal(Config{}); !errors.Is(err, ErrSerialization) {
		t.Fatalf("Config MarshalJSON() error = %v", err)
	}
	if _, err := json.Marshal(Harness{}); !errors.Is(err, ErrSerialization) {
		t.Fatalf("Harness MarshalJSON() error = %v", err)
	}
}

func TestHarnessDoesNotConsumeCancelledStart(t *testing.T) {
	harness, err := New(validConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := harness.runWith(
		ctx,
		func(context.Context, canaryevidence.Config) (canaryevidence.Result, error) {
			t.Fatal("cancelled run reached evidence")
			return canaryevidence.Result{}, nil
		},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Run() error = %v", err)
	}
	if _, err := harness.runWith(
		context.Background(),
		func(context.Context, canaryevidence.Config) (canaryevidence.Result, error) {
			return canaryevidence.Result{}, nil
		},
	); err != nil {
		t.Fatalf("retry after pre-start cancellation: %v", err)
	}
}

type fakeSession struct {
	errors     string
	closeCalls int
	closeErr   error
}

func (*fakeSession) Input() io.WriteCloser {
	return nopWriteCloser{Writer: io.Discard}
}

func (*fakeSession) Output() io.ReadCloser {
	return io.NopCloser(strings.NewReader(""))
}

func (session *fakeSession) Errors() io.ReadCloser {
	return io.NopCloser(strings.NewReader(session.errors))
}

func (*fakeSession) Wait() error { return nil }

func (session *fakeSession) Close() error {
	session.closeCalls++
	return session.closeErr
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

func validConfig(t *testing.T) Config {
	t.Helper()
	names, err := NamesForRun(testRunID)
	if err != nil {
		t.Fatal(err)
	}
	binding := execution.ProviderBinding{
		IsolationDomainID: "domain-evidence",
		GatewayID:         "gateway-native",
		ID:                "provider-native",
		Name:              names.Provider,
		ResourceVersion:   1,
	}
	provisioned, err := canaryprovider.Provision(
		context.Background(),
		canaryprovider.ProvisionConfig{
			RunID:             testRunID,
			IsolationDomainID: binding.IsolationDomainID,
			GatewayID:         binding.GatewayID,
		},
		fakeProvisioner{binding: binding},
	)
	if err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(t.TempDir(), "evidence")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace, err := canaryworkspace.Open(canaryworkspace.Config{Root: root, RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = workspace.Cleanup(context.Background(), canaryevidence.CleanupRequest{
			RunID: testRunID, ResourceKind: "workspace", ResourceName: names.Workspace,
		})
	})

	return Config{
		RunID:       testRunID,
		Provider:    &openshell.Provider{},
		Provisioned: provisioned,
		Execution: execution.Execution{
			IsolationDomainID: binding.IsolationDomainID,
			ID:                "execution-evidence",
			GatewayID:         binding.GatewayID,
			State:             "ready",
		},
		Workspace: workspace,
		OpenShell: &openshell.CredentialEvidenceSources{},
		Docker:    &canarydocker.Sources{},
		Runtime:   &canaryruntime.Sources{},
	}
}

func structResourceNames(config canaryevidence.Config) canarysource.ResourceNames {
	return canarysource.ResourceNames{
		Gateway:  config.Resources.Gateway,
		Sandbox:  config.Resources.Sandbox,
		Provider: config.Resources.Provider,
		Runtime:  config.Resources.Runtime,
	}
}
