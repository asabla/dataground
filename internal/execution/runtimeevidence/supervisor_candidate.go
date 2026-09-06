package runtimeevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"
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
	output, err := runner.Run(ctx, runtimeTopologyDockerEnvironment(), binary, "image", "inspect", image, "--format", `{"id":{{json .Id}},"os":{{json .Os}},"source":{{json (index .Config.Labels "dataground.dev.supervisor-compatibility-source")}},"certification":{{json (index .Config.Labels "dataground.dev.certification-eligible")}}}`)
	if err != nil || ctx.Err() != nil || len(output) == 0 || len(output) > 4096 {
		return nil, ErrDockerTopologyConfiguration
	}
	defer clear(output)
	var value struct {
		ID, OS, Source, Certification string
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || value.ID != image || value.OS != "linux" || value.Source != "d556748771c41cbbd4e4dd7cd9030c798afe2b7d" || value.Certification != "false" {
		return nil, ErrDockerTopologyConfiguration
	}
	return bytes.Replace(gateway, []byte(supervisorImage), []byte(image), 1), nil
}
