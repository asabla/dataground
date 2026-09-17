package runtimeevidence

import (
	"bytes"
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

type developmentGatewayFake struct {
	config                                                   DevelopmentGatewayConfig
	calls                                                    []dockerTopologyCall
	id, started                                              string
	exists, running, noEffect, loseUp, loseStop, unavailable bool
	ups, stops                                               int
	onUp                                                     func()
}

func (runner *developmentGatewayFake) Run(_ context.Context, env []string, binary string, args ...string) ([]byte, error) {
	runner.calls = append(runner.calls, dockerTopologyCall{environment: append([]string(nil), env...), binary: binary, args: append([]string(nil), args...)})
	if runner.unavailable {
		return nil, errors.New("private docker failure")
	}
	switch args[0] {
	case "ps":
		if runner.exists {
			return []byte(runner.id + "\n"), nil
		}
		return nil, nil
	case "compose":
		runner.ups++
		if !runner.noEffect {
			runner.exists = true
			runner.running = true
		}
		if runner.onUp != nil {
			runner.onUp()
		}
		if runner.loseUp {
			return nil, errors.New("lost acknowledgement with private payload")
		}
		return nil, nil
	case "stop":
		runner.stops++
		runner.running = false
		if runner.loseStop {
			return nil, errors.New("private stop outcome")
		}
		return nil, nil
	case "network":
		return []byte(testGatewayBridge), nil
	}
	workspacePath := filepath.Join(runner.config.WorkspaceRoot, runtimeTopologyDirectoryPrefix+runner.config.RunID)
	state := &dockerTopologyState{workspace: &runtimeTopologyWorkspace{statePath: filepath.Join(workspacePath, "gateway-state"), gatewayPath: filepath.Join(workspacePath, "gateway.toml"), jwtPath: filepath.Join(workspacePath, runtimeTopologyJWTDirectory)}}
	value, image := validRunningGateway(state, runner.id)
	value.StartedAt = runner.started
	value.Running = runner.running
	if args[0] == "inspect" {
		if !runner.exists {
			return nil, errors.New("missing")
		}
		if args[2] == runningGatewayInspection {
			return json.Marshal(value)
		}
		if args[2] == `{{.State.Running}} {{.State.StartedAt}}` {
			if runner.running {
				return []byte("true " + runner.started), nil
			}
			return []byte("false " + runner.started), nil
		}
		return []byte(runtimeTopologyInspection(runner.config.RunID, runner.id)), nil
	}
	if args[0] == "image" {
		if args[3] == runningGatewayImageInspection {
			return json.Marshal(image)
		}
		if args[4] == openshell.SupervisorCandidateInspectionFormat {
			return []byte(`{"id":"` + runner.config.SupervisorLocalImageID + `","os":"linux","source":"d556748771c41cbbd4e4dd7cd9030c798afe2b7d","patch":"5e97724dd9d9e7fad9abed8a46b9a4d6e06979119998c411daf34b2423056057","certification":"false"}`), nil
		}
	}
	return nil, errors.New("unexpected command")
}

func developmentGatewayFixture(t *testing.T) (DevelopmentGatewayConfig, developmentGatewayDependencies, *developmentGatewayFake) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if os.Chmod(root, 0o700) != nil {
		t.Fatal("private root")
	}
	config := DevelopmentGatewayConfig{IsolationDomainID: "iso_00000000000000000001", ServiceID: "svc_00000000000000000001", RevisionID: "rev_00000000000000000001", RunID: testRunID, RepositoryRoot: runtimeTopologyRepositoryRoot(t), WorkspaceRoot: root, DockerBinary: "/usr/bin/docker", GatewayConfigSHA256: runtimeGatewayConfigSHA256}
	runner := &developmentGatewayFake{config: config, id: strings.Repeat("a", 64), started: "2026-01-01T00:00:00Z"}
	deps := developmentGatewayDependencies{observedTopologyDependencies: observedTopologyDependencies{dockerTopologyDependencies: dockerTopologyDependencies{runner: runner, resolveBinary: func(v string) (string, error) { return v, nil }, processIdentity: func() (int, int, int, error) { return 1000, 1000, 999, nil }, checkListeners: func(context.Context, int, string) error { return nil }, wait: func(context.Context) error { return nil }}, checkLoadedConfiguration: func(context.Context, int, *runtimeTopologyWorkspace, string, time.Time) error { return nil }}, stageWait: func(context.Context) error { return nil }}
	return config, deps, runner
}

func TestDevelopmentGatewaySurvivesCommandExitAndReplaysWithoutMutation(t *testing.T) {
	config, deps, runner := developmentGatewayFixture(t)
	t.Setenv("DOCKER_HOST", "tcp://foreign:2375")
	t.Setenv("HOME", "/private/credentials")
	t.Setenv("TOKEN", "secret")
	first, err := runDevelopmentGateway(context.Background(), "up", config, deps)
	if err != nil || first.ContainerID != runner.id || first.State != "running" {
		t.Fatal(first, err)
	}
	for _, action := range []string{"up", "inspect", "up"} {
		next, err := runDevelopmentGateway(context.Background(), action, config, deps)
		if err != nil || next != first || !runner.running || runner.ups != 1 {
			t.Fatal("replay changed deployment", err)
		}
	}
	for _, call := range runner.calls {
		for _, value := range call.environment {
			if value != "PATH=/usr/bin:/usr/bin:/bin" && !strings.HasPrefix(value, "DATAGROUND_RUNTIME_CONFORMANCE_") {
				t.Fatal("ambient environment reached Docker")
			}
		}
		if call.args[0] == "compose" && strings.Join(call.args, " ") != "compose --project-name dg_runtime_"+config.RunID+" --file "+filepath.Join(config.WorkspaceRoot, runtimeTopologyDirectoryPrefix+config.RunID, "docker-compose.yml")+" up --detach --no-recreate --no-build --pull never gateway" {
			t.Fatal("unsafe Compose mutation")
		}
	}
	path := filepath.Join(config.WorkspaceRoot, runtimeTopologyDirectoryPrefix+config.RunID, "gateway.toml")
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("workspace was removed or exposed")
	}
}

func TestDevelopmentGatewayObservesLostStartAndNeverRepeatsAmbiguousEffect(t *testing.T) {
	for _, effect := range []bool{true, false} {
		t.Run(map[bool]string{true: "effect completed", false: "effect absent"}[effect], func(t *testing.T) {
			config, deps, runner := developmentGatewayFixture(t)
			runner.loseUp = true
			runner.noEffect = !effect
			_, err := runDevelopmentGateway(context.Background(), "up", config, deps)
			if (err == nil) != effect {
				t.Fatal("incorrect ambiguous outcome", err)
			}
			for range 2 {
				_, err = runDevelopmentGateway(context.Background(), "up", config, deps)
				if (err == nil) != effect {
					t.Fatal(err)
				}
			}
			if runner.ups != 1 {
				t.Fatal("ambiguous start repeated")
			}
		})
	}
}

func TestDevelopmentGatewayRecoversReceiptAfterProcessLoss(t *testing.T) {
	config, deps, runner := developmentGatewayFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	runner.onUp = cancel
	if _, err := runDevelopmentGateway(ctx, "up", config, deps); err == nil {
		t.Fatal("cancelled result published")
	}
	if _, err := os.Stat(filepath.Join(config.WorkspaceRoot, "dg-runtime-deployment-"+config.RunID+".receipt.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("receipt committed after cancellation")
	}
	runner.onUp = nil
	if _, err := runDevelopmentGateway(context.Background(), "up", config, deps); err != nil || runner.ups != 1 {
		t.Fatal("lost result not recovered by observation", err)
	}
}

func TestDevelopmentGatewayStopIsTerminalAndPreservesData(t *testing.T) {
	config, deps, runner := developmentGatewayFixture(t)
	if _, err := runDevelopmentGateway(context.Background(), "up", config, deps); err != nil {
		t.Fatal(err)
	}
	runner.loseStop = true
	for range 2 {
		receipt, err := runDevelopmentGateway(context.Background(), "stop", config, deps)
		if err != nil || receipt.State != "stopped" || runner.running || runner.stops != 1 {
			t.Fatal("stop did not observe exact result", err)
		}
	}
	if _, err := runDevelopmentGateway(context.Background(), "up", config, deps); err != ErrDevelopmentGatewayRetired || runner.ups != 1 {
		t.Fatal("retired gateway restarted", err)
	}
	for _, path := range []string{"gateway-state", runtimeTopologyJWTDirectory, "gateway.toml"} {
		if _, err := os.Stat(filepath.Join(config.WorkspaceRoot, runtimeTopologyDirectoryPrefix+config.RunID, path)); err != nil {
			t.Fatal("stop removed retained data")
		}
	}
}

func TestDevelopmentGatewayRejectsCrossScopeSubstitutionAndConcurrentOwner(t *testing.T) {
	config, deps, runner := developmentGatewayFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	runner.onUp = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() { _, err := runDevelopmentGateway(context.Background(), "up", config, deps); done <- err }()
	<-entered
	if _, err := runDevelopmentGateway(context.Background(), "up", config, deps); err != ErrDevelopmentGateway {
		t.Fatal("concurrent owner acquired start")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	before := len(runner.calls)
	for _, mutate := range []func(*DevelopmentGatewayConfig){func(c *DevelopmentGatewayConfig) { c.IsolationDomainID = "iso_00000000000000000002" }, func(c *DevelopmentGatewayConfig) { c.ServiceID = "svc_00000000000000000002" }, func(c *DevelopmentGatewayConfig) { c.RevisionID = "rev_00000000000000000002" }, func(c *DevelopmentGatewayConfig) { c.DockerBinary = "/different/docker" }} {
		other := config
		mutate(&other)
		if _, err := runDevelopmentGateway(context.Background(), "up", other, deps); err != ErrDevelopmentGateway || len(runner.calls) != before {
			t.Fatal("changed request reached Docker")
		}
	}
}

func TestDevelopmentGatewayRejectsRestartReplacementAndCorruptRecords(t *testing.T) {
	for _, mode := range []string{"restart", "replacement", "receipt", "request symlink", "configuration", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			config, deps, runner := developmentGatewayFixture(t)
			if _, err := runDevelopmentGateway(context.Background(), "up", config, deps); err != nil {
				t.Fatal(err)
			}
			prefix := filepath.Join(config.WorkspaceRoot, "dg-runtime-deployment-"+config.RunID)
			switch mode {
			case "restart":
				runner.started = "2026-01-02T00:00:00Z"
			case "replacement":
				runner.id = strings.Repeat("b", 64)
			case "receipt":
				if os.WriteFile(prefix+".receipt.json", []byte("{}"), 0o600) != nil {
					t.Fatal("write")
				}
			case "request symlink":
				content, _ := os.ReadFile(prefix + ".request.json")
				_ = os.Remove(prefix + ".request.json")
				_ = os.WriteFile(prefix+".foreign", content, 0o600)
				if os.Symlink(prefix+".foreign", prefix+".request.json") != nil {
					t.Fatal("symlink")
				}
			case "configuration":
				if os.WriteFile(filepath.Join(config.WorkspaceRoot, runtimeTopologyDirectoryPrefix+config.RunID, "gateway.toml"), []byte("changed"), 0o600) != nil {
					t.Fatal("write")
				}
			case "unavailable":
				runner.unavailable = true
			}
			if _, err := runDevelopmentGateway(context.Background(), "up", config, deps); err != ErrDevelopmentGateway || runner.ups != 1 {
				t.Fatal("drift recovered through mutation", err)
			}
		})
	}
}

func TestDevelopmentGatewayStagesExactStrictCandidate(t *testing.T) {
	config, deps, runner := developmentGatewayFixture(t)
	config.SupervisorLocalImageID = "sha256:" + strings.Repeat("c", 64)
	gateway, err := os.ReadFile(filepath.Join(config.RepositoryRoot, runtimeTopologyGatewayPath))
	if err != nil {
		t.Fatal(err)
	}
	config.GatewayConfigSHA256 = runtimeTopologySHA256(bytes.Replace(gateway, []byte(supervisorImage), []byte(config.SupervisorLocalImageID), 1))
	runner.config = config
	if _, err := runDevelopmentGateway(context.Background(), "up", config, deps); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(config.WorkspaceRoot, runtimeTopologyDirectoryPrefix+config.RunID, "gateway.toml"))
	if err != nil || !bytes.Contains(content, []byte(config.SupervisorLocalImageID)) || runtimeTopologySHA256(content) != config.GatewayConfigSHA256 {
		t.Fatal("candidate binding not staged")
	}
}

func TestDevelopmentGatewayRejectsUnsafeRecordsAndMissingStagedInputs(t *testing.T) {
	for _, mode := range []string{"lock symlink", "request permissions", "partial workspace", "missing workspace", "missing intent"} {
		t.Run(mode, func(t *testing.T) {
			config, deps, runner := developmentGatewayFixture(t)
			prefix := filepath.Join(config.WorkspaceRoot, "dg-runtime-deployment-"+config.RunID)
			workspace := filepath.Join(config.WorkspaceRoot, runtimeTopologyDirectoryPrefix+config.RunID)
			switch mode {
			case "lock symlink":
				if os.WriteFile(prefix+".foreign", nil, 0o600) != nil || os.Symlink(prefix+".foreign", prefix+".lock") != nil {
					t.Fatal("fixture")
				}
			case "partial workspace":
				if os.Mkdir(workspace, 0o700) != nil {
					t.Fatal("fixture")
				}
			default:
				if _, err := runDevelopmentGateway(context.Background(), "up", config, deps); err != nil {
					t.Fatal(err)
				}
				if mode == "request permissions" {
					if os.Chmod(prefix+".request.json", 0o644) != nil {
						t.Fatal("fixture")
					}
				}
				if mode == "missing workspace" {
					if os.RemoveAll(workspace) != nil {
						t.Fatal("fixture")
					}
				}
				if mode == "missing intent" {
					if os.Remove(prefix+".start") != nil {
						t.Fatal("fixture")
					}
				}
			}
			before := len(runner.calls)
			if _, err := runDevelopmentGateway(context.Background(), "up", config, deps); err != ErrDevelopmentGateway || len(runner.calls) != before {
				t.Fatal("unsafe local state reached Docker", err)
			}
			if mode == "missing workspace" {
				if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("missing workspace was recreated after start")
				}
			}
		})
	}
}

func TestDevelopmentGatewayStopDoesNotAffectReplacementOrUnknownState(t *testing.T) {
	for _, mode := range []string{"replacement", "restart", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			config, deps, runner := developmentGatewayFixture(t)
			if _, err := runDevelopmentGateway(context.Background(), "up", config, deps); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "replacement":
				runner.id = strings.Repeat("b", 64)
			case "restart":
				runner.started = "2026-01-02T00:00:00Z"
			case "unavailable":
				runner.unavailable = true
			}
			if _, err := runDevelopmentGateway(context.Background(), "stop", config, deps); err != ErrDevelopmentGateway || runner.stops != 0 || !runner.running {
				t.Fatal("stop affected unverified process", err)
			}
		})
	}
}
