package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution/recoveryconformance/pgrouteproxy"
)

func TestRouteSupervisorGatesReadinessAndRestartsChild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan *fakeRouteChild, 2)
	events := newSupervisorEventWriter()
	var currentChild atomic.Int64
	var readyThrough atomic.Int64
	var stateExists atomic.Bool

	dependencies := routeSupervisorDependencies{
		StartChild: func(arguments []string) (routeChild, error) {
			child := newFakeRouteChild()
			currentChild.Add(1)
			started <- child
			return child, nil
		},
		StateStatus: func(context.Context, string) (pgrouteproxy.Route, uint64, error) {
			if readyThrough.Load() < currentChild.Load() {
				return "", 0, errors.New("not ready")
			}
			return pgrouteproxy.Promoted, 2, nil
		},
		StateExists: func(string) (bool, error) {
			return stateExists.Load(), nil
		},
	}
	result := make(chan error, 1)
	go func() {
		result <- runRouteSupervisor(
			ctx,
			testSupervisorConfig(true),
			testSupervisorPolicy(),
			dependencies,
			events,
		)
	}()

	first := receiveStartedChild(t, started)
	requireNoSupervisorEvent(t, events.events)
	stateExists.Store(true)
	readyThrough.Store(1)
	requireSupervisorEvent(t, events.events, routerReadyState)
	first.exit(errors.New("injected process loss"))
	requireSupervisorEvent(t, events.events, routerRestartScheduledState)

	receiveStartedChild(t, started)
	requireNoSupervisorEvent(t, events.events)
	readyThrough.Store(2)
	requireSupervisorEvent(t, events.events, routerReadyState)
	cancel()
	if err := receiveSupervisorResult(t, result); err != nil {
		t.Fatal(err)
	}
	if got := events.String(); got != "router-ready\nrouter-restart-scheduled\nrouter-ready\n" {
		t.Fatalf("unexpected supervisor states: %q", got)
	}
}

func TestRouteSupervisorExhaustsBoundedRestartBudget(t *testing.T) {
	events := newSupervisorEventWriter()
	var starts atomic.Int64
	dependencies := routeSupervisorDependencies{
		StartChild: func(arguments []string) (routeChild, error) {
			starts.Add(1)
			child := newFakeRouteChild()
			child.exit(errors.New("injected startup failure"))
			return child, nil
		},
		StateStatus: func(context.Context, string) (pgrouteproxy.Route, uint64, error) {
			return "", 0, errors.New("not ready")
		},
		StateExists: func(string) (bool, error) { return true, nil },
	}
	policy := testSupervisorPolicy()
	policy.RestartBackoffs = []time.Duration{0, 0, 0}
	err := runRouteSupervisor(
		context.Background(),
		testSupervisorConfig(false),
		policy,
		dependencies,
		events,
	)
	if err == nil || starts.Load() != 4 {
		t.Fatalf("starts=%d err=%v", starts.Load(), err)
	}
	if got := events.String(); got != strings.Repeat("router-restart-scheduled\n", 3) {
		t.Fatalf("unexpected supervisor states: %q", got)
	}
}

func TestRouteSupervisorRejectsMismatchedInitialStateBoundary(t *testing.T) {
	for _, test := range []struct {
		name         string
		initializing bool
		stateExists  bool
	}{
		{name: "initial state already exists", initializing: true, stateExists: true},
		{name: "recovery state is missing", initializing: false, stateExists: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencies := routeSupervisorDependencies{
				StartChild: func([]string) (routeChild, error) {
					t.Fatal("child started across an invalid state boundary")
					return nil, nil
				},
				StateStatus: pgrouteproxy.StateStatus,
				StateExists: func(string) (bool, error) { return test.stateExists, nil },
			}
			err := runRouteSupervisor(
				context.Background(),
				testSupervisorConfig(test.initializing),
				testSupervisorPolicy(),
				dependencies,
				&bytes.Buffer{},
			)
			if err == nil {
				t.Fatal("supervisor accepted an invalid state boundary")
			}
		})
	}
}

func TestStopRouteChildEscalatesAfterShutdownTimeout(t *testing.T) {
	child := newFakeRouteChild()
	child.ignoreSignal = true
	exit := make(chan error, 1)
	go func() { exit <- child.Wait() }()
	stopRouteChild(child, exit, time.Millisecond)
	if !child.killed.Load() {
		t.Fatal("supervisor did not kill a child that ignored graceful shutdown")
	}
}

func TestRouteChildArgumentsDropInitialStateDuringRecovery(t *testing.T) {
	config := testSupervisorConfig(true)
	initial := strings.Join(routeChildArguments(config, true), " ")
	recovered := strings.Join(routeChildArguments(config, false), " ")
	if !strings.Contains(initial, "--route primary --promotion-generation 1") {
		t.Fatalf("initial arguments do not bind initial state: %q", initial)
	}
	if strings.Contains(recovered, "--route") || strings.Contains(recovered, "--promotion-generation") {
		t.Fatalf("recovery arguments contain caller-supplied state: %q", recovered)
	}
}

type fakeRouteChild struct {
	done         chan error
	once         sync.Once
	ignoreSignal bool
	killed       atomic.Bool
}

func newFakeRouteChild() *fakeRouteChild {
	return &fakeRouteChild{done: make(chan error, 1)}
}

func (child *fakeRouteChild) Wait() error {
	return <-child.done
}

func (child *fakeRouteChild) Signal(os.Signal) error {
	if !child.ignoreSignal {
		child.exit(nil)
	}
	return nil
}

func (child *fakeRouteChild) Kill() error {
	child.killed.Store(true)
	child.exit(errors.New("killed"))
	return nil
}

func (child *fakeRouteChild) exit(err error) {
	child.once.Do(func() { child.done <- err })
}

type supervisorEventWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	events chan string
}

func newSupervisorEventWriter() *supervisorEventWriter {
	return &supervisorEventWriter{events: make(chan string, 8)}
}

func (writer *supervisorEventWriter) Write(content []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	_, _ = writer.buffer.Write(content)
	writer.events <- strings.TrimSuffix(string(content), "\n")
	return len(content), nil
}

func (writer *supervisorEventWriter) String() string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.buffer.String()
}

func testSupervisorConfig(initializing bool) routeSupervisorConfig {
	config := routeSupervisorConfig{
		ListenAddress:  "127.0.0.1:55431",
		ControlSocket:  "/tmp/dataground-route/control.sock",
		StateFile:      "/tmp/dataground-route/state.json",
		PrimaryTarget:  "127.0.0.1:55432",
		PromotedTarget: "127.0.0.1:55433",
	}
	if initializing {
		config.InitialRoute = "primary"
		config.InitialPromotionGeneration = 1
	}
	return config
}

func testSupervisorPolicy() routeSupervisorPolicy {
	return routeSupervisorPolicy{
		RestartBackoffs:  []time.Duration{0},
		ReadinessTimeout: 200 * time.Millisecond,
		ReadinessPoll:    time.Millisecond,
		ShutdownTimeout:  200 * time.Millisecond,
	}
}

func receiveStartedChild(t *testing.T, started <-chan *fakeRouteChild) *fakeRouteChild {
	t.Helper()
	select {
	case child := <-started:
		return child
	case <-time.After(time.Second):
		t.Fatal("supervisor did not start a child")
		return nil
	}
}

func requireSupervisorEvent(t *testing.T, events <-chan string, expected string) {
	t.Helper()
	select {
	case event := <-events:
		if event != expected {
			t.Fatalf("event=%q expected=%q", event, expected)
		}
	case <-time.After(time.Second):
		t.Fatalf("supervisor did not emit %q", expected)
	}
}

func requireNoSupervisorEvent(t *testing.T, events <-chan string) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("supervisor emitted an event before readiness: %q", event)
	case <-time.After(10 * time.Millisecond):
	}
}

func receiveSupervisorResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop")
		return nil
	}
}
