package runtimeevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func artifactProductionScript(server *codexProbeServer) {
	server.start()
	var thread, turn map[string]any
	if json.Unmarshal(server.threadParams, &thread) != nil || json.Unmarshal(server.turnParams, &turn) != nil {
		panic("invalid artifact production request")
	}
	if thread["approvalPolicy"] != "never" || thread["cwd"] != "/sandbox" ||
		thread["sandbox"] != "workspace-write" ||
		!bytes.Contains(server.turnParams, []byte(runtimeArtifactPath(testRunID))) ||
		!bytes.Contains(server.turnParams, []byte("umask 077")) {
		panic("artifact production weakened its execution boundary")
	}
	artifactEvents(server, true, "completed")
}

func artifactEvents(server *codexProbeServer, command bool, status string) {
	if command {
		for _, method := range []string{"item/started", "item/completed"} {
			server.notify(method, map[string]any{
				"threadId": "native-thread", "turnId": "native-turn",
				"item": map[string]any{"type": "commandExecution"},
			})
		}
	}
	server.notify("turn/completed", map[string]any{
		"threadId": "native-thread",
		"turn":     map[string]any{"id": "native-turn", "status": status, "items": []any{}},
	})
}

func TestArtifactProductionFailurePreventsExport(t *testing.T) {
	for _, name := range []string{"open", "close", "failed", "cancelled", "no-command"} {
		t.Run(name, func(t *testing.T) {
			fixture := newOpenShellProbeFixture()
			privateErr := errors.New("private runtime failure")
			switch name {
			case "open":
				fixture.runtime.openErr = privateErr
			case "close":
				fixture.runtime.closeErr = privateErr
			default:
				fixture.runtime.scripts[0] = func(server *codexProbeServer) {
					server.start()
					status := "completed"
					if name == "failed" {
						status = "failed"
					}
					if name == "cancelled" {
						status = "interrupted"
					}
					artifactEvents(server, name != "no-command", status)
				}
			}
			probes := fixture.open(t)
			request := testProbeRequest()
			if _, err := probes.GatewayReady(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if _, err := probes.SandboxReady(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			result, err := probes.ArtifactExport(context.Background(), request)
			if !errors.Is(err, ErrOpenShellProbeObservation) || errors.Is(err, privateErr) || result.ObservationSHA256 != ([32]byte{}) || fixture.provider.exportCalls != 0 {
				t.Fatalf("failed production reached export or emitted evidence: %v", err)
			}
			if _, err := probes.ArtifactExport(context.Background(), request); !errors.Is(err, ErrOpenShellProbeOrder) {
				t.Fatal("failed production was retried")
			}
		})
	}
}

func TestArtifactProductionUsesLocalMediatedModel(t *testing.T) {
	fixture := newOpenShellProbeFixture()
	fixture.runtime.scripts[0] = func(server *codexProbeServer) {
		server.startWithModel("gpt-6-astra", "dataground_openshell_codex")
		artifactEvents(server, true, "completed")
	}
	config := fixture.config()
	config.diagnosticModel = "gpt-6-astra"
	probes, err := NewOpenShellProbes(config)
	if err != nil {
		t.Fatal(err)
	}
	request := testProbeRequest()
	if _, err := probes.GatewayReady(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := probes.SandboxReady(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := probes.ArtifactExport(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if fixture.runtime.opened != 1 || fixture.provider.exportCalls != 1 {
		t.Fatal("artifact flow did not produce and export exactly once")
	}
}
