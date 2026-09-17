package runtimeevidence

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDockerTopologyLiveIdentityAndCleanup(t *testing.T) {
	repositoryRoot := os.Getenv("DATAGROUND_TEST_RUNTIME_TOPOLOGY_ROOT")
	if repositoryRoot == "" {
		t.Skip("DATAGROUND_TEST_RUNTIME_TOPOLOGY_ROOT is required for the isolated live Docker topology test")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("the checked host-network Docker topology requires a Linux execution environment")
	}
	runID, err := newRuntimeLauncherRunID()
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	topology, err := NewDockerTopology(DockerTopologyConfig{RunID: runID, RepositoryRoot: repositoryRoot, WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("create checked topology: %v", err)
	}
	t.Cleanup(func() {
		if err := topology.Cleanup(context.Background()); err != nil {
			t.Errorf("remove exact topology: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := topology.Start(ctx); err != nil {
		t.Fatalf("start and verify exact live gateway: %v", err)
	}
	if err := topology.Check(ctx); err != nil {
		t.Fatalf("recheck live gateway: %v", err)
	}
	// A health response alone would also pass with the image's inherited
	// wildcard listener. Check the host socket tables for that regression.
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(content), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 4 || fields[3] != "0A" {
				continue
			}
			address, port, found := strings.Cut(fields[1], ":")
			if found && (port == "1F90" || port == "1F91") && strings.Trim(address, "0") == "" {
				t.Fatal("gateway control or health listener bound to wildcard address")
			}
		}
	}
	state := topology.state
	if _, err := state.runner.Run(ctx, state.environment, state.binary, "pause", state.containerID); err != nil {
		t.Fatal("pause owned test gateway failed")
	}
	checkErr := topology.Check(ctx)
	if _, err := state.runner.Run(ctx, state.environment, state.binary, "unpause", state.containerID); err != nil {
		t.Fatal("unpause owned test gateway failed")
	}
	if !errors.Is(checkErr, ErrDockerTopologyDrift) {
		t.Fatal("paused gateway accepted")
	}
	if topology.Check(ctx) == nil {
		t.Fatal("poisoned topology became ready")
	}
	if err := topology.Cleanup(context.Background()); err != nil {
		t.Fatalf("observe live teardown: %v", err)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("frozen topology workspace remains: count=%d error=%v", len(entries), err)
	}
}
