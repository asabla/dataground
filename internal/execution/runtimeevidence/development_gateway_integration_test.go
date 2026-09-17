package runtimeevidence

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDevelopmentGatewayLiveReplayAndRetirement(t *testing.T) {
	testDevelopmentGatewayLive(t, "")
}

func TestDevelopmentGatewayLiveStrictCandidate(t *testing.T) {
	image := os.Getenv("DATAGROUND_TEST_DEVELOPMENT_SUPERVISOR_IMAGE")
	if image == "" {
		t.Skip("DATAGROUND_TEST_DEVELOPMENT_SUPERVISOR_IMAGE is required")
	}
	testDevelopmentGatewayLive(t, image)
}

func testDevelopmentGatewayLive(t *testing.T, supervisor string) {
	t.Helper()
	repository := os.Getenv("DATAGROUND_TEST_RUNTIME_TOPOLOGY_ROOT")
	if repository == "" {
		t.Skip("DATAGROUND_TEST_RUNTIME_TOPOLOGY_ROOT is required")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("local Linux Docker host is required")
	}
	runID, err := newRuntimeLauncherRunID()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if os.Chmod(root, 0o700) != nil {
		t.Fatal("private workspace")
	}
	config := DevelopmentGatewayConfig{IsolationDomainID: "iso_00000000000000000001", ServiceID: "svc_00000000000000000001", RevisionID: "rev_00000000000000000001", RunID: runID, RepositoryRoot: repository, WorkspaceRoot: root, DockerBinary: binary, GatewayConfigSHA256: runtimeGatewayConfigSHA256}
	if supervisor != "" {
		config.SupervisorLocalImageID = supervisor
		gateway, err := os.ReadFile(filepath.Join(repository, runtimeTopologyGatewayPath))
		if err != nil {
			t.Fatal(err)
		}
		config.GatewayConfigSHA256 = runtimeTopologySHA256(bytes.Replace(gateway, []byte(supervisorImage), []byte(supervisor), 1))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	runner := localTopologyDockerRunner{}
	state := &dockerTopologyState{runID: runID, resources: namesForRun(runID), project: "dg_runtime_" + runID, binary: binary, runner: runner, environment: developmentGatewayEnvironment(binary, nil)}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		ids, err := developmentGatewayContainers(cleanup, state)
		if err != nil {
			t.Error("cleanup observation", err)
			return
		}
		for _, id := range ids {
			if state.verifyContainer(cleanup, id) != nil {
				t.Error("cleanup identity mismatch")
				return
			}
			output, err := runner.Run(cleanup, state.environment, binary, "rm", "--force", id)
			clear(output)
			if err != nil {
				t.Error("remove exact test container", err)
			}
		}
	})
	first, err := RunDevelopmentGateway(ctx, "up", config)
	if err != nil {
		t.Fatal("start persistent gateway", err)
	}
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	t.Setenv("DOCKER_CONTEXT", "foreign")
	t.Setenv("DOCKER_CONFIG", "/unavailable")
	for _, action := range []string{"inspect", "up"} {
		replay, err := RunDevelopmentGateway(ctx, action, config)
		if err != nil || replay != first {
			t.Fatal("gateway changed across command replacement", err)
		}
	}
	// Simulate losing only the final publication after the native effect. The
	// retained start intent must recover the same process without another up.
	if os.Remove(filepath.Join(root, "dg-runtime-deployment-"+runID+".receipt.json")) != nil {
		t.Fatal("remove test receipt")
	}
	recovered, err := RunDevelopmentGateway(ctx, "up", config)
	if err != nil || recovered != first {
		t.Fatal("ambiguous start not observed", err)
	}
	for range 2 {
		stopped, err := RunDevelopmentGateway(ctx, "stop", config)
		if err != nil || stopped.State != "stopped" || stopped.ContainerID != first.ContainerID {
			t.Fatal("stop failed", err)
		}
	}
	if _, err := RunDevelopmentGateway(ctx, "up", config); !errors.Is(err, ErrDevelopmentGatewayRetired) {
		t.Fatal("retired deployment restarted")
	}
	for _, name := range []string{"gateway-state", runtimeTopologyJWTDirectory, "gateway.toml"} {
		if _, err := os.Stat(filepath.Join(root, runtimeTopologyDirectoryPrefix+runID, name)); err != nil {
			t.Fatal("stop removed state", err)
		}
	}
}
