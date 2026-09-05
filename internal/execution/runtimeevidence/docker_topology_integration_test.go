package runtimeevidence

import (
	"context"
	"os"
	"runtime"
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
	if err := topology.Cleanup(context.Background()); err != nil {
		t.Fatalf("observe live teardown: %v", err)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("frozen topology workspace remains: count=%d error=%v", len(entries), err)
	}
}
