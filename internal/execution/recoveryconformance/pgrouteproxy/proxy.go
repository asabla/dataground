// Package pgrouteproxy provides a bounded, loopback-only TCP routing proxy for
// PostgreSQL failover conformance. Routes are selected from predeclared targets
// by caller-triggered, generation-bound health confirmation and persisted before
// sessions change. Restart recovery reconfirms the exact persisted state; the
// proxy never promotes, elects, fences, or replays traffic.
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
	staleSocketDialTimeout = 200 * time.Millisecond
)

type Route string

const (
	Primary  Route = "primary"
	Promoted Route = "promoted"
)

type Config struct {
	ListenAddress              string
	ControlSocket              string
	StateFile                  string
	PrimaryTarget              string
	PromotedTarget             string
	InitialRoute               Route
	InitialPromotionGeneration uint64
	HealthProbe                HealthProbe
}

type Health struct {
	Writable            bool
	PromotionGeneration uint64
}

type HealthProbe func(context.Context, string) (Health, error)

type Proxy struct {
	listener *net.TCPListener
	control  *net.UnixListener
	state    *routeStateStore
	routes   map[Route]string
	probe    HealthProbe

	ctx                 context.Context
	cancel              context.CancelFunc
	mu                  sync.Mutex
	route               Route
	generation          uint64
	promotionGeneration uint64
	active              map[*connectionPair]struct{}
	traffic             chan struct{}
	controls            chan struct{}
	wait                sync.WaitGroup
	closeOnce           sync.Once
	closeErr            error
	socketInfo          os.FileInfo
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
	stateStore, err := openRouteStateStore(config.StateFile, config.ControlSocket)
	if err != nil {
		return nil, err
	}
	closeState := true
	defer func() {
		if closeState {
			_ = stateStore.close()
		}
	}()

	tcpAddress, err := net.ResolveTCPAddr("tcp4", config.ListenAddress)
	if err != nil {
		return nil, errors.New("resolve PostgreSQL route proxy listen address")
	}
	listener, err := net.ListenTCP("tcp4", tcpAddress)
	if err != nil {
		return nil, errors.New("listen for PostgreSQL route proxy")
	}
	routes := map[Route]string{
		Primary:  config.PrimaryTarget,
		Promoted: config.PromotedTarget,
	}
	if err := prepareControlSocket(config.ControlSocket); err != nil {
		_ = listener.Close()
		return nil, err
	}
	state, err := initializeOrRecoverRouteState(ctx, stateStore, config, routes)
	if err != nil {
		_ = listener.Close()
		return nil, err
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
		listener:            listener,
		control:             control,
		state:               stateStore,
		probe:               config.HealthProbe,
		routes:              routes,
		ctx:                 proxyContext,
		cancel:              cancel,
		route:               state.Route,
		promotionGeneration: state.PromotionGeneration,
		active:              make(map[*connectionPair]struct{}),
		traffic:             make(chan struct{}, maxActiveConnections),
		controls:            make(chan struct{}, maxControlConnections),
		socketInfo:          socketInfo,
	}
	proxy.wait.Add(2)
	go proxy.acceptTraffic()
	go proxy.acceptControl()
	go func() {
		<-proxyContext.Done()
		_ = proxy.Close()
	}()
	closeState = false
	return proxy, nil
}

func initializeOrRecoverRouteState(
	ctx context.Context,
	store *routeStateStore,
	config Config,
	routes map[Route]string,
) (persistedRouteState, error) {
	initializing := config.InitialRoute != ""
	if initializing {
		exists, err := store.exists()
		if err != nil {
			return persistedRouteState{}, err
		}
		if exists {
			return persistedRouteState{}, errors.New("PostgreSQL route state already exists")
		}
		state := persistedRouteState{
			Version:             routeStateVersion,
			PrimaryTarget:       config.PrimaryTarget,
			PromotedTarget:      config.PromotedTarget,
			Route:               config.InitialRoute,
			PromotionGeneration: config.InitialPromotionGeneration,
		}
		selected, err := confirmWritable(
			ctx,
			routes,
			config.HealthProbe,
			state.PromotionGeneration,
		)
		if err != nil || selected != state.Route {
			return persistedRouteState{}, errors.New("confirm initial PostgreSQL route state")
		}
		if err := store.write(state, false); err != nil {
			return persistedRouteState{}, err
		}
		return state, nil
	}

	state, err := store.read(config.PrimaryTarget, config.PromotedTarget)
	if err != nil {
		return persistedRouteState{}, err
	}
	selected, err := confirmWritable(
		ctx,
		routes,
		config.HealthProbe,
		state.PromotionGeneration,
	)
	if err != nil || selected != state.Route {
		return persistedRouteState{}, errors.New("confirm recovered PostgreSQL route state")
	}
	return state, nil
}

func confirmWritable(
	ctx context.Context,
	routes map[Route]string,
	probe HealthProbe,
	expectedGeneration uint64,
) (Route, error) {
	if probe == nil || expectedGeneration == 0 {
		return "", errors.New("PostgreSQL route proxy health probe is unavailable")
	}
	probeContext, cancel := context.WithTimeout(ctx, selectionTimeout)
	defer cancel()
	var selected Route
	for confirmation := range confirmationCount {
		candidate, err := probeWritableRound(probeContext, routes, probe, expectedGeneration)
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
	return selected, nil
}

func prepareControlSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("inspect PostgreSQL route proxy control socket")
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 ||
		!routePathOwnedByCurrentUser(info) || !routePathSingleLink(info) {
		return errors.New("invalid PostgreSQL route proxy control path")
	}
	connection, dialErr := net.DialTimeout("unix", path, staleSocketDialTimeout)
	if dialErr == nil {
		_ = connection.Close()
		return errors.New("PostgreSQL route proxy control socket is active")
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return errors.New("PostgreSQL route proxy control socket changed during recovery")
	}
	if err := os.Remove(path); err != nil {
		return errors.New("remove stale PostgreSQL route proxy control socket")
	}
	return nil
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
		stateErr := proxy.state.close()
		if trafficErr != nil && !errors.Is(trafficErr, net.ErrClosed) {
			proxy.closeErr = errors.New("close PostgreSQL route proxy listener")
		} else if controlErr != nil && !errors.Is(controlErr, net.ErrClosed) {
			proxy.closeErr = errors.New("close PostgreSQL route proxy control listener")
		} else if stateErr != nil {
			proxy.closeErr = stateErr
		}
	})
	return proxy.closeErr
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

func StateStatus(ctx context.Context, controlSocket string) (Route, uint64, error) {
	response, err := controlRequest(ctx, controlSocket, "state\n")
	if err != nil {
		return "", 0, err
	}
	fields := strings.Split(response, " ")
	if len(fields) != 3 || fields[0] != "ok" {
		return "", 0, errors.New("PostgreSQL route proxy rejected state request")
	}
	route := Route(fields[1])
	generation, err := strconv.ParseUint(fields[2], 10, 64)
	if !validRoute(route) || err != nil || generation == 0 {
		return "", 0, errors.New("PostgreSQL route proxy returned invalid state")
	}
	return route, generation, nil
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
	case "state\n":
		proxy.mu.Lock()
		route := proxy.route
		promotionGeneration := proxy.promotionGeneration
		proxy.mu.Unlock()
		_, _ = io.WriteString(
			connection,
			"ok "+string(route)+" "+strconv.FormatUint(promotionGeneration, 10)+"\n",
		)
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
	proxy.mu.Lock()
	controlGeneration := proxy.generation
	proxy.mu.Unlock()
	selected, err := confirmWritable(proxy.ctx, proxy.routes, proxy.probe, expectedGeneration)
	if err != nil {
		return "", err
	}
	if err := proxy.persistSelection(selected, expectedGeneration, controlGeneration); err != nil {
		return "", err
	}
	return selected, nil
}

func probeWritableRound(
	ctx context.Context,
	routes map[Route]string,
	probe HealthProbe,
	expectedGeneration uint64,
) (Route, error) {
	roundContext, cancel := context.WithTimeout(ctx, probeRoundTimeout)
	defer cancel()
	type result struct {
		route  Route
		health Health
		err    error
	}
	results := make(chan result, len(routes))
	for route, target := range routes {
		go func() {
			health, err := probe(roundContext, target)
			results <- result{route: route, health: health, err: err}
		}()
	}

	var selected Route
	for range routes {
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

func (proxy *Proxy) persistSelection(
	route Route,
	promotionGeneration uint64,
	expectedControlGeneration uint64,
) error {
	proxy.mu.Lock()
	if proxy.generation != expectedControlGeneration {
		proxy.mu.Unlock()
		return errors.New("PostgreSQL route proxy route changed during writable confirmation")
	}
	if proxy.route == route && proxy.promotionGeneration == promotionGeneration {
		proxy.mu.Unlock()
		return nil
	}
	if promotionGeneration <= proxy.promotionGeneration {
		proxy.mu.Unlock()
		return errors.New("PostgreSQL route proxy rejected stale promotion generation")
	}
	state := persistedRouteState{
		Version:             routeStateVersion,
		PrimaryTarget:       proxy.routes[Primary],
		PromotedTarget:      proxy.routes[Promoted],
		Route:               route,
		PromotionGeneration: promotionGeneration,
	}
	if err := proxy.state.write(state, true); err != nil {
		if errors.Is(err, errRouteStateOutcomeUnknown) {
			proxy.cancel()
		}
		proxy.mu.Unlock()
		return err
	}
	proxy.route = route
	proxy.promotionGeneration = promotionGeneration
	proxy.generation++
	connections := make([]*connectionPair, 0, len(proxy.active))
	for connection := range proxy.active {
		connections = append(connections, connection)
	}
	proxy.mu.Unlock()
	for _, connection := range connections {
		connection.close()
	}
	return nil
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
	initializing := config.InitialRoute != "" || config.InitialPromotionGeneration != 0
	if initializing && (!validRoute(config.InitialRoute) || config.InitialPromotionGeneration == 0) {
		return errors.New("invalid PostgreSQL route proxy initial route")
	}
	if config.HealthProbe == nil {
		return errors.New("PostgreSQL route proxy health probe is unavailable")
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
