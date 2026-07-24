package openshell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
)

type runnerCall struct {
	binary string
	args   []string
	start  bool
}

type scriptedResult struct {
	result CommandResult
	err    error
}

type scriptedRunner struct {
	calls   []runnerCall
	results []scriptedResult
	session execution.RuntimeSession
	runHook func([]string)
}

func (runner *scriptedRunner) Run(_ context.Context, binary string, args ...string) (CommandResult, error) {
	runner.calls = append(runner.calls, runnerCall{binary: binary, args: append([]string(nil), args...)})
	if runner.runHook != nil {
		runner.runHook(args)
	}
	if len(runner.results) == 0 {
		return CommandResult{}, errors.New("unexpected command")
	}
	next := runner.results[0]
	runner.results = runner.results[1:]
	return next.result, next.err
}

func (runner *scriptedRunner) Start(_ context.Context, binary string, args ...string) (execution.RuntimeSession, error) {
	runner.calls = append(runner.calls, runnerCall{binary: binary, args: append([]string(nil), args...), start: true})
	if runner.session == nil {
		return nil, errors.New("unexpected process start")
	}
	return runner.session, nil
}

type inertSession struct{}

func (inertSession) Input() io.WriteCloser { return inertWriteCloser{Writer: io.Discard} }
func (inertSession) Output() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }
func (inertSession) Errors() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }
func (inertSession) Wait() error           { return nil }
func (inertSession) Close() error          { return nil }

type inertWriteCloser struct{ io.Writer }

func (inertWriteCloser) Close() error { return nil }

func TestGatewaySelectionIsDeterministicAndDrainAware(t *testing.T) {
	provider := New(Config{}, &scriptedRunner{})
	context := context.Background()
	for _, id := range []string{"gateway-b", "gateway-a"} {
		_, err := provider.RegisterGateway(context, execution.GatewayRegistration{
			IsolationDomainID: "iso-a", ID: id, Endpoint: "http://127.0.0.1:8080", Driver: "docker",
			Capabilities: []string{"codex.app-server"},
		})
		if err != nil {
			t.Fatalf("register gateway: %v", err)
		}
	}
	first, err := provider.SelectGateway(context, execution.PlacementRequest{
		IsolationDomainID: "iso-a", OperationID: "op-a", RequiredCapabilities: []string{"codex.app-server"},
	})
	if err != nil {
		t.Fatalf("select first gateway: %v", err)
	}
	if first.GatewayID != "gateway-a" {
		t.Fatalf("expected stable gateway-a tie-break, got %q", first.GatewayID)
	}
	if err := provider.SetGatewayState(context, "iso-a", "gateway-b", execution.GatewayDraining); err != nil {
		t.Fatalf("drain gateway: %v", err)
	}
	second, err := provider.SelectGateway(context, execution.PlacementRequest{
		IsolationDomainID: "iso-a", OperationID: "op-b", RequiredCapabilities: []string{"codex.app-server"},
	})
	if err != nil {
		t.Fatalf("select second gateway: %v", err)
	}
	if second.GatewayID != "gateway-a" {
		t.Fatalf("draining gateway accepted new placement: %q", second.GatewayID)
	}
	again, err := provider.SelectGateway(context, execution.PlacementRequest{
		IsolationDomainID: "iso-a", OperationID: "op-a", RequiredCapabilities: []string{"codex.app-server"},
	})
	if err != nil || again != first {
		t.Fatalf("placement was not idempotent: %#v, %v", again, err)
	}
	if _, err := provider.SelectGateway(context, execution.PlacementRequest{
		IsolationDomainID: "iso-b", OperationID: "op-a", RequiredCapabilities: []string{"codex.app-server"},
	}); !errors.Is(err, ErrNoGateway) {
		t.Fatalf("cross-domain gateway selection = %v, want ErrNoGateway", err)
	}
}

func TestGatewayResponseCannotSerializeEndpoint(t *testing.T) {
	provider := New(Config{}, &scriptedRunner{})
	gateway, err := provider.RegisterGateway(context.Background(), execution.GatewayRegistration{
		IsolationDomainID: "iso-a", ID: "gateway-a", Endpoint: "http://127.0.0.1:8080", Driver: "docker",
	})
	if err != nil {
		t.Fatalf("register gateway: %v", err)
	}
	encoded, err := json.Marshal(gateway)
	if err != nil {
		t.Fatalf("marshal gateway: %v", err)
	}
	if strings.Contains(string(encoded), "8080") || strings.Contains(string(encoded), "endpoint") {
		t.Fatalf("gateway endpoint crossed provider boundary: %s", encoded)
	}
}

func TestGatewayRegistrationRejectsSecretBearingOrRemotePlaintextEndpoint(t *testing.T) {
	provider := New(Config{}, &scriptedRunner{})
	for _, endpoint := range []string{
		"http://gateway.example.com:8080",
		"https://token@gateway.example.com",
		"https://gateway.example.com/control?token=secret",
	} {
		_, err := provider.RegisterGateway(context.Background(), execution.GatewayRegistration{
			IsolationDomainID: "iso-a", ID: endpoint, Endpoint: endpoint, Driver: "docker",
		})
		if err == nil {
			t.Fatalf("unsafe gateway endpoint accepted: %q", endpoint)
		}
	}
}

func TestCreateRequiresPinnedImageAndVerifiedPolicy(t *testing.T) {
	provider, _, placement, policy, policyDigest := preparedProvider(t, nil)
	request := createRequest(placement, policy, policyDigest)
	request.Image = "ghcr.io/nvidia/openshell-community/sandboxes/base:latest"
	if _, err := provider.Create(context.Background(), request); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("mutable image accepted: %v", err)
	}
	request = createRequest(placement, policy, "sha256:"+strings.Repeat("0", 64))
	if _, err := provider.Create(context.Background(), request); !errors.Is(err, execution.ErrPolicyInvalid) {
		t.Fatalf("wrong policy digest accepted: %v", err)
	}
}

func TestCreateRequiresManagedPolicyWorkspaceForNewSandbox(t *testing.T) {
	provider, runner, placement, policy, policyDigest := preparedProvider(t, nil)
	provider.workspace = nil
	runner.results = []scriptedResult{{result: CommandResult{Stdout: []byte("[]")}}}
	_, err := provider.Create(context.Background(), createRequest(placement, policy, policyDigest))
	if !errors.Is(err, ErrPolicyWorkspaceUnavailable) {
		t.Fatalf("create without policy workspace = %v, want unavailable", err)
	}
	if len(runner.calls) != 1 || containsSequence(runner.calls[0].args, "sandbox", "create") {
		t.Fatalf("sandbox create reached without policy workspace: %#v", runner.calls)
	}
}

func TestCreateRequestCannotSerializePolicyContent(t *testing.T) {
	_, _, placement, policy, policyDigest := preparedProvider(t, nil)
	encoded, err := json.Marshal(createRequest(placement, policy, policyDigest))
	if err != nil {
		t.Fatalf("marshal create request: %v", err)
	}
	if strings.Contains(string(encoded), "version: 1") || strings.Contains(string(encoded), `"Policy":`) {
		t.Fatalf("enforcement policy serialized across provider boundary: %s", encoded)
	}
}

func TestCreateUsesArgumentVectorAndNoCredentialValues(t *testing.T) {
	var materializedPath string
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("[]")}},
		{result: CommandResult{}},
	}}
	provider, _, placement, policy, policyDigest := preparedProvider(t, runner)
	runner.runHook = func(args []string) {
		for index, argument := range args {
			if argument != "--policy" || index+1 >= len(args) {
				continue
			}
			materializedPath = args[index+1]
			content, err := os.ReadFile(materializedPath)
			if err != nil || !reflect.DeepEqual(content, policy) {
				t.Errorf("materialized policy = %q, %v", content, err)
			}
			info, err := os.Stat(materializedPath)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Errorf("materialized policy mode = %v, %v", info, err)
			}
		}
	}
	request := createRequest(placement, policy, policyDigest)
	request.OperationID = "op-a; echo leaked"
	request.Placement, _ = provider.SelectGateway(context.Background(), execution.PlacementRequest{
		IsolationDomainID: request.IsolationDomainID, OperationID: request.OperationID,
		RequiredCapabilities: []string{"codex.app-server"},
	})
	request.ProviderProfiles = []string{"codex"}
	created, err := provider.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	if created.ID == "" || len(runner.calls) != 2 {
		t.Fatalf("unexpected create result or calls: %#v %#v", created, runner.calls)
	}
	createArgs := runner.calls[1].args
	if !containsSequence(createArgs, "--provider", "codex") || !containsSequence(createArgs, "--", "true") {
		t.Fatalf("missing certified create arguments: %#v", createArgs)
	}
	joined := strings.Join(createArgs, " ")
	if strings.Contains(joined, "echo leaked") || strings.Contains(joined, "API_KEY") || strings.Contains(joined, "ACCESS_TOKEN") {
		t.Fatalf("untrusted operation data or credential material reached argv: %s", joined)
	}
	if runner.calls[1].binary != "openshell" {
		t.Fatalf("unexpected command runner binary: %q", runner.calls[1].binary)
	}
	if materializedPath == "" {
		t.Fatal("provider did not materialize the enforcement policy")
	}
	if filepath.Dir(materializedPath) != provider.workspace.root {
		t.Fatalf("policy escaped managed workspace: %q", materializedPath)
	}
	if _, err := os.Stat(materializedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("materialized policy survived create: %v", err)
	}
}

func TestCreateRejectsUnconfiguredProviderProfileBeforeProviderAccess(t *testing.T) {
	provider, runner, placement, policy, policyDigest := preparedProvider(t, nil)
	request := createRequest(placement, policy, policyDigest)
	request.ProviderProfiles = []string{"unregistered"}
	if _, err := provider.Create(context.Background(), request); !errors.Is(err, execution.ErrProviderProfileUnavailable) {
		t.Fatalf("unregistered profile error = %v, want unavailable", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("provider access reached for unregistered profile: %#v", runner.calls)
	}
}

func TestCreateRejectsUnreservedOrMismatchedPlacement(t *testing.T) {
	provider, _, placement, policy, policyDigest := preparedProvider(t, nil)
	request := createRequest(placement, policy, policyDigest)
	request.OperationID = "different-operation"
	if _, err := provider.Create(context.Background(), request); err == nil || !strings.Contains(err.Error(), "placement") {
		t.Fatalf("mismatched placement accepted: %v", err)
	}
	request = createRequest(execution.Placement{
		IsolationDomainID: "iso-a", ID: derivedID("plc", "iso-a:op-a"), GatewayID: "gateway-b",
	}, policy, policyDigest)
	if _, err := provider.Create(context.Background(), request); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("forged placement accepted: %v", err)
	}
}

func TestCreateObservesAfterLostAcknowledgement(t *testing.T) {
	var materializedPath string
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("[]")}},
		{err: context.DeadlineExceeded},
		{result: CommandResult{Stdout: []byte(`[{
			"name":"dg-e130c9d4ce8cea6ef2d0dfdb","phase":"Ready"
		}]`)}},
	}}
	provider, _, placement, policy, policyDigest := preparedProvider(t, runner)
	runner.runHook = func(args []string) {
		for index, argument := range args {
			if argument == "--policy" && index+1 < len(args) {
				materializedPath = args[index+1]
			}
		}
	}
	request := createRequest(placement, policy, policyDigest)
	expectedName := sandboxName(request.IsolationDomainID, request.OperationID)
	fingerprint := fingerprintCreate(request, request.ProviderProfiles)
	runner.results[2].result.Stdout = []byte(`[{"name":"` + expectedName + `","phase":"Ready","labels":{"` +
		createLabel + `":"` + fingerprint + `"}}]`)
	created, err := provider.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("recover ambiguous create: %v", err)
	}
	if created.State != "ready" || len(runner.calls) != 3 {
		t.Fatalf("create was repeated or not observed: %#v calls=%d", created, len(runner.calls))
	}
	if materializedPath == "" {
		t.Fatal("ambiguous create did not materialize policy")
	}
	if _, err := os.Stat(materializedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("policy survived ambiguous create recovery: %v", err)
	}
}

func TestCreateRecordsSuccessfulEffectWhenPolicyCleanupFails(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("[]")}},
		{result: CommandResult{}},
	}}
	provider, _, placement, policy, policyDigest := preparedProvider(t, runner)
	request := createRequest(placement, policy, policyDigest)
	expectedName := sandboxName(request.IsolationDomainID, request.OperationID)
	fingerprint := fingerprintCreate(request, request.ProviderProfiles)
	runner.runHook = func(args []string) {
		if !containsSequence(args, "sandbox", "create") {
			return
		}
		for index, argument := range args {
			if argument != "--policy" || index+1 >= len(args) {
				continue
			}
			path := args[index+1]
			if err := os.Remove(path); err != nil {
				t.Errorf("replace materialized policy: %v", err)
				return
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Errorf("create cleanup obstruction: %v", err)
				return
			}
			if err := os.WriteFile(filepath.Join(path, "obstruction"), nil, 0o600); err != nil {
				t.Errorf("populate cleanup obstruction: %v", err)
			}
		}
	}
	if _, err := provider.Create(context.Background(), request); !errors.Is(err, ErrPolicyWorkspaceFailure) {
		t.Fatalf("cleanup failure = %v, want workspace failure", err)
	}
	recorded, err := provider.store.GetExecution(context.Background(), execution.ExecutionRef{
		IsolationDomainID: request.IsolationDomainID,
		ID:                derivedID("exe", request.IsolationDomainID+":"+request.OperationID),
	})
	if err != nil || recorded.Execution.State != "provisioning" {
		t.Fatalf("successful external effect was not recorded: %#v, %v", recorded, err)
	}
	runner.results = []scriptedResult{{result: CommandResult{Stdout: []byte(`[{
		"name":"` + expectedName + `","phase":"Ready","labels":{"` + createLabel + `":"` + fingerprint + `"}
	}]`)}}}
	recovered, err := provider.Create(context.Background(), request)
	if err != nil || recovered.State != "ready" {
		t.Fatalf("retry did not observe recorded create: %#v, %v", recovered, err)
	}
	if len(runner.calls) != 3 || containsSequence(runner.calls[2].args, "sandbox", "create") {
		t.Fatalf("cleanup-failure retry repeated create: %#v", runner.calls)
	}
}

func TestCreateRejectsConflictingRetryFingerprint(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("[]")}},
		{result: CommandResult{}},
	}}
	provider, _, placement, policy, policyDigest := preparedProvider(t, runner)
	first := createRequest(placement, policy, policyDigest)
	first.ProviderProfiles = []string{"codex"}
	if _, err := provider.Create(context.Background(), first); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	fingerprint := fingerprintCreate(first, []string{"codex"})
	runner.results = []scriptedResult{{result: CommandResult{Stdout: []byte(`[{"name":"` +
		sandboxName(first.IsolationDomainID, first.OperationID) + `","phase":"Ready","labels":{"` + createLabel +
		`":"` + fingerprint + `"}}]`)}}}
	changed := first
	changed.ProviderProfiles = []string{"other"}
	if _, err := provider.Create(context.Background(), changed); !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("conflicting retry error = %v, want ErrStateConflict", err)
	}
	if len(runner.calls) != 3 || containsSequence(runner.calls[2].args, "sandbox", "create") {
		t.Fatalf("conflicting retry repeated create: %#v", runner.calls)
	}
}

func TestCreateDoesNotRepeatWhenPriorStateCannotBeObserved(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{{err: context.DeadlineExceeded}}}
	provider, _, placement, policy, policyDigest := preparedProvider(t, runner)
	if _, err := provider.Create(context.Background(), createRequest(placement, policy, policyDigest)); !errors.Is(err, ErrProviderFailure) {
		t.Fatalf("unobservable prior state did not fail closed: %v", err)
	}
	if len(runner.calls) != 1 || containsSequence(runner.calls[0].args, "sandbox", "create") {
		t.Fatalf("create was attempted after observation failure: %#v", runner.calls)
	}
}

func TestStartRuntimeKeepsNativeEndpointInsideAdapter(t *testing.T) {
	runner := &scriptedRunner{session: inertSession{}, results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("[]")}},
		{result: CommandResult{}},
	}}
	provider, _, placement, policy, policyDigest := preparedProvider(t, runner)
	created, err := provider.Create(context.Background(), createRequest(placement, policy, policyDigest))
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	ref := execution.ExecutionRef{IsolationDomainID: created.IsolationDomainID, ID: created.ID}
	session, err := provider.StartRuntime(context.Background(), ref)
	if err != nil || session == nil {
		t.Fatalf("start runtime: %v", err)
	}
	call := runner.calls[len(runner.calls)-1]
	if !call.start || !containsSequence(call.args, "--", "codex", "app-server") || !containsSequence(call.args, "--no-tty") {
		t.Fatalf("native stdio transport was not started correctly: %#v", call)
	}
	encoded, err := json.Marshal(created)
	if err != nil {
		t.Fatalf("marshal execution: %v", err)
	}
	if strings.Contains(string(encoded), "127.0.0.1") || strings.Contains(string(encoded), "sandbox") {
		t.Fatalf("provider coordinate leaked through execution: %s", encoded)
	}
}

func TestCheckRequiresCertifiedVersion(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{{result: CommandResult{Stdout: []byte("openshell 0.0.86\n")}}}}
	provider := New(Config{ExpectedVersion: "0.0.86"}, runner)
	if err := provider.Check(context.Background()); err != nil {
		t.Fatalf("certified version rejected: %v", err)
	}
	runner.results = []scriptedResult{{result: CommandResult{Stdout: []byte("openshell 0.0.87\n")}}}
	if err := provider.Check(context.Background()); err == nil {
		t.Fatal("uncertified version accepted")
	}
}

func TestTerminateIsIdempotentAfterProviderConfirmsAbsence(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("[]")}},
		{result: CommandResult{}},
		{err: context.DeadlineExceeded},
		{result: CommandResult{Stdout: []byte("[]")}},
	}}
	provider, _, placement, policy, policyDigest := preparedProvider(t, runner)
	created, err := provider.Create(context.Background(), createRequest(placement, policy, policyDigest))
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	ref := execution.ExecutionRef{IsolationDomainID: created.IsolationDomainID, ID: created.ID}
	if err := provider.Terminate(context.Background(), ref); err != nil {
		t.Fatalf("recover ambiguous termination: %v", err)
	}
	if err := provider.Terminate(context.Background(), ref); err != nil {
		t.Fatalf("repeat termination: %v", err)
	}
}

func TestListOrphansUsesDataGroundIdentityNotSandboxName(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{{result: CommandResult{Stdout: []byte(`[
		{"name":"native-sandbox-a","phase":"Ready","labels":{"dataground.execution":"exe_known"}},
		{"name":"native-sandbox-b","phase":"Ready","labels":{"dataground.execution":"exe_orphan"}},
		{"name":"native-sandbox-c","phase":"Ready","labels":{}}
	]`)}}}}
	provider, _, _, _, _ := preparedProvider(t, runner)
	orphans, err := provider.ListOrphans(context.Background(), "iso-a", "gateway-a", map[string]struct{}{"exe_known": {}})
	if err != nil {
		t.Fatalf("list orphans: %v", err)
	}
	if len(orphans) != 1 || orphans[0].ID != "exe_orphan" {
		t.Fatalf("unexpected orphan identities: %#v", orphans)
	}
	encoded, err := json.Marshal(orphans)
	if err != nil {
		t.Fatalf("marshal orphans: %v", err)
	}
	if strings.Contains(string(encoded), "native-sandbox") {
		t.Fatalf("native sandbox name crossed provider boundary: %s", encoded)
	}
}

func preparedProvider(t *testing.T, runner *scriptedRunner) (*Provider, *scriptedRunner, execution.Placement, []byte, string) {
	t.Helper()
	if runner == nil {
		runner = &scriptedRunner{}
	}
	workspace, err := OpenPolicyWorkspace(filepath.Join(t.TempDir(), "policies"))
	if err != nil {
		t.Fatalf("open policy workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := workspace.Close(); err != nil {
			t.Errorf("close policy workspace: %v", err)
		}
	})
	profiles, err := execution.NewProviderProfileRegistry([]string{"codex", "other"})
	if err != nil {
		t.Fatalf("construct provider profile registry: %v", err)
	}
	provider := New(Config{
		ExpectedVersion: "0.0.86", PolicyWorkspace: workspace, ProviderProfiles: profiles,
		Now: func() time.Time {
			return time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC)
		},
	}, runner)
	context := context.Background()
	_, err = provider.RegisterGateway(context, execution.GatewayRegistration{
		IsolationDomainID: "iso-a", ID: "gateway-a", Endpoint: "http://127.0.0.1:8080", Driver: "docker",
		Capabilities: []string{"codex.app-server"},
	})
	if err != nil {
		t.Fatalf("register gateway: %v", err)
	}
	placement, err := provider.SelectGateway(context, execution.PlacementRequest{
		IsolationDomainID: "iso-a", OperationID: "op-a", RequiredCapabilities: []string{"codex.app-server"},
	})
	if err != nil {
		t.Fatalf("select gateway: %v", err)
	}
	policy := []byte("version: 1\n")
	digest := sha256.Sum256(policy)
	return provider, runner, placement, policy, "sha256:" + hex.EncodeToString(digest[:])
}

func createRequest(placement execution.Placement, policy []byte, policyDigest string) execution.CreateRequest {
	return execution.CreateRequest{
		Placement: placement, IsolationDomainID: "iso-a", OperationID: "op-a",
		Image:        "ghcr.io/nvidia/openshell-community/sandboxes/base@sha256:" + strings.Repeat("a", 64),
		Policy:       append([]byte(nil), policy...),
		PolicyDigest: policyDigest,
	}
}

func containsSequence(items []string, sequence ...string) bool {
	for index := 0; index+len(sequence) <= len(items); index++ {
		if reflect.DeepEqual(items[index:index+len(sequence)], sequence) {
			return true
		}
	}
	return false
}
