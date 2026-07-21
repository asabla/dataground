package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/asabla/dataground/internal/execution/recoveryconformance/pgrouteproxy"
)

const routerReadyState = "router-ready"
const routerRestartScheduledState = "router-restart-scheduled"

var conformanceSupervisorPolicy = routeSupervisorPolicy{
	RestartBackoffs:  []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second},
	ReadinessTimeout: 12 * time.Second,
	ReadinessPoll:    100 * time.Millisecond,
	ShutdownTimeout:  3 * time.Second,
}

type routeSupervisorConfig struct {
	ListenAddress              string
	ControlSocket              string
	StateFile                  string
	PrimaryTarget              string
	PromotedTarget             string
	InitialRoute               string
	InitialPromotionGeneration uint64
}

type routeSupervisorPolicy struct {
	RestartBackoffs  []time.Duration
	ReadinessTimeout time.Duration
	ReadinessPoll    time.Duration
	ShutdownTimeout  time.Duration
}

type routeChild interface {
	Wait() error
	Signal(os.Signal) error
	Kill() error
}

type commandChild struct {
	command *exec.Cmd
}

func (child commandChild) Wait() error {
	return child.command.Wait()
}

func (child commandChild) Signal(signal os.Signal) error {
	return child.command.Process.Signal(signal)
}

func (child commandChild) Kill() error {
	return child.command.Process.Kill()
}

type routeSupervisorDependencies struct {
	StartChild  func([]string) (routeChild, error)
	StateStatus func(context.Context, string) (pgrouteproxy.Route, uint64, error)
	StateExists func(string) (bool, error)
}

func superviseRoute(ctx context.Context, config routeSupervisorConfig, output io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return errors.New("resolve PostgreSQL route conformance executable")
	}
	dependencies := routeSupervisorDependencies{
		StartChild: func(arguments []string) (routeChild, error) {
			command := exec.Command(executable, arguments...)
			command.Stdout = io.Discard
			command.Stderr = io.Discard
			if err := command.Start(); err != nil {
				return nil, errors.New("start PostgreSQL route conformance child")
			}
			return commandChild{command: command}, nil
		},
		StateStatus: pgrouteproxy.StateStatus,
		StateExists: routeStateExists,
	}
	return runRouteSupervisor(ctx, config, conformanceSupervisorPolicy, dependencies, output)
}

func runRouteSupervisor(
	ctx context.Context,
	config routeSupervisorConfig,
	policy routeSupervisorPolicy,
	dependencies routeSupervisorDependencies,
	output io.Writer,
) error {
	if err := validateSupervisorPolicy(policy); err != nil {
		return err
	}
	stateExists, err := dependencies.StateExists(config.StateFile)
	if err != nil {
		return err
	}
	initializing := config.InitialRoute != ""
	if initializing == stateExists {
		return errors.New("PostgreSQL route supervisor state boundary is invalid")
	}

	for attempt := 0; ; attempt++ {
		child, startErr := dependencies.StartChild(routeChildArguments(config, initializing))
		if startErr == nil {
			exited, childExit, waitErr := waitForRouteChild(
				ctx,
				child,
				config.ControlSocket,
				policy,
				dependencies.StateStatus,
			)
			if ctx.Err() != nil {
				if !exited {
					stopRouteChild(child, childExit, policy.ShutdownTimeout)
				}
				return nil
			}
			if waitErr == nil {
				if err := writeSupervisorState(output, routerReadyState); err != nil {
					stopRouteChild(child, childExit, policy.ShutdownTimeout)
					return err
				}
				select {
				case <-ctx.Done():
					stopRouteChild(child, childExit, policy.ShutdownTimeout)
					return nil
				case <-childExit:
				}
			} else if !exited {
				stopRouteChild(child, childExit, policy.ShutdownTimeout)
			}
		}

		if attempt >= len(policy.RestartBackoffs) {
			return errors.New("PostgreSQL route supervisor restart budget exhausted")
		}
		if err := writeSupervisorState(output, routerRestartScheduledState); err != nil {
			return err
		}
		if initializing {
			stateExists, err = dependencies.StateExists(config.StateFile)
			if err != nil {
				return err
			}
			if stateExists {
				initializing = false
			}
		}
		if !waitForSupervisorBackoff(ctx, policy.RestartBackoffs[attempt]) {
			return nil
		}
	}
}

func writeSupervisorState(output io.Writer, state string) error {
	if _, err := fmt.Fprintln(output, state); err != nil {
		return errors.New("write PostgreSQL route supervisor state")
	}
	return nil
}

func waitForRouteChild(
	ctx context.Context,
	child routeChild,
	controlSocket string,
	policy routeSupervisorPolicy,
	stateStatus func(context.Context, string) (pgrouteproxy.Route, uint64, error),
) (bool, chan error, error) {
	exit := make(chan error, 1)
	go func() { exit <- child.Wait() }()
	deadline := time.NewTimer(policy.ReadinessTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(policy.ReadinessPoll)
	defer ticker.Stop()

	for {
		probeContext, cancel := context.WithTimeout(ctx, policy.ReadinessPoll)
		route, generation, err := stateStatus(probeContext, controlSocket)
		cancel()
		if err == nil && validRoute(string(route)) && generation > 0 {
			return false, exit, nil
		}
		select {
		case <-ctx.Done():
			return false, exit, ctx.Err()
		case <-exit:
			return true, exit, errors.New("PostgreSQL route conformance child exited before readiness")
		case <-deadline.C:
			return false, exit, errors.New("PostgreSQL route conformance child readiness timed out")
		case <-ticker.C:
		}
	}
}

func stopRouteChild(child routeChild, exit <-chan error, timeout time.Duration) {
	_ = child.Signal(os.Interrupt)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-exit:
		return
	case <-timer.C:
		_ = child.Kill()
		<-exit
	}
}

func waitForSupervisorBackoff(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func routeChildArguments(config routeSupervisorConfig, initializing bool) []string {
	arguments := []string{
		"--mode", "serve",
		"--listen-address", config.ListenAddress,
		"--control-socket", config.ControlSocket,
		"--state-file", config.StateFile,
		"--primary-target", config.PrimaryTarget,
		"--promoted-target", config.PromotedTarget,
	}
	if initializing {
		arguments = append(
			arguments,
			"--route", config.InitialRoute,
			"--promotion-generation", strconv.FormatUint(config.InitialPromotionGeneration, 10),
		)
	}
	return arguments
}

func routeStateExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("inspect PostgreSQL route supervisor state")
	}
	return true, nil
}

func validateSupervisorPolicy(policy routeSupervisorPolicy) error {
	if len(policy.RestartBackoffs) == 0 || policy.ReadinessTimeout <= 0 ||
		policy.ReadinessPoll <= 0 || policy.ReadinessPoll > policy.ReadinessTimeout ||
		policy.ShutdownTimeout <= 0 {
		return errors.New("invalid PostgreSQL route supervisor policy")
	}
	for _, backoff := range policy.RestartBackoffs {
		if backoff < 0 {
			return errors.New("invalid PostgreSQL route supervisor policy")
		}
	}
	return nil
}
