// Package pgrouteproxy provides a bounded, loopback-only TCP routing proxy for
// PostgreSQL failover conformance. Route changes are explicit or selected from
// predeclared targets by caller-triggered, generation-bound health confirmation,
// and invalidate existing sessions; the proxy never promotes, elects, fences,
// or replays traffic.
package pgrouteproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxControlCommandBytes = 64
	maxActiveConnections   = 64
	maxControlConnections  = 8
	controlDeadline        = 9 * time.Second
	dialTimeout            = 3 * time.Second
	selectionTimeout       = 7 * time.Second
	probeRoundTimeout      = 2 * time.Second
	confirmationInterval   = 200 * time.Millisecond
	confirmationCount      = 3
)

type Route string

const (
	Primary  Route = "primary"
	Promoted Route = "promoted"
)

type Config struct {
	ListenAddress  string
	ControlSocket  string
	PrimaryTarget  string
	PromotedTarget string
	InitialRoute   Route
	HealthProbe    HealthProbe
}

type Health struct {
	Writable            bool
	PromotionGeneration uint64
}

type HealthProbe func(context.Context, string) (Health, error)

type Proxy struct {
	listener *net.TCPListener
	control  *net.UnixListener
	routes   map[Route]string
	probe    HealthProbe

	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	route      Route
	generation uint64
	active     map[*connectionPair]struct{}
	traffic    chan struct{}
	controls   chan struct{}
	wait       sync.WaitGroup
	closeOnce  sync.Once
	closeErr   error
	socketInfo os.FileInfo
}

type connectionPair struct {
	mu       sync.Mutex
	client   net.Conn
	upstream net.Conn
	closed   bool
}

func Start(ctx context.Context, config Config) (*Proxy, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(config.ControlSocket); err == nil {
		return nil, errors.New("PostgreSQL route proxy control socket already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("inspect PostgreSQL route proxy control socket")
	}
	parent, err := os.Lstat(filepath.Dir(config.ControlSocket))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("invalid PostgreSQL route proxy control directory")
	}

	tcpAddress, err := net.ResolveTCPAddr("tcp4", config.ListenAddress)
	if err != nil {
		return nil, errors.New("resolve PostgreSQL route proxy listen address")
	}
	listener, err := net.ListenTCP("tcp4", tcpAddress)
	if err != nil {
		return nil, errors.New("listen for PostgreSQL route proxy")
	}
	controlAddress := &net.UnixAddr{Name: config.ControlSocket, Net: "unix"}
	control, err := net.ListenUnix("unix", controlAddress)
	if err != nil {
		_ = listener.Close()
		return nil, errors.New("listen for PostgreSQL route proxy control")
	}
	control.SetUnlinkOnClose(false)
	socketInfo, err := os.Lstat(config.ControlSocket)
	if err != nil || socketInfo.Mode()&os.ModeSocket == 0 {
		_ = control.Close()
		_ = listener.Close()
		return nil, errors.New("inspect PostgreSQL route proxy control socket")
	}
	if err := os.Chmod(config.ControlSocket, 0o600); err != nil {
		_ = control.Close()
		_ = listener.Close()
		removeSocket(config.ControlSocket, socketInfo)
		return nil, errors.New("protect PostgreSQL route proxy control socket")
	}

	proxyContext, cancel := context.WithCancel(ctx)
	proxy := &Proxy{
		listener: listener,
		control:  control,
		probe:    config.HealthProbe,
		routes: map[Route]string{
			Primary:  config.PrimaryTarget,
			Promoted: config.PromotedTarget,
		},
		ctx:        proxyContext,
		cancel:     cancel,
		route:      config.InitialRoute,
		active:     make(map[*connectionPair]struct{}),
		traffic:    make(chan struct{}, maxActiveConnections),
		controls:   make(chan struct{}, maxControlConnections),
		socketInfo: socketInfo,
	}
	proxy.wait.Add(2)
	go proxy.acceptTraffic()
	go proxy.acceptControl()
	go func() {
		<-proxyContext.Done()
		_ = proxy.Close()
	}()
	return proxy, nil
}

func (proxy *Proxy) Address() string {
	return proxy.listener.Addr().String()
}

func (proxy *Proxy) Close() error {
	proxy.closeOnce.Do(func() {
		proxy.cancel()
		trafficErr := proxy.listener.Close()
		controlErr := proxy.control.Close()
		proxy.mu.Lock()
		connections := make([]*connectionPair, 0, len(proxy.active))
		for connection := range proxy.active {
			connections = append(connections, connection)
		}
		proxy.mu.Unlock()
		for _, connection := range connections {
			connection.close()
		}
		proxy.wait.Wait()
		removeSocket(proxy.control.Addr().String(), proxy.socketInfo)
		if trafficErr != nil && !errors.Is(trafficErr, net.ErrClosed) {
			proxy.closeErr = errors.New("close PostgreSQL route proxy listener")
		} else if controlErr != nil && !errors.Is(controlErr, net.ErrClosed) {
			proxy.closeErr = errors.New("close PostgreSQL route proxy control listener")
		}
	})
	return proxy.closeErr
}

func RouteTo(ctx context.Context, controlSocket string, route Route) error {
	if !validRoute(route) {
		return errors.New("invalid PostgreSQL route proxy route")
	}
	response, err := controlRequest(ctx, controlSocket, "route "+string(route)+"\n")
	if err != nil {
		return err
	}
	if response != "ok "+string(route) {
		return errors.New("PostgreSQL route proxy rejected route change")
	}
	return nil
}

func Status(ctx context.Context, controlSocket string) (Route, error) {
	response, err := controlRequest(ctx, controlSocket, "status\n")
	if err != nil {
		return "", err
	}
	const prefix = "ok "
	if !strings.HasPrefix(response, prefix) {
		return "", errors.New("PostgreSQL route proxy rejected status request")
	}
	route := Route(strings.TrimPrefix(response, prefix))
	if !validRoute(route) {
		return "", errors.New("PostgreSQL route proxy returned invalid status")
	}
	return route, nil
}

func SelectWritable(ctx context.Context, controlSocket string, expectedGeneration uint64) (Route, error) {
	if expectedGeneration == 0 {
		return "", errors.New("invalid PostgreSQL promotion generation")
	}
	response, err := controlRequest(
		ctx,
		controlSocket,
		"select "+strconv.FormatUint(expectedGeneration, 10)+"\n",
	)
	if err != nil {
		return "", err
	}
	const prefix = "ok "
	if !strings.HasPrefix(response, prefix) {
		return "", errors.New("PostgreSQL route proxy rejected writable selection")
	}
	route := Route(strings.TrimPrefix(response, prefix))
	if !validRoute(route) {
		return "", errors.New("PostgreSQL route proxy returned invalid writable selection")
	}
	return route, nil
}

func (proxy *Proxy) acceptTraffic() {
	defer proxy.wait.Done()
	for {
		client, err := proxy.listener.Accept()
		if err != nil {
			return
		}
		select {
		case proxy.traffic <- struct{}{}:
		default:
			_ = client.Close()
			continue
		}
		proxy.wait.Add(1)
		go func() {
			defer func() { <-proxy.traffic }()
			proxy.forward(client)
		}()
	}
}

func (proxy *Proxy) forward(client net.Conn) {
	defer proxy.wait.Done()
	pair := &connectionPair{client: client}
	proxy.mu.Lock()
	target := proxy.routes[proxy.route]
	generation := proxy.generation
	proxy.active[pair] = struct{}{}
	proxy.mu.Unlock()
	defer func() {
		pair.close()
		proxy.mu.Lock()
		delete(proxy.active, pair)
		proxy.mu.Unlock()
	}()

	dialContext, cancel := context.WithTimeout(proxy.ctx, dialTimeout)
	defer cancel()
	upstream, err := (&net.Dialer{}).DialContext(dialContext, "tcp4", target)
	if err != nil {
		return
	}
	proxy.mu.Lock()
	if generation != proxy.generation {
		proxy.mu.Unlock()
		_ = upstream.Close()
		return
	}
	if !pair.setUpstream(upstream) {
		proxy.mu.Unlock()
		_ = upstream.Close()
		return
	}
	proxy.mu.Unlock()

	done := make(chan struct{}, 2)
	go copyConnection(upstream, client, done)
	go copyConnection(client, upstream, done)
	select {
	case <-done:
	case <-proxy.ctx.Done():
	}
}

func (proxy *Proxy) acceptControl() {
	defer proxy.wait.Done()
	for {
		connection, err := proxy.control.AcceptUnix()
		if err != nil {
			return
		}
		select {
		case proxy.controls <- struct{}{}:
		default:
			_ = connection.Close()
			continue
		}
		proxy.wait.Add(1)
		go func() {
			defer func() { <-proxy.controls }()
			proxy.handleControl(connection)
		}()
	}
}

func (proxy *Proxy) handleControl(connection *net.UnixConn) {
	defer proxy.wait.Done()
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(controlDeadline))
	reader := bufio.NewReaderSize(connection, maxControlCommandBytes+1)
	command, err := reader.ReadSlice('\n')
	if err != nil || len(command) > maxControlCommandBytes {
		_, _ = io.WriteString(connection, "error\n")
		return
	}

	switch string(command) {
	case "status\n":
		proxy.mu.Lock()
		route := proxy.route
		proxy.mu.Unlock()
		_, _ = io.WriteString(connection, "ok "+string(route)+"\n")
	case "route primary\n":
		proxy.switchRoute(Primary)
		_, _ = io.WriteString(connection, "ok primary\n")
	case "route promoted\n":
		proxy.switchRoute(Promoted)
		_, _ = io.WriteString(connection, "ok promoted\n")
	default:
		expectedGeneration, ok := parseSelectionCommand(string(command))
		if !ok {
			_, _ = io.WriteString(connection, "error\n")
			return
		}
		route, err := proxy.selectWritable(expectedGeneration)
		if err != nil {
			_, _ = io.WriteString(connection, "error\n")
			return
		}
		_, _ = io.WriteString(connection, "ok "+string(route)+"\n")
	}
}

func (proxy *Proxy) selectWritable(expectedGeneration uint64) (Route, error) {
	if proxy.probe == nil {
		return "", errors.New("PostgreSQL route proxy health probe is unavailable")
	}
	probeContext, cancel := context.WithTimeout(proxy.ctx, selectionTimeout)
	defer cancel()
	proxy.mu.Lock()
	controlGeneration := proxy.generation
	proxy.mu.Unlock()

	var selected Route
	for confirmation := range confirmationCount {
		candidate, err := proxy.probeWritableRound(probeContext, expectedGeneration)
		if err != nil {
			return "", err
		}
		if selected != "" && candidate != selected {
			return "", errors.New("PostgreSQL route proxy writable target changed during confirmation")
		}
		selected = candidate
		if confirmation+1 == confirmationCount {
			break
		}
		timer := time.NewTimer(confirmationInterval)
		select {
		case <-timer.C:
		case <-probeContext.Done():
			timer.Stop()
			return "", errors.New("PostgreSQL route proxy writable confirmation timed out")
		}
	}
	if !proxy.switchRouteAtGeneration(selected, controlGeneration) {
		return "", errors.New("PostgreSQL route proxy route changed during writable confirmation")
	}
	return selected, nil
}

func (proxy *Proxy) probeWritableRound(ctx context.Context, expectedGeneration uint64) (Route, error) {
	roundContext, cancel := context.WithTimeout(ctx, probeRoundTimeout)
	defer cancel()
	type result struct {
		route  Route
		health Health
		err    error
	}
	results := make(chan result, len(proxy.routes))
	for route, target := range proxy.routes {
		go func() {
			health, err := proxy.probe(roundContext, target)
			results <- result{route: route, health: health, err: err}
		}()
	}

	var selected Route
	for range proxy.routes {
		select {
		case candidate := <-results:
			if candidate.err != nil || !candidate.health.Writable {
				continue
			}
			if selected != "" {
				return "", errors.New("PostgreSQL route proxy found multiple writable targets")
			}
			if candidate.health.PromotionGeneration != expectedGeneration {
				return "", errors.New("PostgreSQL route proxy writable target has unexpected promotion generation")
			}
			selected = candidate.route
		case <-roundContext.Done():
			return "", errors.New("PostgreSQL route proxy writable probe timed out")
		}
	}
	if selected == "" {
		return "", errors.New("PostgreSQL route proxy found no writable target")
	}
	return selected, nil
}

func parseSelectionCommand(command string) (uint64, bool) {
	const prefix = "select "
	if !strings.HasPrefix(command, prefix) || !strings.HasSuffix(command, "\n") {
		return 0, false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(command, prefix), "\n")
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, false
	}
	generation, err := strconv.ParseUint(value, 10, 64)
	return generation, err == nil && generation > 0
}

func (proxy *Proxy) switchRoute(route Route) {
	proxy.switchRouteConditionally(route, nil)
}

func (proxy *Proxy) switchRouteAtGeneration(route Route, expectedGeneration uint64) bool {
	return proxy.switchRouteConditionally(route, &expectedGeneration)
}

func (proxy *Proxy) switchRouteConditionally(route Route, expectedGeneration *uint64) bool {
	proxy.mu.Lock()
	if expectedGeneration != nil && proxy.generation != *expectedGeneration {
		proxy.mu.Unlock()
		return false
	}
	if proxy.route == route {
		proxy.mu.Unlock()
		return true
	}
	proxy.route = route
	proxy.generation++
	connections := make([]*connectionPair, 0, len(proxy.active))
	for connection := range proxy.active {
		connections = append(connections, connection)
	}
	proxy.mu.Unlock()
	for _, connection := range connections {
		connection.close()
	}
	return true
}

func (pair *connectionPair) close() {
	pair.mu.Lock()
	if pair.closed {
		pair.mu.Unlock()
		return
	}
	pair.closed = true
	client := pair.client
	upstream := pair.upstream
	pair.mu.Unlock()
	_ = client.Close()
	if upstream != nil {
		_ = upstream.Close()
	}
}

func (pair *connectionPair) setUpstream(upstream net.Conn) bool {
	pair.mu.Lock()
	defer pair.mu.Unlock()
	if pair.closed {
		return false
	}
	pair.upstream = upstream
	return true
}

func copyConnection(destination net.Conn, source net.Conn, done chan<- struct{}) {
	_, _ = io.Copy(destination, source)
	done <- struct{}{}
}

func controlRequest(ctx context.Context, controlSocket string, command string) (string, error) {
	if err := validateControlSocket(controlSocket); err != nil {
		return "", err
	}
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", controlSocket)
	if err != nil {
		return "", errors.New("connect to PostgreSQL route proxy control socket")
	}
	defer connection.Close()
	deadline := time.Now().Add(controlDeadline)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
	if _, err := io.WriteString(connection, command); err != nil {
		return "", errors.New("write PostgreSQL route proxy control request")
	}
	response, err := bufio.NewReaderSize(connection, maxControlCommandBytes+1).ReadSlice('\n')
	if err != nil || len(response) > maxControlCommandBytes {
		return "", errors.New("read PostgreSQL route proxy control response")
	}
	return strings.TrimSuffix(string(response), "\n"), nil
}

func validateConfig(config Config) error {
	if err := validateLoopbackAddress(config.ListenAddress, true); err != nil {
		return errors.New("invalid PostgreSQL route proxy listen address")
	}
	if err := validateLoopbackAddress(config.PrimaryTarget, false); err != nil {
		return errors.New("invalid PostgreSQL route proxy primary target")
	}
	if err := validateLoopbackAddress(config.PromotedTarget, false); err != nil {
		return errors.New("invalid PostgreSQL route proxy promoted target")
	}
	if config.PrimaryTarget == config.PromotedTarget {
		return errors.New("PostgreSQL route proxy targets must be distinct")
	}
	if err := validateControlSocket(config.ControlSocket); err != nil {
		return err
	}
	if !validRoute(config.InitialRoute) {
		return errors.New("invalid PostgreSQL route proxy initial route")
	}
	return nil
}

func validateLoopbackAddress(address string, allowZeroPort bool) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.Equal(net.IPv4(127, 0, 0, 1)) || host != "127.0.0.1" {
		return errors.New("address must use literal IPv4 loopback")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 || (!allowZeroPort && port == 0) {
		return errors.New("address has invalid port")
	}
	return nil
}

func validateControlSocket(controlSocket string) error {
	if controlSocket == "" || !filepath.IsAbs(controlSocket) || filepath.Clean(controlSocket) != controlSocket {
		return errors.New("invalid PostgreSQL route proxy control socket")
	}
	return nil
}

func validRoute(route Route) bool {
	return route == Primary || route == Promoted
}

func removeSocket(path string, expected os.FileInfo) {
	current, err := os.Lstat(path)
	if err != nil {
		return
	}
	if expected != nil && !os.SameFile(current, expected) {
		return
	}
	if current.Mode()&os.ModeSocket == 0 {
		return
	}
	_ = os.Remove(path)
}
