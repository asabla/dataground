package pgrouteproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProxyRoutesNewConnectionsAndClosesOldSessions(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	controlSocket := filepath.Join(t.TempDir(), "route.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxy, err := Start(ctx, Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
		InitialRoute:   Primary,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	if got := roundTrip(t, proxy.Address()); got != "primary" {
		t.Fatalf("initial route = %q, want primary", got)
	}
	oldSession, err := net.Dial("tcp4", proxy.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer oldSession.Close()
	if err := oldSession.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(oldSession, "request\n"); err != nil {
		t.Fatal(err)
	}
	if response, err := bufio.NewReader(oldSession).ReadString('\n'); err != nil || response != "primary\n" {
		t.Fatalf("old session was not established through primary: response=%q error=%v", response, err)
	}
	if err := RouteTo(ctx, controlSocket, Promoted); err != nil {
		t.Fatal(err)
	}
	if got := roundTrip(t, proxy.Address()); got != "promoted" {
		t.Fatalf("promoted route = %q, want promoted", got)
	}
	if err := oldSession.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := oldSession.Read(make([]byte, 1)); err == nil {
		t.Fatal("session established before route change remained open")
	} else if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
		t.Fatal("session established before route change timed out instead of closing")
	}
	route, err := Status(ctx, controlSocket)
	if err != nil {
		t.Fatal(err)
	}
	if route != Promoted {
		t.Fatalf("status = %q, want promoted", route)
	}
}

func TestSelectWritableSwitchesToUniqueHealthyTargetAndClosesOldSessions(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	controlSocket := filepath.Join(t.TempDir(), "route.sock")
	var probeMu sync.Mutex
	probeCalls := 0
	proxy, err := Start(context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
		InitialRoute:   Primary,
		HealthProbe: func(_ context.Context, target string) (Health, error) {
			probeMu.Lock()
			probeCalls++
			probeMu.Unlock()
			if target == primary {
				return Health{}, errors.New("unavailable")
			}
			return Health{Writable: target == promoted, PromotionGeneration: 2}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	oldSession := establishedSession(t, proxy.Address(), "primary")
	defer oldSession.Close()
	selected, err := SelectWritable(context.Background(), controlSocket, 2)
	if err != nil {
		t.Fatal(err)
	}
	if selected != Promoted || roundTrip(t, proxy.Address()) != "promoted" {
		t.Fatalf("selected route = %q, want promoted", selected)
	}
	probeMu.Lock()
	observedProbeCalls := probeCalls
	probeMu.Unlock()
	if observedProbeCalls != confirmationCount*2 {
		t.Fatalf("health probe calls = %d, want %d", observedProbeCalls, confirmationCount*2)
	}
	if err := oldSession.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := oldSession.Read(make([]byte, 1)); err == nil {
		t.Fatal("session established before health selection remained open")
	} else if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
		t.Fatal("session established before health selection timed out instead of closing")
	}
}

func TestSelectWritableRejectsZeroCandidatesWithoutChangingRoute(t *testing.T) {
	testRejectedWritableSelection(t, 2, func(_ context.Context, _ string) (Health, error) {
		return Health{}, nil
	})
}

func TestSelectWritableRejectsMultipleCandidatesWithoutChangingRoute(t *testing.T) {
	testRejectedWritableSelection(t, 2, func(_ context.Context, _ string) (Health, error) {
		return Health{Writable: true, PromotionGeneration: 2}, nil
	})
}

func TestSelectWritableRejectsUnexpectedPromotionGenerationWithoutChangingRoute(t *testing.T) {
	testRejectedWritableSelection(t, 2, func(_ context.Context, _ string) (Health, error) {
		return Health{Writable: true, PromotionGeneration: 1}, nil
	})
}

func TestSelectWritableRejectsCandidateChangeAcrossConfirmations(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	var mu sync.Mutex
	probeCount := map[string]int{}
	testRejectedWritableSelectionWithTargets(t, primary, promoted, 2, func(_ context.Context, target string) (Health, error) {
		mu.Lock()
		probeCount[target]++
		observation := probeCount[target]
		mu.Unlock()
		writable := target == promoted
		if observation > 1 {
			writable = target == primary
		}
		return Health{Writable: writable, PromotionGeneration: 2}, nil
	})
}

func TestSelectWritableRejectsGenerationChangeAcrossConfirmations(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	var mu sync.Mutex
	promotedObservations := 0
	testRejectedWritableSelectionWithTargets(t, primary, promoted, 2, func(_ context.Context, target string) (Health, error) {
		if target == primary {
			return Health{}, nil
		}
		mu.Lock()
		promotedObservations++
		generation := uint64(promotedObservations + 1)
		mu.Unlock()
		return Health{Writable: true, PromotionGeneration: generation}, nil
	})
}

func TestSelectWritableRejectsObservationsAfterConcurrentRouteChanges(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	firstProbe := make(chan struct{})
	releaseProbe := make(chan struct{})
	var first sync.Once
	controlSocket := filepath.Join(t.TempDir(), "route.sock")
	proxy, err := Start(context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
		InitialRoute:   Primary,
		HealthProbe: func(ctx context.Context, target string) (Health, error) {
			if target == primary {
				return Health{}, nil
			}
			first.Do(func() { close(firstProbe) })
			select {
			case <-releaseProbe:
				return Health{Writable: true, PromotionGeneration: 2}, nil
			case <-ctx.Done():
				return Health{}, ctx.Err()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	selectionResult := make(chan error, 1)
	go func() {
		_, selectErr := SelectWritable(context.Background(), controlSocket, 2)
		selectionResult <- selectErr
	}()
	<-firstProbe
	if err := RouteTo(context.Background(), controlSocket, Promoted); err != nil {
		t.Fatal(err)
	}
	if err := RouteTo(context.Background(), controlSocket, Primary); err != nil {
		t.Fatal(err)
	}
	close(releaseProbe)
	if err := <-selectionResult; err == nil {
		t.Fatal("stale writable confirmation overrode concurrent route changes")
	}
	if route, err := Status(context.Background(), controlSocket); err != nil || route != Primary {
		t.Fatalf("route after stale confirmation = %q, error=%v", route, err)
	}
}

func testRejectedWritableSelection(
	t *testing.T,
	expectedGeneration uint64,
	probe HealthProbe,
) {
	t.Helper()
	testRejectedWritableSelectionWithTargets(
		t,
		startReplyServer(t, "primary"),
		startReplyServer(t, "promoted"),
		expectedGeneration,
		probe,
	)
}

func testRejectedWritableSelectionWithTargets(
	t *testing.T,
	primary string,
	promoted string,
	expectedGeneration uint64,
	probe HealthProbe,
) {
	t.Helper()
	controlSocket := filepath.Join(t.TempDir(), "route.sock")
	proxy, err := Start(context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
		InitialRoute:   Primary,
		HealthProbe:    probe,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	session := establishedSession(t, proxy.Address(), "primary")
	defer session.Close()
	if _, err := SelectWritable(context.Background(), controlSocket, expectedGeneration); err == nil {
		t.Fatal("unsafe writable selection succeeded")
	}
	if route, err := Status(context.Background(), controlSocket); err != nil || route != Primary {
		t.Fatalf("route after rejected selection = %q, error=%v", route, err)
	}
	if err := session.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(session, "request\n"); err != nil {
		t.Fatal(err)
	}
	if response, err := bufio.NewReader(session).ReadString('\n'); err != nil || response != "primary\n" {
		t.Fatalf("session changed after rejected selection: response=%q error=%v", response, err)
	}
}

func TestSelectWritableRejectsInvalidPromotionGeneration(t *testing.T) {
	if _, err := SelectWritable(context.Background(), "/tmp/unused.sock", 0); err == nil {
		t.Fatal("zero promotion generation was accepted")
	}
}

func TestParseSelectionCommandRejectsNoncanonicalGeneration(t *testing.T) {
	for _, command := range []string{
		"select\n",
		"select 0\n",
		"select 01\n",
		"select -1\n",
		"select 2 trailing\n",
		"select 18446744073709551616\n",
	} {
		if _, ok := parseSelectionCommand(command); ok {
			t.Fatalf("malformed selection command was accepted: %q", command)
		}
	}
	if generation, ok := parseSelectionCommand("select 2\n"); !ok || generation != 2 {
		t.Fatalf("valid selection command parsed as generation=%d ok=%t", generation, ok)
	}
}

func TestProxyControlSocketIsPrivateAndRemovedOnClose(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	controlSocket := filepath.Join(t.TempDir(), "route.sock")
	proxy, err := Start(context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
		InitialRoute:   Primary,
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(controlSocket)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("control socket mode = %v, want private socket", info.Mode())
	}
	if err := proxy.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(controlSocket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("control socket remained after close: %v", err)
	}
}

func TestProxyRejectsPreexistingControlPathWithoutRemovingIt(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	controlSocket := filepath.Join(t.TempDir(), "route.sock")
	if err := os.WriteFile(controlSocket, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Start(context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
		InitialRoute:   Primary,
	})
	if err == nil {
		t.Fatal("proxy accepted a preexisting control path")
	}
	content, readErr := os.ReadFile(controlSocket)
	if readErr != nil || string(content) != "owned" {
		t.Fatalf("preexisting control path changed: content=%q error=%v", content, readErr)
	}
}

func TestProxyClosePreservesReplacementControlPath(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	controlSocket := filepath.Join(t.TempDir(), "route.sock")
	proxy, err := Start(context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
		InitialRoute:   Primary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(controlSocket); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(controlSocket, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := proxy.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(controlSocket)
	if err != nil || string(content) != "replacement" {
		t.Fatalf("replacement control path changed: content=%q error=%v", content, err)
	}
}

func TestProxyRejectsMalformedControlWithoutChangingRoute(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	controlSocket := filepath.Join(t.TempDir(), "route.sock")
	proxy, err := Start(context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
		InitialRoute:   Primary,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	connection, err := net.Dial("unix", controlSocket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(connection, strings.Repeat("x", maxControlCommandBytes+1)+"\n"); err != nil {
		t.Fatal(err)
	}
	response, err := bufio.NewReader(connection).ReadString('\n')
	_ = connection.Close()
	if err != nil || response != "error\n" {
		t.Fatalf("malformed response = %q, error=%v", response, err)
	}
	route, err := Status(context.Background(), controlSocket)
	if err != nil {
		t.Fatal(err)
	}
	if route != Primary {
		t.Fatalf("route changed after malformed control request: %q", route)
	}
}

func TestProxySurvivesUnavailableTarget(t *testing.T) {
	promoted := startReplyServer(t, "promoted")
	unavailable, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unavailableAddress := unavailable.Addr().String()
	_ = unavailable.Close()
	controlSocket := filepath.Join(t.TempDir(), "route.sock")
	proxy, err := Start(context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		PrimaryTarget:  unavailableAddress,
		PromotedTarget: promoted,
		InitialRoute:   Primary,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	connection, err := net.Dial("tcp4", proxy.Address())
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	_, _ = connection.Write([]byte("request\n"))
	if _, err := connection.Read(make([]byte, 1)); err == nil {
		t.Fatal("unavailable target produced a response")
	}
	_ = connection.Close()
	if err := RouteTo(context.Background(), controlSocket, Promoted); err != nil {
		t.Fatal(err)
	}
	if got := roundTrip(t, proxy.Address()); got != "promoted" {
		t.Fatalf("route after unavailable target = %q, want promoted", got)
	}
}

func TestProxyConcurrentRouteChangesAndConnections(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	controlSocket := filepath.Join(t.TempDir(), "route.sock")
	proxy, err := Start(context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  controlSocket,
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
		InitialRoute:   Primary,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wait sync.WaitGroup
	for index := range 40 {
		wait.Add(2)
		go func(routeIndex int) {
			defer wait.Done()
			route := Primary
			if routeIndex%2 == 0 {
				route = Promoted
			}
			_ = RouteTo(ctx, controlSocket, route)
		}(index)
		go func() {
			defer wait.Done()
			connection, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp4", proxy.Address())
			if dialErr != nil {
				return
			}
			_ = connection.SetDeadline(time.Now().Add(time.Second))
			_, _ = io.WriteString(connection, "request\n")
			_, _ = bufio.NewReader(connection).ReadString('\n')
			_ = connection.Close()
		}()
	}
	wait.Wait()
	if _, err := Status(ctx, controlSocket); err != nil {
		t.Fatal(err)
	}
}

func TestProxyBoundsActiveTrafficConnections(t *testing.T) {
	primary := startReplyServer(t, "primary")
	promoted := startReplyServer(t, "promoted")
	proxy, err := Start(context.Background(), Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  filepath.Join(t.TempDir(), "route.sock"),
		PrimaryTarget:  primary,
		PromotedTarget: promoted,
		InitialRoute:   Primary,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	connections := make([]net.Conn, 0, maxActiveConnections)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for range maxActiveConnections {
		connection, err := net.DialTimeout("tcp4", proxy.Address(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(connection, "request\n"); err != nil {
			t.Fatal(err)
		}
		if response, err := bufio.NewReader(connection).ReadString('\n'); err != nil || response != "primary\n" {
			t.Fatalf("bounded session was not established: response=%q error=%v", response, err)
		}
		connections = append(connections, connection)
	}
	overflow, err := net.DialTimeout("tcp4", proxy.Address(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer overflow.Close()
	if err := overflow.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(overflow, "request\n")
	if _, err := bufio.NewReader(overflow).ReadString('\n'); err == nil {
		t.Fatal("proxy accepted traffic above its active connection bound")
	} else if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
		t.Fatal("traffic above the active connection bound timed out instead of being rejected")
	}
}

func TestStartRejectsUnsafeConfiguration(t *testing.T) {
	valid := Config{
		ListenAddress:  "127.0.0.1:0",
		ControlSocket:  filepath.Join(t.TempDir(), "route.sock"),
		PrimaryTarget:  "127.0.0.1:55432",
		PromotedTarget: "127.0.0.1:55433",
		InitialRoute:   Primary,
	}
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{name: "hostname listener", change: func(config *Config) { config.ListenAddress = "localhost:0" }},
		{name: "wildcard listener", change: func(config *Config) { config.ListenAddress = "0.0.0.0:0" }},
		{name: "remote primary", change: func(config *Config) { config.PrimaryTarget = "192.0.2.1:5432" }},
		{name: "zero target port", change: func(config *Config) { config.PromotedTarget = "127.0.0.1:0" }},
		{name: "same targets", change: func(config *Config) { config.PromotedTarget = config.PrimaryTarget }},
		{name: "relative control", change: func(config *Config) { config.ControlSocket = "route.sock" }},
		{name: "unknown route", change: func(config *Config) { config.InitialRoute = "standby" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.change(&config)
			proxy, err := Start(context.Background(), config)
			if proxy != nil {
				_ = proxy.Close()
			}
			if err == nil {
				t.Fatal("unsafe configuration was accepted")
			}
		})
	}
}

func startReplyServer(t *testing.T, response string) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer connection.Close()
				reader := bufio.NewReader(connection)
				for {
					if _, readErr := reader.ReadString('\n'); readErr != nil {
						return
					}
					if _, writeErr := io.WriteString(connection, response+"\n"); writeErr != nil {
						return
					}
				}
			}()
		}
	}()
	return listener.Addr().String()
}

func establishedSession(t *testing.T, address string, expected string) net.Conn {
	t.Helper()
	connection, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	if _, err := io.WriteString(connection, "request\n"); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	response, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil || strings.TrimSpace(response) != expected {
		_ = connection.Close()
		t.Fatalf("session was not established through %s: response=%q error=%v", expected, response, err)
	}
	return connection
}

func roundTrip(t *testing.T, address string) string {
	t.Helper()
	connection, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(connection, "request\n"); err != nil {
		t.Fatal(err)
	}
	response, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(response)
}
