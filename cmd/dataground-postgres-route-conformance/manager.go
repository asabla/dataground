package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/asabla/dataground/internal/execution/recoveryconformance/pgrouteproxy"
)

const supervisorReadyState = "supervisor-ready"
const supervisorRestartScheduledState = "supervisor-restart-scheduled"

var conformanceManagerPolicy = routeSupervisorPolicy{
	RestartBackoffs:  []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second},
	ReadinessTimeout: 12 * time.Second,
	ReadinessPoll:    100 * time.Millisecond,
	ShutdownTimeout:  5 * time.Second,
}

func manageRouteSupervisor(
	ctx context.Context,
	config routeSupervisorConfig,
	output io.Writer,
) error {
	executable, err := os.Executable()
	if err != nil {
		return errors.New("resolve PostgreSQL route conformance executable")
	}
	dependencies := routeSupervisorDependencies{
		StartChild: func(arguments []string) (routeChild, error) {
			command := exec.Command(executable, arguments...)
			command.Stdout = io.Discard
			command.Stderr = io.Discard
			if err := configureRouteChildOwnership(command); err != nil {
				return nil, err
			}
			if err := command.Start(); err != nil {
				return nil, errors.New("start PostgreSQL route conformance supervisor")
			}
			return commandChild{command: command}, nil
		},
		StateStatus: pgrouteproxy.StateStatus,
		StateExists: routeStateExists,
	}
	return runRouteManager(ctx, config, conformanceManagerPolicy, dependencies, output)
}

func runRouteManager(
	ctx context.Context,
	config routeSupervisorConfig,
	policy routeSupervisorPolicy,
	dependencies routeSupervisorDependencies,
	output io.Writer,
) error {
	return runBoundedConformanceSupervisor(
		ctx,
		config.ControlSocket,
		config.StateFile,
		config.InitialRoute != "",
		policy,
		dependencies,
		output,
		supervisorReadyState,
		supervisorRestartScheduledState,
		func(initializing bool) []string {
			return supervisorChildArguments(config, initializing, os.Getpid())
		},
	)
}

func supervisorChildArguments(
	config routeSupervisorConfig,
	initializing bool,
	managerPID int,
) []string {
	arguments := []string{
		"--mode", "supervise",
		"--listen-address", config.ListenAddress,
		"--control-socket", config.ControlSocket,
		"--state-file", config.StateFile,
		"--primary-target", config.PrimaryTarget,
		"--promoted-target", config.PromotedTarget,
		"--manager-pid", strconv.Itoa(managerPID),
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
