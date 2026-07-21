package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution/recoveryconformance/pgrouteproxy"
)

func TestRouteManagerGatesReadinessAndRestartsSupervisor(t *testing.T) {
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
		result <- runRouteManager(
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
	requireSupervisorEvent(t, events.events, supervisorReadyState)
	first.exit(errors.New("injected supervisor loss"))
	requireSupervisorEvent(t, events.events, supervisorRestartScheduledState)

	receiveStartedChild(t, started)
	requireNoSupervisorEvent(t, events.events)
	readyThrough.Store(2)
	requireSupervisorEvent(t, events.events, supervisorReadyState)
	cancel()
	if err := receiveSupervisorResult(t, result); err != nil {
		t.Fatal(err)
	}
	if got := events.String(); got != "supervisor-ready\nsupervisor-restart-scheduled\nsupervisor-ready\n" {
		t.Fatalf("unexpected manager states: %q", got)
	}
}

func TestRouteManagerExhaustsBoundedRestartBudget(t *testing.T) {
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
	err := runRouteManager(
		context.Background(),
		testSupervisorConfig(false),
		policy,
		dependencies,
		events,
	)
	if err == nil || starts.Load() != 4 {
		t.Fatalf("starts=%d err=%v", starts.Load(), err)
	}
	if got := events.String(); got != strings.Repeat("supervisor-restart-scheduled\n", 3) {
		t.Fatalf("unexpected manager states: %q", got)
	}
}

func TestSupervisorChildArgumentsBindManagerAndDropInitialState(t *testing.T) {
	config := testSupervisorConfig(true)
	initial := strings.Join(supervisorChildArguments(config, true, 1234), " ")
	recovered := strings.Join(supervisorChildArguments(config, false, 1234), " ")
	if !strings.Contains(initial, "--route primary --promotion-generation 1") {
		t.Fatalf("initial arguments do not bind initial state: %q", initial)
	}
	if !strings.Contains(initial, "--manager-pid 1234") {
		t.Fatalf("initial arguments do not bind manager ownership: %q", initial)
	}
	if strings.Contains(recovered, "--route") || strings.Contains(recovered, "--promotion-generation") {
		t.Fatalf("recovery arguments contain caller-supplied state: %q", recovered)
	}
	if !strings.Contains(recovered, "--manager-pid 1234") {
		t.Fatalf("recovery arguments do not bind manager ownership: %q", recovered)
	}
}
