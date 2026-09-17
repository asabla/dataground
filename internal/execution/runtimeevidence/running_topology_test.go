package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func validRunningGateway(state *dockerTopologyState, id string) (runningGateway, runningGatewayImage) {
	image := runningGatewayImage{ID: "sha256:" + strings.Repeat("b", 64), OS: "linux", RepoDigests: []string{gatewayImage}, Entrypoint: []string{"/gateway"}, Env: []string{"PATH=/usr/bin:/bin"}}
	return runningGateway{
		ID: id, Image: gatewayImage, ImageID: image.ID, StartedAt: "2026-01-01T00:00:00Z", User: "1000:1000", Network: "host", Running: true, PID: 123,
		Cmd: []string{"--config", "/etc/openshell/gateway.toml"}, Entrypoint: image.Entrypoint, GroupAdd: []string{"999"}, Env: []string{"PATH=/usr/bin:/bin", "OPENSHELL_GATEWAY_CONFIG=/etc/openshell/gateway.toml", "OPENSHELL_DB_URL=sqlite:" + state.workspace.statePath + "/gateway.db?mode=rwc", "HOME=" + state.workspace.statePath, "XDG_DATA_HOME=" + state.workspace.statePath},
		Mounts: []runningGatewayMount{
			{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock", RW: true},
			{Type: "bind", Source: state.workspace.statePath, Destination: state.workspace.statePath, RW: true},
			{Type: "bind", Source: state.workspace.jwtPath, Destination: "/run/dataground-runtime-conformance/gateway-jwt"},
			{Type: "bind", Source: state.workspace.gatewayPath, Destination: "/etc/openshell/gateway.toml"},
		},
	}, image
}
func TestRunningGatewayRejectsRuntimeConfigurationDrift(t *testing.T) {
	id := strings.Repeat("a", 64)
	topology, _ := newTestDockerTopology(t, &fakeDockerTopologyRunner{}, func(context.Context) error { return nil })
	value, image := validRunningGateway(topology.state, id)
	if err := topology.state.validateRunningConfiguration(id, value, image); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*runningGateway, *runningGatewayImage){
		"container":      func(v *runningGateway, _ *runningGatewayImage) { v.ID = strings.Repeat("c", 64) },
		"image tag":      func(v *runningGateway, _ *runningGatewayImage) { v.Image = "gateway:latest" },
		"image identity": func(v *runningGateway, _ *runningGatewayImage) { v.ImageID = "sha256:" + strings.Repeat("c", 64) },
		"unpinned image": func(_ *runningGateway, i *runningGatewayImage) { i.RepoDigests = nil },
		"image OS":       func(_ *runningGateway, i *runningGatewayImage) { i.OS = "windows" },
		"stopped":        func(v *runningGateway, _ *runningGatewayImage) { v.Running = false },
		"paused":         func(v *runningGateway, _ *runningGatewayImage) { v.Paused = true },
		"restarting":     func(v *runningGateway, _ *runningGatewayImage) { v.Restarting = true },
		"dead":           func(v *runningGateway, _ *runningGatewayImage) { v.Dead = true },
		"missing pid":    func(v *runningGateway, _ *runningGatewayImage) { v.PID = 0 },
		"start time":     func(v *runningGateway, _ *runningGatewayImage) { v.StartedAt = "invalid" },
		"future start": func(v *runningGateway, _ *runningGatewayImage) {
			v.StartedAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
		},
		"network":           func(v *runningGateway, _ *runningGatewayImage) { v.Network = "bridge" },
		"root":              func(v *runningGateway, _ *runningGatewayImage) { v.User = "0:0" },
		"extra group":       func(v *runningGateway, _ *runningGatewayImage) { v.GroupAdd = append(v.GroupAdd, "0") },
		"privileged":        func(v *runningGateway, _ *runningGatewayImage) { v.Privileged = true },
		"host PID":          func(v *runningGateway, _ *runningGatewayImage) { v.PIDMode = "host" },
		"user namespace":    func(v *runningGateway, _ *runningGatewayImage) { v.UsernsMode = "host" },
		"capability":        func(v *runningGateway, _ *runningGatewayImage) { v.CapAdd = []string{"SYS_ADMIN"} },
		"security override": func(v *runningGateway, _ *runningGatewayImage) { v.SecurityOpt = []string{"seccomp=unconfined"} },
		"entrypoint":        func(v *runningGateway, _ *runningGatewayImage) { v.Entrypoint = []string{"/other"} },
		"command":           func(v *runningGateway, _ *runningGatewayImage) { v.Cmd = []string{"--unsafe"} },
		"extra env":         func(v *runningGateway, _ *runningGatewayImage) { v.Env = append(v.Env, "PRIVATE_TOKEN=secret") },
		"duplicate env":     func(v *runningGateway, _ *runningGatewayImage) { v.Env = append(v.Env, v.Env[0]) },
		"config override":   func(v *runningGateway, _ *runningGatewayImage) { v.Env[1] = "OPENSHELL_GATEWAY_CONFIG=/other" },
		"extra mount": func(v *runningGateway, _ *runningGatewayImage) {
			v.Mounts = append(v.Mounts, runningGatewayMount{Destination: "/extra"})
		},
		"writable config": func(v *runningGateway, _ *runningGatewayImage) { v.Mounts[3].RW = true },
		"config source":   func(v *runningGateway, _ *runningGatewayImage) { v.Mounts[3].Source = "/other" },
		"config volume":   func(v *runningGateway, _ *runningGatewayImage) { v.Mounts[3].Type = "volume" },
		"duplicate mount": func(v *runningGateway, _ *runningGatewayImage) { v.Mounts[3] = v.Mounts[2] },
	} {
		t.Run(name, func(t *testing.T) {
			value, image := validRunningGateway(topology.state, id)
			mutate(&value, &image)
			if !errors.Is(topology.state.validateRunningConfiguration(id, value, image), ErrDockerTopologyDrift) {
				t.Fatal("configuration drift accepted")
			}
		})
	}
	topology.state.startedAt = "2026-01-02T00:00:00Z"
	if topology.state.validateRunningConfiguration(id, value, image) == nil {
		t.Fatal("gateway restart accepted")
	}
}
func TestTopologyCheckReobservesIdentityAndPoisonsDriftWithoutRestart(t *testing.T) {
	id := strings.Repeat("a", 64)
	runner := &fakeDockerTopologyRunner{results: []dockerTopologyResult{{}, {output: id}, {output: runtimeTopologyInspection(testRunID, id)}, {output: id}, {output: runtimeTopologyInspection(testRunID, id)}, {output: strings.Repeat("c", 64)}, {}, {}, {}}}
	topology, _ := newTestDockerTopology(t, runner, func(context.Context) error { return nil })
	if topology.Check(context.Background()) == nil {
		t.Fatal("unstarted topology accepted")
	}
	if err := topology.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := topology.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(topology.Check(context.Background()), ErrDockerTopologyDrift) {
		t.Fatal("replacement accepted")
	}
	calls := len(runner.calls)
	if topology.Check(context.Background()) == nil || len(runner.calls) != calls {
		t.Fatal("poisoned topology retried")
	}
	if err := topology.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range runner.calls[5:] {
		if len(call.args) > 5 && call.args[5] == "up" {
			t.Fatal("readiness restarted gateway")
		}
	}
}
func TestTopologyRejectsModifiedOrReplacedFrozenConfiguration(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "modified", true: "replaced"}[replacement], func(t *testing.T) {
			topology, _ := newTestDockerTopology(t, &fakeDockerTopologyRunner{}, func(context.Context) error { return nil })
			state := topology.state
			if err := state.verifyFrozenTopology(); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(state.workspace.gatewayPath)
			if err != nil {
				t.Fatal(err)
			}
			if replacement {
				if err := os.Rename(state.workspace.gatewayPath, state.workspace.gatewayPath+".old"); err != nil {
					t.Fatal(err)
				}
			} else {
				original = append(original, []byte("\n# drift\n")...)
			}
			if err := os.WriteFile(state.workspace.gatewayPath, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(state.verifyFrozenTopology(), ErrDockerTopologyDrift) {
				t.Fatal("frozen input drift accepted")
			}
		})
	}
}
func TestTopologyWithholdsMalformedInspectionAndCommandDetails(t *testing.T) {
	for _, content := range []string{"", "{}{}", strings.Repeat("x", runtimeTopologyMaxOutputBytes+1), `{"unexpected":"private token"}`} {
		var value runningGateway
		if err := decodeTopologyInspection([]byte(content), &value); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("malformed output accepted or disclosed")
		}
	}
	topology, _ := newTestDockerTopology(t, &fakeDockerTopologyRunner{}, func(context.Context) error { return nil })
	topology.state.runner = &fakeDockerTopologyRunner{results: []dockerTopologyResult{{err: errors.New("private token")}}}
	if _, err := topology.state.verifyRunningConfiguration(context.Background(), strings.Repeat("a", 64)); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("native command details disclosed")
	}
}
func TestLauncherWithholdsResultsWhenFinalTopologyCheckFails(t *testing.T) {
	fixture := newLauncherFixture()
	fixture.topology.checkErr = errors.New("private topology state")
	result, err := launch(context.Background(), fixture.config(), fixture.dependencies())
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("drift did not withhold result safely")
	}
	if _, err := json.Marshal(result); err == nil {
		t.Fatal("failed run emitted evidence")
	}
	if fixture.events[len(fixture.events)-1] != "topology-cleanup" {
		t.Fatal("drift skipped cleanup")
	}
}

func TestFrozenTopologyDriftStopsBeforeComposeStart(t *testing.T) {
	runner := &fakeDockerTopologyRunner{}
	topology, _ := newTestDockerTopology(t, runner, func(context.Context) error { return nil })
	if err := os.WriteFile(topology.state.workspace.gatewayPath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(topology.Start(context.Background()), ErrDockerTopologyDrift) || len(runner.calls) != 0 {
		t.Fatal("changed input reached native startup")
	}
}
func TestRunningTopologyRequiresEveryInspectionField(t *testing.T) {
	topology, _ := newTestDockerTopology(t, &fakeDockerTopologyRunner{}, func(context.Context) error { return nil })
	value, image := validRunningGateway(topology.state, strings.Repeat("a", 64))
	for _, source := range []any{value, image} {
		encoded, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		for key, original := range fields {
			delete(fields, key)
			content, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			var target any = new(runningGateway)
			if _, ok := source.(runningGatewayImage); ok {
				target = new(runningGatewayImage)
			}
			if decodeTopologyInspection(content, target) == nil {
				t.Fatalf("missing %s accepted", key)
			}
			fields[key] = original
		}
	}
}

func TestFrozenTopologyRejectsDirectoryReplacementWithOriginalFiles(t *testing.T) {
	topology, _ := newTestDockerTopology(t, &fakeDockerTopologyRunner{}, func(context.Context) error { return nil })
	workspace := topology.state.workspace
	moved := workspace.path + "-replaced"
	if err := os.Rename(workspace.path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workspace.path, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docker-compose.yml", "gateway.toml", "gateway-state", "gateway-jwt"} {
		if err := os.Rename(moved+"/"+name, workspace.path+"/"+name); err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(topology.state.verifyFrozenTopology(), ErrDockerTopologyDrift) {
		t.Fatal("directory replacement accepted")
	}
}
