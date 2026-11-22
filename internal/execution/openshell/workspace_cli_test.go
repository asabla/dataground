package openshell

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"golang.org/x/crypto/ssh"
)

func TestWorkspaceCLIRejectsIncompleteOrAmbiguousLists(t *testing.T) {
	const first = `{"sandboxes":[{"id":"id-1","name":"name-1","workspace":"default"}],"next_page_token":"second"}`
	cases := []struct {
		name string
		page string
	}{
		{"old wire shape", `[]`},
		{"missing cursor", `{"sandboxes":[]}`},
		{"wrong workspace", `{"sandboxes":[{"id":"id-2","name":"name-2","workspace":"wrong"}],"next_page_token":""}`},
		{"repeated cursor", `{"sandboxes":[],"next_page_token":"second"}`},
		{"repeated name", `{"sandboxes":[{"id":"id-2","name":"name-1","workspace":"default"}],"next_page_token":""}`},
		{"repeated id", `{"sandboxes":[{"id":"id-1","name":"name-2","workspace":"default"}],"next_page_token":""}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			runner := &scriptedRunner{results: []scriptedResult{
				{result: CommandResult{Stdout: []byte(first)}},
				{result: CommandResult{Stdout: []byte(tt.page)}},
			}}
			provider := New(Config{ExpectedVersion: WorkspaceCLIVersion}, runner)
			if rows, err := provider.listManagedSandboxes(t.Context(), runtimeSSHGatewayEndpoint); err == nil || rows != nil {
				t.Fatal("incomplete or conflicting workspace listing was accepted")
			}
			if len(runner.calls) != 2 || !slices.Contains(runner.calls[1].args, "--page-token=second") {
				t.Fatal("second page was not requested through the opaque token")
			}
		})
	}
}

func TestWorkspaceCLIRuntimeVerifiesGatewayAndSandboxBeforeOpeningProxy(t *testing.T) {
	for _, phase := range []string{"Ready", "Stopped"} {
		t.Run(phase, func(t *testing.T) {
			runner := &scriptedRunner{}
			provider, ref, record := readyWorkspaceCLITarget(t, runner)
			row := sandboxView{
				workspaceResource: workspaceResource{ID: "sandbox-uuid", Name: record.SandboxName, Workspace: localWorkspace},
				Phase:             phase, Labels: map[string]string{
					managedLabel: "true", isolationLabel: shortDigest(ref.IsolationDomainID),
					executionLabel: ref.ID, operationLabel: shortDigest(record.OperationID),
				},
			}
			raw, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			runner.results = []scriptedResult{
				{result: CommandResult{Stdout: []byte("openshell " + WorkspaceCLIVersion + "\n")}},
				{result: CommandResult{Stdout: []byte(`{"server":"http://127.0.0.1:8080","status":"connected","version":"0.1.9"}`)}},
				{result: CommandResult{Stdout: raw}},
			}
			if phase == "Ready" {
				runner.session = newSSHTestProxy(t, func(channel ssh.Channel) {
					_, _ = channel.Write([]byte("ready"))
					finishSSHTest(channel, 0)
				})
			}
			session, err := provider.StartRuntime(t.Context(), ref)
			if phase == "Stopped" {
				if err == nil || session != nil || len(runner.calls) != 3 {
					t.Fatal("stopped workspace was allowed to start a native runtime")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			if len(runner.calls) != 4 || !slices.Equal(runner.calls[3].args, []string{
				"ssh-proxy", "--server", runtimeSSHGatewayEndpoint, "--gateway-name", record.Execution.GatewayID,
				"--name", record.SandboxName, "--workspace", localWorkspace,
			}) {
				t.Fatal("proxy route was not scoped to the exact workspace, gateway and sandbox")
			}
			output, err := io.ReadAll(session.Output())
			if err != nil || string(output) != "ready" || session.Wait() != nil {
				t.Fatal("scoped proxy did not return runtime output and terminal status")
			}
		})
	}
}

func readyWorkspaceCLITarget(t *testing.T, runner Runner) (*Provider, execution.ExecutionRef, execution.ExecutionRecord) {
	t.Helper()
	const isolationID, gatewayID, operationID = "iso-workspace", "dg-local-release", "op-workspace"
	provider := New(Config{ExpectedVersion: WorkspaceCLIVersion}, runner)
	placement := registerWorkspaceGateway(t, t.Context(), provider, isolationID, gatewayID, operationID)
	value := execution.Execution{
		IsolationDomainID: isolationID, ID: derivedID("exe", isolationID+":"+operationID),
		GatewayID: gatewayID, State: "ready",
	}
	name := provider.sandboxName(isolationID, operationID)
	if err := provider.rememberExecution(t.Context(), value, placement.ID, operationID, name); err != nil {
		t.Fatal(err)
	}
	ref := execution.ExecutionRef{IsolationDomainID: value.IsolationDomainID, ID: value.ID}
	entry, err := provider.store.GetExecution(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return provider, ref, entry
}

func registerWorkspaceGateway(
	t *testing.T, ctx context.Context, provider *Provider, isolationID, gatewayID, operationID string,
) execution.Placement {
	t.Helper()
	if _, err := provider.RegisterGateway(ctx, execution.GatewayRegistration{
		IsolationDomainID: isolationID, ID: gatewayID, Endpoint: runtimeSSHGatewayEndpoint, Driver: "docker",
		Capabilities: []string{"codex.app-server"},
	}); err != nil {
		t.Fatal(err)
	}
	placement, err := provider.SelectGateway(ctx, execution.PlacementRequest{
		IsolationDomainID: isolationID, OperationID: operationID, RequiredCapabilities: []string{"codex.app-server"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return placement
}

func TestWorkspaceCLILiveLifecycle(t *testing.T) {
	root := os.Getenv("DATAGROUND_TEST_OPENSHELL_RELEASE_ROOT")
	if root == "" {
		t.Skip("requires the isolated OpenShell local release gateway")
	}
	bindingName := os.Getenv("DATAGROUND_TEST_OPENSHELL_PROVIDER")
	image := os.Getenv("DATAGROUND_TEST_OPENSHELL_WORKLOAD")
	if bindingName == "" || image == "" {
		t.Fatal("the synthetic release fixture did not supply a provider and workload")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	workspace, err := OpenPolicyWorkspace(filepath.Join(t.TempDir(), "policies"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := workspace.Close(); err != nil {
			t.Error(err)
		}
	}()
	provider := New(Config{ExpectedVersion: WorkspaceCLIVersion, PolicyWorkspace: workspace}, nil)
	const isolationID, gatewayID, operationID = "iso-release-test", "dataground-local-v019", "op-release-test"
	placement := registerWorkspaceGateway(t, ctx, provider, isolationID, gatewayID, operationID)
	if err := provider.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := provider.EnableProviderProfiles(ctx, isolationID, gatewayID); err != nil {
		t.Fatal(err)
	}
	binding, exists, _, err := provider.observeProviderBindingName(ctx, isolationID, gatewayID, bindingName)
	if err != nil || !exists || !slices.Equal(binding.CredentialKeys, []string{"CHATGPT_ACCOUNT_ID", "OPENAI_API_KEY"}) {
		t.Fatal("synthetic provider was not observed through the workspace adapter")
	}
	policy, err := os.ReadFile(filepath.Join(root, "deploy/openshell/policies/deny-all.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(policy)
	ref := execution.ExecutionRef{IsolationDomainID: isolationID, ID: derivedID("exe", isolationID+":"+operationID)}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := provider.Terminate(cleanup, ref); err != nil {
			t.Errorf("sandbox cleanup: %v", err)
		}
	}()
	request := execution.CreateRequest{
		IsolationDomainID: isolationID, OperationID: operationID, Placement: placement,
		Image: image, Policy: policy, PolicyDigest: "sha256:" + hex.EncodeToString(sum[:]),
	}
	created, err := provider.Create(ctx, request)
	if err != nil || created.ID != ref.ID {
		t.Fatalf("sandbox creation (%s): %v", NativeFailureClassOf(err), err)
	}
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	for {
		observation, err := provider.Observe(ctx, ref)
		if err != nil || (observation.State != "provisioning" && observation.State != "ready") {
			t.Fatalf("sandbox observation: state=%s error=%v", observation.State, err)
		}
		if observation.State == "ready" {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("sandbox did not become ready")
		case <-time.After(time.Second):
		}
	}
	replayed, err := provider.Create(ctx, request)
	if err != nil || replayed.ID != ref.ID || replayed.State != "ready" {
		t.Fatalf("sandbox replay: %v", err)
	}
	session, err := provider.startSandboxCommand(ctx, ref, "/bin/cat")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	input := strings.NewReader("workspace runtime round trip\n")
	if _, err := io.Copy(session.Input(), input); err != nil {
		t.Fatal(err)
	}
	if err := session.Input().Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(io.LimitReader(session.Output(), 1024))
	if err != nil || !bytes.Equal(output, []byte("workspace runtime round trip\n")) || session.Wait() != nil {
		t.Fatal("OpenShell proxy did not complete the full stdio exchange")
	}
	if err := provider.Terminate(ctx, ref); err != nil {
		t.Fatal(err)
	}
	final, err := provider.Observe(ctx, ref)
	if err != nil || final.State != "terminated" {
		t.Fatal("released OpenShell sandbox did not terminate")
	}
}
