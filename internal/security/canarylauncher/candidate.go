package canarylauncher

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"regexp"
	"time"
)

var candidateImagePattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// CheckCandidate runs the complete synthetic credential scan against one exact
// experimental image. It returns no serializable certification record and never
// acquires real provider credentials.
func CheckCandidate(ctx context.Context, config Config, image string) error {
	if ctx == nil || !candidateImagePattern.MatchString(image) {
		return ErrInvalidConfiguration
	}
	ctx, stop := context.WithTimeout(ctx, 10*time.Minute)
	defer stop()
	resolved, err := resolveConfig(config)
	if err != nil {
		return err
	}
	inspectionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(inspectionCtx, resolved.dockerBinary, "image", "inspect", image,
		"--format", `{"id":{{json .Id}},"user":{{json .Config.User}},"labels":{{json .Config.Labels}}}`)
	cmd.Env = dockerClientEnvironment()
	cmd.WaitDelay = time.Second
	var output candidateInspectionOutput
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil || !validCandidateInspection(output.Bytes(), image) {
		return ErrInvalidConfiguration
	}
	_, err = run(ctx, config, image)
	return err
}

func validCandidateInspection(data []byte, image string) bool {
	var inspection struct {
		ID     string            `json:"id"`
		User   string            `json:"user"`
		Labels map[string]string `json:"labels"`
	}
	return candidateImagePattern.MatchString(image) && json.Unmarshal(data, &inspection) == nil &&
		inspection.ID == image && inspection.User == "sandbox" &&
		inspection.Labels["dataground.dev.codex-compatibility-source"] == "4c70bff480af37b1bf1a9b352b8341060fe55755" &&
		inspection.Labels["dataground.dev.certification-eligible"] == "false"
}

type candidateInspectionOutput struct{ bytes.Buffer }

func (output *candidateInspectionOutput) Write(data []byte) (int, error) {
	if output.Len()+len(data) > 16<<10 {
		return 0, io.ErrShortBuffer
	}
	return output.Buffer.Write(data)
}
