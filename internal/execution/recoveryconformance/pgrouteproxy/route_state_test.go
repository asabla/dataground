package pgrouteproxy

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProxyRecoversPersistedRouteAndGenerationFromStaleSocket(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	directory := t.TempDir()
	controlSocket := filepath.Join(directory, "route.sock")
	stateFile := filepath.Join(directory, "state.json")
	proxy, probes := startTestProxy(t, context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
	}, nil)
	probes.set(func(_ context.Context, target string) (Health, error) {
		return Health{Writable: target == promoted, PromotionGeneration: 2}, nil
	})
	if _, err := SelectWritable(context.Background(), controlSocket, 2); err != nil {
		t.Fatal(err)
	}
	if err := proxy.Close(); err != nil {
		t.Fatal(err)
	}

	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: controlSocket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := os.Chmod(controlSocket, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}

	recovered, err := Start(context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		StateFile:      stateFile,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
		HealthProbe: func(_ context.Context, target string) (Health, error) {
			return Health{Writable: target == promoted, PromotionGeneration: 2}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	route, generation, err := StateStatus(context.Background(), controlSocket)
	if err != nil || route != Promoted || generation != 2 {
		t.Fatalf("recovered state = %q %d, error=%v", route, generation, err)
	}
	if got := roundTrip(t, recovered.Address()); got != "promoted" {
		t.Fatalf("recovered route = %q, want promoted", got)
	}
}

func TestProxyRejectsRecoveryWhenPersistedHealthIsStale(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	directory := t.TempDir()
	controlSocket := filepath.Join(directory, "route.sock")
	stateFile := filepath.Join(directory, "state.json")
	proxy, probes := startTestProxy(t, context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
	}, nil)
	probes.set(func(_ context.Context, target string) (Health, error) {
		return Health{Writable: target == promoted, PromotionGeneration: 2}, nil
	})
	if _, err := SelectWritable(context.Background(), controlSocket, 2); err != nil {
		t.Fatal(err)
	}
	if err := proxy.Close(); err != nil {
		t.Fatal(err)
	}
	contentBefore, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}

	failed, err := Start(context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		StateFile:      stateFile,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
		HealthProbe:    primaryHealthProbe(primary),
	})
	if failed != nil {
		_ = failed.Close()
	}
	if err == nil {
		t.Fatal("proxy recovered a stale persisted route")
	}
	contentAfter, readErr := os.ReadFile(stateFile)
	if readErr != nil || string(contentAfter) != string(contentBefore) {
		t.Fatalf("rejected recovery changed state: content=%q error=%v", contentAfter, readErr)
	}
}

func TestSelectionPreservesRouteWhenStateReplacementFails(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	directory := t.TempDir()
	controlSocket := filepath.Join(directory, "route.sock")
	stateFile := filepath.Join(directory, "state.json")
	proxy, probes := startTestProxy(t, context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
	}, nil)
	defer proxy.Close()
	session := establishedSession(t, proxy.Address(), "primary")
	defer session.Close()
	if err := os.Remove(stateFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateFile, 0o700); err != nil {
		t.Fatal(err)
	}
	probes.set(func(_ context.Context, target string) (Health, error) {
		return Health{Writable: target == promoted, PromotionGeneration: 2}, nil
	})
	if _, err := SelectWritable(context.Background(), controlSocket, 2); err == nil {
		t.Fatal("route changed without durable state")
	}
	if route, err := Status(context.Background(), controlSocket); err != nil || route != Primary {
		t.Fatalf("route after failed state replacement = %q, error=%v", route, err)
	}
	if err := session.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(session, "request\n"); err != nil {
		t.Fatal(err)
	}
	if response, err := bufio.NewReader(session).ReadString('\n'); err != nil || response != "primary\n" {
		t.Fatalf("session changed after failed state replacement: response=%q error=%v", response, err)
	}
}

func TestProxyRejectsMalformedOrPermissionWeakenedState(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	for _, test := range []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{name: "malformed", content: "not-json\n", mode: 0o600},
		{name: "unknown field", content: `{"version":1,"primaryTarget":"127.0.0.1:1","promotedTarget":"127.0.0.1:2","route":"primary","promotionGeneration":1,"extra":true}` + "\n", mode: 0o600},
		{name: "weak permissions", content: "{}\n", mode: 0o644},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			controlSocket := filepath.Join(directory, "route.sock")
			stateFile := filepath.Join(directory, "state.json")
			if err := os.WriteFile(stateFile, []byte(test.content), test.mode); err != nil {
				t.Fatal(err)
			}
			proxy, err := Start(context.Background(), Config{
				ListenAddress:  "127.0.0.1:0",
				ControlSocket:  controlSocket,
				StateFile:      stateFile,
				PrimaryTarget:  primary,
				PromotedTarget: promoted,
				HealthProbe:    primaryHealthProbe(primary),
			})
			if proxy != nil {
				_ = proxy.Close()
			}
			if err == nil {
				t.Fatal("proxy accepted unsafe persisted state")
			}
		})
	}
}

func TestProxyRejectsPermissionWeakenedStateWorkspace(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	proxy, err := Start(context.Background(), Config{
		ListenAddress:              "127.0.0.1:0",
		ControlSocket:              filepath.Join(directory, "route.sock"),
		StateFile:                  filepath.Join(directory, "state.json"),
		PrimaryTarget:              primary,
		PromotedTarget:             promoted,
		InitialRoute:               Primary,
		InitialPromotionGeneration: 1,
		HealthProbe:                primaryHealthProbe(primary),
	})
	if proxy != nil {
		_ = proxy.Close()
	}
	if err == nil {
		t.Fatal("proxy accepted a permission-weakened state workspace")
	}
}

func TestProxyRejectsHardLinkedState(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	directory := t.TempDir()
	controlSocket := filepath.Join(directory, "route.sock")
	stateFile := filepath.Join(directory, "state.json")
	proxy, _ := startTestProxy(t, context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
	}, nil)
	if err := proxy.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(stateFile, filepath.Join(directory, "state-alias.json")); err != nil {
		t.Fatal(err)
	}

	recovered, err := Start(context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		StateFile:      stateFile,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
		HealthProbe:    primaryHealthProbe(primary),
	})
	if recovered != nil {
		_ = recovered.Close()
	}
	if err == nil {
		t.Fatal("proxy accepted hard-linked route state")
	}
}

func TestProxyRejectsTargetMismatchedState(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	directory := t.TempDir()
	controlSocket := filepath.Join(directory, "route.sock")
	stateFile := filepath.Join(directory, "state.json")
	content, err := encodeRouteState(persistedRouteState{
		Version:             routeStateVersion,
		PrimaryTarget:       "127.0.0.1:55430",
		PromotedTarget:      promoted,
		Route:               Primary,
		PromotionGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, content, 0o600); err != nil {
		t.Fatal(err)
	}
	proxy, err := Start(context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		StateFile:      stateFile,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
		HealthProbe:    primaryHealthProbe(primary),
	})
	if proxy != nil {
		_ = proxy.Close()
	}
	if err == nil {
		t.Fatal("proxy accepted route state for different targets")
	}
}

func TestProxyHoldsExclusiveStateLock(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	directory := t.TempDir()
	controlSocket := filepath.Join(directory, "route.sock")
	stateFile := filepath.Join(directory, "state.json")
	proxy, _ := startTestProxy(t, context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
	}, nil)
	defer proxy.Close()

	second, err := Start(context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		StateFile:      stateFile,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
		HealthProbe:    primaryHealthProbe(primary),
	})
	if second != nil {
		_ = second.Close()
	}
	if err == nil {
		t.Fatal("second proxy acquired an active route state")
	}
}
