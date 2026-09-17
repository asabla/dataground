package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution/openshell"
)

func observedTopologyFixture(t *testing.T) (ObservedDockerTopologyConfig, observedTopologyDependencies, *fakeDockerTopologyRunner) {
	t.Helper()
	owner, root := newTestDockerTopology(t, &fakeDockerTopologyRunner{}, func(context.Context) error { return nil })
	config := ObservedDockerTopologyConfig{RunID: testRunID, ContainerID: strings.Repeat("a", 64), StartedAt: "2026-01-01T00:00:00Z", WorkspaceRoot: root, DockerBinary: "/usr/bin/docker", GatewayConfigSHA256: runtimeGatewayConfigSHA256}
	runner := &fakeDockerTopologyRunner{topology: owner.state, results: []dockerTopologyResult{{output: runtimeTopologyInspection(testRunID, config.ContainerID)}}}
	dependencies := observedTopologyDependencies{
		dockerTopologyDependencies: dockerTopologyDependencies{runner: runner, resolveBinary: func(s string) (string, error) { return s, nil }, processIdentity: func() (int, int, int, error) { return 1000, 1000, 999, nil }, checkListeners: func(context.Context, int, string) error { return nil }},
		checkLoadedConfiguration: func(_ context.Context, pid int, workspace *runtimeTopologyWorkspace, digest string, started time.Time) error {
			if pid != 123 || workspace.gatewayPath != owner.state.workspace.gatewayPath || digest != config.GatewayConfigSHA256 || started.Format(time.RFC3339Nano) != config.StartedAt {
				t.Fatal("mounted configuration check lost deployment pins")
			}
			return nil
		},
	}
	return config, dependencies, runner
}
func TestObservedTopologyNeverAcquiresMutationAuthority(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://untrusted:2375")
	t.Setenv("DOCKER_CONTEXT", "remote")
	t.Setenv("DOCKER_CONFIG", "/private/registry")
	t.Setenv("PRIVATE_PROVIDER_TOKEN", "must-not-cross")
	config, dependencies, runner := observedTopologyFixture(t)
	observer, err := newObservedDockerTopology(config, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("constructor contacted native runtime")
	}
	if err := observer.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := len(runner.calls)
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}
	if observer.Check(context.Background()) == nil || len(runner.calls) != before {
		t.Fatal("closed observer contacted deployment")
	}
	if _, err := os.Stat(observer.state.workspace.gatewayPath); err != nil {
		t.Fatal("observer removed configuration")
	}
	for _, call := range runner.calls {
		if call.args[0] != "inspect" && call.args[0] != "image" && call.args[0] != "network" {
			t.Fatal("observation performed a lifecycle operation")
		}
		for _, entry := range call.environment {
			if !strings.HasPrefix(entry, "DATAGROUND_RUNTIME_CONFORMANCE_") && entry != "PATH=/usr/bin:/usr/bin:/bin" {
				t.Fatal("ambient credentials or remote context reached Docker")
			}
		}
	}
	if _, err := json.Marshal(config); err == nil {
		t.Fatal("deployment paths serialized")
	}
	if _, err := json.Marshal(observer); err == nil {
		t.Fatal("deployment observer serialized")
	}
}
func TestObservedTopologyRejectsMissingAndMalformedIndependentPins(t *testing.T) {
	for name, mutate := range map[string]func(*ObservedDockerTopologyConfig){
		"container": func(c *ObservedDockerTopologyConfig) { c.ContainerID = "gateway" },
		"run":       func(c *ObservedDockerTopologyConfig) { c.RunID = "" },
		"start":     func(c *ObservedDockerTopologyConfig) { c.StartedAt = "" },
		"future": func(c *ObservedDockerTopologyConfig) {
			c.StartedAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
		},
		"digest":                 func(c *ObservedDockerTopologyConfig) { c.GatewayConfigSHA256 = strings.Repeat("b", 64) },
		"candidate substitution": func(c *ObservedDockerTopologyConfig) { c.SupervisorLocalImageID = "sha256:" + strings.Repeat("d", 64) },
		"candidate tag":          func(c *ObservedDockerTopologyConfig) { c.SupervisorLocalImageID = "candidate:latest" },
		"root":                   func(c *ObservedDockerTopologyConfig) { c.WorkspaceRoot = "relative" },
		"binary":                 func(c *ObservedDockerTopologyConfig) { c.DockerBinary = "docker" },
	} {
		t.Run(name, func(t *testing.T) {
			config, deps, runner := observedTopologyFixture(t)
			mutate(&config)
			observer, err := newObservedDockerTopology(config, deps)
			if observer != nil {
				_ = observer.Close()
			}
			if err == nil || len(runner.calls) != 0 {
				t.Fatal("invalid pins reached the deployment")
			}
		})
	}
}
func TestObservedTopologyPoisonsDriftAndRetainsExternalDeployment(t *testing.T) {
	for _, mode := range []string{"container replaced", "config changed", "mounted config rewritten", "listeners changed"} {
		t.Run(mode, func(t *testing.T) {
			config, deps, runner := observedTopologyFixture(t)
			observer, err := newObservedDockerTopology(config, deps)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close()
			switch mode {
			case "container replaced":
				runner.results[0].output = runtimeTopologyInspection(testRunID, strings.Repeat("c", 64))
			case "config changed":
				if err := os.WriteFile(observer.state.workspace.gatewayPath, []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "mounted config rewritten":
				observer.state.checkLoadedConfiguration = func(context.Context, int) error { return errors.New("private process information") }
			case "listeners changed":
				observer.state.checkListeners = func(context.Context, int, string) error { return errors.New("private process information") }
			}
			if err := observer.Check(context.Background()); err != ErrDockerTopologyDrift {
				t.Fatal("unsafe deployment observation", err)
			}
			calls := len(runner.calls)
			if observer.Check(context.Background()) == nil || len(runner.calls) != calls {
				t.Fatal("poisoned observation retried")
			}
			_ = observer.Close()
			if _, err := os.Stat(observer.state.workspace.path); err != nil {
				t.Fatal("failed observation removed deployment")
			}
		})
	}
}
func TestObservedTopologyRejectsUnsafeWorkspaceWithoutRemovingIt(t *testing.T) {
	for _, mode := range []string{"symlink", "permissions", "missing"} {
		t.Run(mode, func(t *testing.T) {
			config, deps, _ := observedTopologyFixture(t)
			path := filepath.Join(config.WorkspaceRoot, runtimeTopologyDirectoryPrefix+config.RunID, "gateway.toml")
			switch mode {
			case "symlink":
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".original", path); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if observer, err := newObservedDockerTopology(config, deps); err == nil {
				_ = observer.Close()
				t.Fatal("unsafe workspace accepted")
			}
			if _, err := os.Stat(filepath.Dir(path)); err != nil {
				t.Fatal("constructor removed deployment")
			}
		})
	}
}

func TestObservedCandidateChecksExactSupervisorIdentityOnEveryObservation(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "matching image", true: "substituted image"}[rejected], func(t *testing.T) {
			config, deps, runner := observedTopologyFixture(t)
			config.SupervisorLocalImageID = "sha256:" + strings.Repeat("d", 64)
			path := filepath.Join(config.WorkspaceRoot, runtimeTopologyDirectoryPrefix+config.RunID, "gateway.toml")
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			content = []byte(strings.Replace(string(content), supervisorImage, config.SupervisorLocalImageID, 1))
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			config.GatewayConfigSHA256 = runtimeTopologySHA256(content)
			deps.checkLoadedConfiguration = func(context.Context, int, *runtimeTopologyWorkspace, string, time.Time) error { return nil }
			id := config.SupervisorLocalImageID
			if rejected {
				id = "sha256:" + strings.Repeat("e", 64)
			}
			inspection, _ := json.Marshal(map[string]string{"id": id, "os": "linux", "source": openShellCommit, "patch": openshell.SupervisorCandidatePatchSHA256, "certification": "false"})
			runner.results = append(runner.results, dockerTopologyResult{output: string(inspection)})
			observer, err := newObservedDockerTopology(config, deps)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close()
			err = observer.Check(context.Background())
			if (err != nil) != rejected {
				t.Fatal("incorrect supervisor binding decision", err)
			}
			if !rejected {
				runner.results = []dockerTopologyResult{{output: runtimeTopologyInspection(testRunID, config.ContainerID)}, {err: errors.New("image removed")}}
				if observer.Check(context.Background()) != ErrDockerTopologyDrift {
					t.Fatal("later supervisor identity loss accepted")
				}
			}
		})
	}
}
