package main

import (
	"context"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/execution/runtimeevidence"
)

var (
	localSupervisorImagePattern = regexp.MustCompile(`^ghcr\.io/asabla/dataground-supervisor-candidate@sha256:[a-f0-9]{64}$`)
	localSupervisorIDPattern    = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	localTopologyRunPattern     = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

type strictLocalRuntimeConfig struct {
	supervisorImage string
	topology        runtimeevidence.ObservedDockerTopologyConfig
}

func loadStrictLocalRuntimeConfig(lookup environmentLookup) (*strictLocalRuntimeConfig, error) {
	config := &strictLocalRuntimeConfig{}
	for _, input := range []struct {
		name  string
		value *string
	}{
		{"DATAGROUND_LOCAL_RUNTIME_SUPERVISOR_IMAGE", &config.supervisorImage},
		{"DATAGROUND_LOCAL_RUNTIME_SUPERVISOR_LOCAL_IMAGE_ID", &config.topology.SupervisorLocalImageID},
		{"DATAGROUND_LOCAL_RUNTIME_GATEWAY_CONFIG_SHA256", &config.topology.GatewayConfigSHA256},
		{"DATAGROUND_LOCAL_RUNTIME_TOPOLOGY_RUN_ID", &config.topology.RunID},
		{"DATAGROUND_LOCAL_RUNTIME_GATEWAY_CONTAINER_ID", &config.topology.ContainerID},
		{"DATAGROUND_LOCAL_RUNTIME_GATEWAY_STARTED_AT", &config.topology.StartedAt},
		{"DATAGROUND_LOCAL_RUNTIME_TOPOLOGY_ROOT", &config.topology.WorkspaceRoot},
		{"DATAGROUND_LOCAL_RUNTIME_DOCKER_BINARY", &config.topology.DockerBinary},
	} {
		value, err := requiredEnvironment(lookup, input.name)
		if err != nil {
			return nil, err
		}
		*input.value = value
	}
	if !config.valid() {
		return nil, ErrRuntimeCertificationUnavailable
	}
	return config, nil
}

func (config strictLocalRuntimeConfig) valid() bool {
	started, err := time.Parse(time.RFC3339Nano, config.topology.StartedAt)
	return err == nil && !started.IsZero() && !started.After(time.Now()) &&
		localSupervisorImagePattern.MatchString(config.supervisorImage) &&
		localSupervisorIDPattern.MatchString(config.topology.SupervisorLocalImageID) &&
		sha256Pattern.MatchString(config.topology.GatewayConfigSHA256) &&
		sha256Pattern.MatchString(config.topology.ContainerID) &&
		localTopologyRunPattern.MatchString(config.topology.RunID) &&
		cleanAbsoluteAcceptancePath(config.topology.WorkspaceRoot) &&
		cleanAbsoluteAcceptancePath(config.topology.DockerBinary) &&
		!strings.ContainsRune(config.topology.DockerBinary, os.PathListSeparator)
}
func (config localRuntimeAcceptanceConfig) acceptanceProfile() string {
	if config.strict != nil {
		return strictLocalRuntimeProfile
	}
	return localRuntimeProfile
}

type runtimeDeploymentObservation interface {
	Check(context.Context) error
	Close() error
}
type runtimeDeploymentObserverFactory func(runtimeevidence.ObservedDockerTopologyConfig) (runtimeDeploymentObservation, error)

func (checker *localRuntimeAcceptanceChecker) checkDeployment(ctx context.Context) error {
	checker.mu.Lock()
	if checker.closed || checker.deploymentFailed || checker.config.strict == nil {
		checker.mu.Unlock()
		return ErrRuntimeCertificationUnavailable
	}
	if checker.observation == nil {
		factory := checker.observe
		if factory == nil {
			factory = observeStrictRuntimeDeployment
		}
		observation, err := factory(checker.config.strict.topology)
		if err != nil || observation == nil {
			checker.deploymentFailed = true
			checker.mu.Unlock()
			if observation != nil {
				_ = observation.Close()
			}
			return ErrRuntimeCertificationUnavailable
		}
		checker.observation = observation
	}
	observation := checker.observation
	checker.mu.Unlock()
	if err := observation.Check(ctx); err != nil {
		checker.mu.Lock()
		checker.deploymentFailed = true
		checker.mu.Unlock()
		return ErrRuntimeCertificationUnavailable
	}
	return nil
}

func (checker *localRuntimeAcceptanceChecker) Close() error {
	if checker == nil {
		return nil
	}
	checker.mu.Lock()
	if checker.closed {
		checker.mu.Unlock()
		return nil
	}
	checker.closed = true
	observation := checker.observation
	checker.mu.Unlock()
	if observation != nil && observation.Close() != nil {
		return ErrRuntimeCertificationUnavailable
	}
	return nil
}

func observeStrictRuntimeDeployment(config runtimeevidence.ObservedDockerTopologyConfig) (runtimeDeploymentObservation, error) {
	// The v2 acceptance contract binds only ARM64 candidate publications.
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return nil, ErrRuntimeCertificationUnavailable
	}
	return runtimeevidence.NewObservedDockerTopology(config)
}
