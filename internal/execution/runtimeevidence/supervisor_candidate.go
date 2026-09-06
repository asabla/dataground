package runtimeevidence

import (
	"bytes"
	"context"
	"time"

	"github.com/asabla/dataground/internal/execution/openshell"
)

// Candidate selection is available only to explicit non-certifying diagnostics
// and package-owned tests; it cannot produce stock-profile evidence.
func selectRuntimeSupervisorCandidate(config DockerTopologyConfig, runner dockerTopologyRunner, binary string, gateway []byte) ([]byte, error) {
	image := config.supervisorCandidateImage
	if runner == nil || binary == "" || !commitmentPattern.MatchString(image) || bytes.Count(gateway, []byte(supervisorImage)) != 1 {
		return nil, ErrDockerTopologyConfiguration
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := runner.Run(ctx, runtimeTopologyDockerEnvironment(), binary, "image", "inspect", image, "--format", openshell.SupervisorCandidateInspectionFormat)
	if err != nil || ctx.Err() != nil || len(output) == 0 || len(output) > 4096 {
		return nil, ErrDockerTopologyConfiguration
	}
	defer clear(output)
	if !openshell.VerifySupervisorCandidateInspection(output, image) {
		return nil, ErrDockerTopologyConfiguration
	}
	return bytes.Replace(gateway, []byte(supervisorImage), []byte(image), 1), nil
}

type candidateTopologyBinding struct {
	image         string
	gatewaySHA256 string
}

func (topology *DockerTopology) candidateProfile() (candidateTopologyBinding, error) {
	if topology == nil || topology.state == nil {
		return candidateTopologyBinding{}, ErrDockerTopologyConfiguration
	}
	state := topology.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.workspace == nil || !state.started || !state.active || state.starting || state.failed || state.removed || state.cleaning || state.candidate.image == "" {
		return candidateTopologyBinding{}, ErrDockerTopologyOrder
	}
	content, err := readRuntimeTopologyFile(state.workspace.gatewayPath, state.candidate.gatewaySHA256)
	clear(content)
	if err != nil {
		return candidateTopologyBinding{}, err
	}
	return state.candidate, nil
}
