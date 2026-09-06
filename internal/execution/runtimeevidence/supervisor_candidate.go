package runtimeevidence

import (
	"bytes"
	"context"
	"time"

	"github.com/asabla/dataground/internal/execution/openshell"
)

// This selection is private to explicit package-owned candidate tests. The
// runtime evidence and diagnostic launchers cannot select it or report the
// default supervisor's evidence for a candidate topology.
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
