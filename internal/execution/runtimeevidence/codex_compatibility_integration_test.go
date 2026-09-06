package runtimeevidence

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

//go:embed testdata/codex-native-compatibility.py
var nativeCompatibilityProbe []byte

// This deliberately produces no runtime-conformance evidence. It compares one
// experimental source build with the retained stock binary without credentials.
func TestCodexNativeSandboxCompatibility(t *testing.T) {
	image := os.Getenv("DATAGROUND_TEST_CODEX_COMPATIBILITY_IMAGE")
	if image == "" {
		t.Skip("DATAGROUND_TEST_CODEX_COMPATIBILITY_IMAGE selects the credential-free candidate test")
	}
	root, binary := os.Getenv("DATAGROUND_TEST_RUNTIME_TOPOLOGY_ROOT"), os.Getenv("DATAGROUND_TEST_OPENSHELL_BINARY")
	if runtime.GOOS != "linux" || root == "" || binary == "" || !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(image) {
		t.Fatal("the candidate test requires Linux, repository root, pinned CLI and exact local image digest")
	}
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	environment := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + workspace, "NO_COLOR=1"}
	run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, command, args...)
		cmd.Env = environment
		cmd.WaitDelay = time.Second
		var output compatibilityOutput
		cmd.Stdout = &output
		err := cmd.Run()
		return output.Bytes(), err
	}
	version, err := run(ctx, binary, "--version")
	if err != nil || strings.TrimSpace(string(version)) != "openshell 0.0.86" {
		t.Fatal("the candidate test requires the pinned OpenShell CLI")
	}
	inspection, err := run(ctx, "docker", "image", "inspect", image, "--format", `{{json .Config.Labels}}`)
	var labels map[string]string
	if err != nil || json.Unmarshal(inspection, &labels) != nil || labels["dataground.dev.codex-compatibility-source"] != "4c70bff480af37b1bf1a9b352b8341060fe55755" || labels["dataground.dev.certification-eligible"] != "false" {
		t.Fatal("the image does not identify the reviewed non-certifying candidate")
	}
	runID, err := newRuntimeLauncherRunID()
	if err != nil {
		t.Fatal(err)
	}
	topology, err := NewDockerTopology(DockerTopologyConfig{RunID: runID, RepositoryRoot: root, WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := topology.Cleanup(context.Background()); err != nil {
			t.Error("candidate gateway cleanup failed")
		}
	})
	if err := topology.Start(ctx); err != nil {
		t.Fatal(err)
	}
	name := "dg-compat-" + runID
	selector := "dataground.dev/compatibility-run=" + runID
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_, _ = run(cleanup, binary, "sandbox", "delete", name, "--gateway-endpoint", gatewayEndpoint)
		data, err := run(cleanup, binary, "sandbox", "list", "--selector", selector, "--output", "json", "--gateway-endpoint", gatewayEndpoint)
		var remaining []json.RawMessage
		if err != nil || json.Unmarshal(data, &remaining) != nil || len(remaining) != 0 {
			t.Error("candidate sandbox cleanup could not be confirmed")
		}
	})
	probe := filepath.Join(workspace, "native-compatibility.py")
	if err := os.WriteFile(probe, nativeCompatibilityProbe, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := run(ctx, binary, "sandbox", "create", "--name", name, "--gateway-endpoint", gatewayEndpoint,
		"--from", image, "--label", selector, "--policy", filepath.Join(root, "deploy/openshell/policies/deny-all.yaml"),
		"--no-auto-providers", "--no-keep", "--no-tty", "--upload", probe+":/tmp/dataground-compatibility.py", "--", "python3", "/tmp/dataground-compatibility.py")
	if err != nil || bytes.Count(output, []byte("DATAGROUND_CODEX_COMPATIBILITY_OK")) != 1 {
		// The probe prints only structural, credential-free test observations.
		for _, line := range bytes.Split(output, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("{")) {
				t.Log(string(line))
			}
		}
		t.Fatal("native sandbox compatibility comparison failed")
	}
}

type compatibilityOutput struct{ bytes.Buffer }

func (output *compatibilityOutput) Write(value []byte) (int, error) {
	if output.Len()+len(value) > 128<<10 {
		return 0, os.ErrInvalid
	}
	return output.Buffer.Write(value)
}
