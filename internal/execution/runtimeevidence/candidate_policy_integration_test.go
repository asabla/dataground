package runtimeevidence

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/openshell"
	"github.com/asabla/dataground/internal/security/canarylauncher"
)

// The provider layer activates OpenShell's proxy filesystem baseline. A test
// without that binding cannot establish that normal runtime children can spawn.
func TestCodexProviderBoundSandboxCompatibility(t *testing.T) {
	image := os.Getenv("DATAGROUND_TEST_CODEX_COMPATIBILITY_IMAGE")
	if image == "" {
		t.Skip("DATAGROUND_TEST_CODEX_COMPATIBILITY_IMAGE selects the synthetic candidate test")
	}
	root, binary := os.Getenv("DATAGROUND_TEST_RUNTIME_TOPOLOGY_ROOT"), os.Getenv("DATAGROUND_TEST_OPENSHELL_BINARY")
	if runtime.GOOS != "linux" || root == "" || binary == "" {
		t.Fatal("Linux, repository root and pinned CLI are required")
	}
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	supervisor := os.Getenv("DATAGROUND_TEST_SUPERVISOR_COMPATIBILITY_IMAGE")
	if err := canarylauncher.CheckCandidate(ctx, canarylauncher.Config{RepositoryRoot: root, WorkspaceRoot: workspace, OpenShellBinary: binary}, image); err != nil {
		t.Fatal("candidate credential scan failed")
	}
	candidates := []bool{false, true}
	if supervisor != "" {
		candidates = []bool{true}
	}
	for _, candidate := range candidates {
		name := "missing-null-device"
		if candidate {
			name = "candidate-policy"
		}
		t.Run(name, func(t *testing.T) {
			runID, err := newRuntimeLauncherRunID()
			if err != nil {
				t.Fatal(err)
			}
			resources := namesForRun(runID)
			topology, err := NewDockerTopology(DockerTopologyConfig{supervisorCandidateImage: supervisor, RunID: runID, RepositoryRoot: root, WorkspaceRoot: workspace})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := topology.Cleanup(context.Background()); err != nil {
					t.Error("gateway cleanup failed")
				}
			})
			if err := topology.Start(ctx); err != nil {
				t.Fatal("gateway startup failed")
			}
			owned, err := newRuntimeLauncherWorkspace(workspace, runID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := owned.Cleanup(context.Background(), CleanupRequest{RunID: runID, ResourceKind: "workspace", ResourceName: resources.Workspace}); err != nil {
					t.Error("workspace cleanup failed")
				}
			})
			port, err := newRuntimeLauncherPorts(binary, runID, owned)
			if err != nil {
				t.Fatal(err)
			}
			ports := port.(*runtimeLauncherPorts)
			if ports.Check(ctx) != nil || ports.Register(ctx, runID) != nil || ports.provider.EnableProviderProfiles(ctx, runtimeIsolationDomain(runID), resources.Gateway) != nil {
				t.Fatal("provider setup failed")
			}
			// These values are synthetic, never copied from the process environment.
			synthetic := []byte("dataground-canary-v1:" + strings.Repeat("a", 43))
			binding, err := NewRuntimeProvider(RuntimeProviderConfig{RunID: runID, Provider: ports.provider, Credentials: execution.RuntimeConformanceCredentials{AccessToken: synthetic, RefreshToken: synthetic, AccountID: synthetic, IDToken: synthetic}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := binding.Cleanup(context.Background(), CleanupRequest{RunID: runID, ResourceKind: "provider", ResourceName: resources.Provider}); err != nil {
					t.Error("synthetic provider cleanup failed")
				}
			})
			if err := binding.Provision(ctx); err != nil {
				t.Fatal("synthetic provider creation failed")
			}
			ref := execution.ExecutionRef{IsolationDomainID: runtimeIsolationDomain(runID), ID: runtimeExecutionID(runID)}
			selection := LauncherConfig{RepositoryRoot: root}
			if candidate {
				selection.candidateImage = image
			}
			if supervisor != "" {
				selection.policyProfile = RosettaRuntimePolicyProfile
			}
			policy, err := readRuntimeLauncherPolicy(selection)
			if err != nil {
				t.Fatal(err)
			}
			if candidate {
				creator, err := NewExecutionCreator(ExecutionCreationConfig{policyProfile: selection.policyProfile, diagnosticImage: image, RunID: runID, Policy: policy, Store: ports.store, Provider: ports.provider})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := creator.Cleanup(context.Background(), CleanupRequest{RunID: runID, ResourceKind: "sandbox", ResourceName: resources.Sandbox}); err != nil {
						t.Error("candidate sandbox cleanup failed")
					}
				})
				if _, err := creator.Create(ctx); err != nil {
					t.Fatal("candidate creation failed")
				}
			} else {
				// Reproduce the old policy directly; the candidate creator must reject it.
				placement, err := ports.provider.SelectGateway(ctx, execution.PlacementRequest{IsolationDomainID: ref.IsolationDomainID, OperationID: runtimeOperationID(runID), RequiredCapabilities: []string{openShellRuntimeCapability}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
					defer stop()
					err := ports.provider.Terminate(cleanup, ref)
					observation, observeErr := ports.provider.Observe(cleanup, ref)
					if err != nil || observeErr != nil || observation.State != "terminated" {
						t.Error("negative-control sandbox cleanup failed")
					}
				})
				if _, err := ports.provider.CreateLocalDiagnostic(ctx, execution.CreateRequest{Placement: placement, IsolationDomainID: ref.IsolationDomainID, OperationID: runtimeOperationID(runID), Image: image, Policy: policy, PolicyDigest: "sha256:" + runtimePolicySHA256, ProviderProfiles: []string{resources.Provider}}); err != nil {
					t.Fatal("negative-control creation failed")
				}
				ready := false
				for i := 0; i < 30; i++ {
					observation, err := ports.provider.Observe(ctx, ref)
					if err != nil {
						t.Fatal(err)
					}
					if observation.State == "ready" {
						ready = true
						break
					}
					time.Sleep(200 * time.Millisecond)
				}
				if !ready {
					t.Fatal("negative-control sandbox was not ready")
				}
			}
			session, err := ports.provider.StartRuntime(ctx, ref)
			if err != nil {
				t.Fatal("native transport failed")
			}
			t.Cleanup(func() {
				if err := session.Close(); err != nil {
					t.Error("native transport cleanup failed")
				}
			})
			go func() { _, _ = io.CopyN(io.Discard, session.Errors(), 128<<10) }()
			rpc := candidatePolicyRPC{t: t, session: session, scanner: bufio.NewScanner(session.Output())}
			rpc.scanner.Buffer(make([]byte, 4096), 8<<20)
			response := rpc.call("initialize", map[string]any{"clientInfo": map[string]any{"name": "dataground-compatibility", "version": "0.1.0"}, "capabilities": map[string]any{"experimentalApi": true}})
			if response.Error != nil {
				t.Fatal("initialization rejected")
			}
			for _, mode := range []string{"readOnly", "workspaceWrite"} {
				sandboxPolicy := map[string]any{"type": mode, "networkAccess": false}
				if mode == "workspaceWrite" {
					sandboxPolicy["writableRoots"] = []string{"/sandbox"}
					sandboxPolicy["excludeTmpdirEnvVar"] = true
					sandboxPolicy["excludeSlashTmp"] = true
				}
				response = rpc.call("command/exec", map[string]any{"command": []string{"python3", "-c", candidatePolicyProbe, "/sandbox/dg-policy-" + runID, "/tmp/dg-policy-" + runID}, "cwd": "/sandbox", "sandboxPolicy": sandboxPolicy, "timeoutMs": 10000, "outputBytesCap": 4096})
				if !candidate {
					var failure struct {
						Message string `json:"message"`
					}
					if json.Unmarshal(response.Error, &failure) != nil || failure.Message != "failed to spawn command: Permission denied (os error 13)" {
						t.Fatal("negative control did not reproduce the device denial")
					}
					continue
				}
				var result struct {
					ExitCode int    `json:"exitCode"`
					Stdout   string `json:"stdout"`
				}
				if response.Error != nil || json.Unmarshal(response.Result, &result) != nil || result.ExitCode != 0 {
					t.Fatal("provider-bound native command failed")
				}
				var observations map[string]string
				if json.Unmarshal([]byte(result.Stdout), &observations) != nil {
					t.Fatal("invalid command observations")
				}
				expected := map[string]string{"workspaceWrite": "denied", "outsideWrite": "denied", "nullRead": "allowed", "nullWrite": "allowed", "zeroRead": "denied", "inetSocket": "denied", "rawSocket": "denied", "userNamespace": "denied"}
				if mode == "workspaceWrite" {
					expected["workspaceWrite"] = "allowed"
				}
				if len(observations) != len(expected) {
					t.Fatal("incomplete command observations")
				}
				for key, want := range expected {
					if observations[key] != want {
						t.Errorf("%s %s: got %q, want %q", mode, key, observations[key], want)
					}
				}
			}
			if candidate {
				verifyCandidateArtifactExport(t, ctx, &rpc, ports.provider, ref, runID)
			}
		})
	}
}

type candidatePolicyResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}
type candidatePolicyRPC struct {
	t       *testing.T
	session execution.RuntimeSession
	scanner *bufio.Scanner
	id      int
}

func (rpc *candidatePolicyRPC) call(method string, params any) candidatePolicyResponse {
	rpc.t.Helper()
	rpc.id++
	message, err := json.Marshal(map[string]any{"id": rpc.id, "method": method, "params": params})
	if err != nil {
		rpc.t.Fatal(err)
	}
	if _, err := rpc.session.Input().Write(append(message, '\n')); err != nil {
		rpc.t.Fatal("native request failed")
	}
	for i := 0; i < 256 && rpc.scanner.Scan(); i++ {
		var response candidatePolicyResponse
		if json.Unmarshal(rpc.scanner.Bytes(), &response) != nil {
			rpc.t.Fatal("invalid native response")
		}
		if response.ID == rpc.id {
			return response
		}
	}
	rpc.t.Fatal("bounded native response missing")
	return candidatePolicyResponse{}
}

const candidatePolicyProbe = `
import ctypes,json,os,socket,sys
results={}
for key,path,flags in [("workspaceWrite",sys.argv[1],os.O_WRONLY|os.O_CREAT|os.O_EXCL),("outsideWrite",sys.argv[2],os.O_WRONLY|os.O_CREAT|os.O_EXCL),("nullRead","/dev/null",os.O_RDONLY),("nullWrite","/dev/null",os.O_WRONLY),("zeroRead","/dev/zero",os.O_RDONLY)]:
 try:
  fd=os.open(path,flags,0o600)
 except OSError as error:results[key]="denied" if error.errno in (1,13,30) else "error"
 else:
  os.close(fd);results[key]="allowed"
  if key in ("workspaceWrite","outsideWrite"):os.unlink(path)
for key,family,kind in [("inetSocket",socket.AF_INET,socket.SOCK_STREAM),("rawSocket",socket.AF_PACKET,socket.SOCK_RAW)]:
 try:
  value=socket.socket(family,kind);value.close();results[key]="allowed"
 except OSError as error:results[key]="denied" if error.errno in (1,13) else "error"
lib=ctypes.CDLL(None,use_errno=True)
value=lib.unshare(0x10000000)
results["userNamespace"]="denied" if value==-1 and ctypes.get_errno() in (1,13) else "allowed"
print(json.dumps(results))
`

func verifyCandidateArtifactExport(t *testing.T, ctx context.Context, rpc *candidatePolicyRPC, provider *openshell.Provider, ref execution.ExecutionRef, runID string) {
	t.Helper()
	response := rpc.call("command/exec", map[string]any{
		"command": []string{"python3", "-c", "import os,sys; fd=os.open(sys.argv[1],os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600); os.write(fd,sys.argv[2].encode()); os.close(fd)", runtimeArtifactPath(runID), string(runtimeArtifactContent(runID))},
		"cwd":     "/sandbox", "sandboxPolicy": map[string]any{"type": "workspaceWrite", "writableRoots": []string{"/sandbox"}, "networkAccess": false}, "timeoutMs": 10000, "outputBytesCap": 4096,
	})
	var result struct {
		ExitCode int `json:"exitCode"`
	}
	if response.Error != nil || json.Unmarshal(response.Result, &result) != nil || result.ExitCode != 0 {
		t.Fatal("native artifact production failed")
	}
	exported, err := provider.Export(ctx, execution.ExportRequest{IsolationDomainID: ref.IsolationDomainID, ExecutionID: ref.ID, SandboxPath: runtimeArtifactPath(runID)})
	defer clear(exported.Content)
	if err != nil {
		t.Fatalf("synthetic artifact export failed: %v", err)
	}
	if exported.IsolationDomainID != ref.IsolationDomainID || exported.ExecutionID != ref.ID || !bytes.Equal(exported.Content, runtimeArtifactContent(runID)) {
		t.Fatal("artifact export substituted content or scope")
	}
}
