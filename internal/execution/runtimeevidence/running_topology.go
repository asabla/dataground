package runtimeevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

// Inspect only the fields needed for validation. Untrusted process environment
// values remain in bounded memory and never enter returned errors or evidence.
const runningGatewayInspection = `{"id":{{json .Id}},"image":{{json .Config.Image}},"imageId":{{json .Image}},"running":{{json .State.Running}},"paused":{{json .State.Paused}},"restarting":{{json .State.Restarting}},"dead":{{json .State.Dead}},"pid":{{json .State.Pid}},"startedAt":{{json .State.StartedAt}},"user":{{json .Config.User}},"entrypoint":{{json .Config.Entrypoint}},"cmd":{{json .Config.Cmd}},"env":{{json .Config.Env}},"network":{{json .HostConfig.NetworkMode}},"privileged":{{json .HostConfig.Privileged}},"pidMode":{{json .HostConfig.PidMode}},"usernsMode":{{json .HostConfig.UsernsMode}},"capAdd":{{json .HostConfig.CapAdd}},"securityOpt":{{json .HostConfig.SecurityOpt}},"groupAdd":{{json .HostConfig.GroupAdd}},"mounts":[{{range $i,$m := .Mounts}}{{if $i}},{{end}}{"type":{{json $m.Type}},"source":{{json $m.Source}},"destination":{{json $m.Destination}},"rw":{{json $m.RW}}}{{end}}]}`
const runningGatewayImageInspection = `{"id":{{json .Id}},"os":{{json .Os}},"repoDigests":{{json .RepoDigests}},"entrypoint":{{json .Config.Entrypoint}},"env":{{json .Config.Env}}}`

type runningGatewayMount struct {
	Type        string `json:"type"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	RW          bool   `json:"rw"`
}
type runningGateway struct {
	ID          string                `json:"id"`
	Image       string                `json:"image"`
	ImageID     string                `json:"imageId"`
	StartedAt   string                `json:"startedAt"`
	User        string                `json:"user"`
	Network     string                `json:"network"`
	PIDMode     string                `json:"pidMode"`
	UsernsMode  string                `json:"usernsMode"`
	Running     bool                  `json:"running"`
	Paused      bool                  `json:"paused"`
	Restarting  bool                  `json:"restarting"`
	Dead        bool                  `json:"dead"`
	Privileged  bool                  `json:"privileged"`
	PID         int                   `json:"pid"`
	Entrypoint  []string              `json:"entrypoint"`
	Cmd         []string              `json:"cmd"`
	Env         []string              `json:"env"`
	CapAdd      []string              `json:"capAdd"`
	SecurityOpt []string              `json:"securityOpt"`
	GroupAdd    []string              `json:"groupAdd"`
	Mounts      []runningGatewayMount `json:"mounts"`
}
type runningGatewayImage struct {
	ID          string   `json:"id"`
	OS          string   `json:"os"`
	RepoDigests []string `json:"repoDigests"`
	Entrypoint  []string `json:"entrypoint"`
	Env         []string `json:"env"`
}

func decodeTopologyInspection(content []byte, target any) error {
	if len(content) == 0 || len(content) > runtimeTopologyMaxOutputBytes {
		return ErrDockerTopologyDrift
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(content, &fields) != nil {
		return ErrDockerTopologyDrift
	}
	required := []string{"id", "os", "repoDigests", "entrypoint", "env"}
	if _, ok := target.(*runningGateway); ok {
		required = []string{"id", "image", "imageId", "startedAt", "user", "network", "pidMode", "usernsMode", "running", "paused", "restarting", "dead", "privileged", "pid", "entrypoint", "cmd", "env", "capAdd", "securityOpt", "groupAdd", "mounts"}
		var mounts []map[string]json.RawMessage
		if json.Unmarshal(fields["mounts"], &mounts) != nil {
			return ErrDockerTopologyDrift
		}
		for _, mount := range mounts {
			if len(mount) != 4 {
				return ErrDockerTopologyDrift
			}
			for _, key := range []string{"type", "source", "destination", "rw"} {
				if len(mount[key]) == 0 || string(mount[key]) == "null" {
					return ErrDockerTopologyDrift
				}
			}
		}
	}
	if len(fields) != len(required) {
		return ErrDockerTopologyDrift
	}
	for _, key := range required {
		raw, ok := fields[key]
		if !ok || (string(raw) == "null" && !slices.Contains([]string{"cmd", "capAdd", "securityOpt", "env"}, key)) {
			return ErrDockerTopologyDrift
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrDockerTopologyDrift
	}
	return nil
}

func (state *dockerTopologyState) verifyRunningConfiguration(ctx context.Context, containerID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := state.runner.Run(ctx, state.environment, state.binary, "inspect", "--format", runningGatewayInspection, containerID)
	if err != nil {
		return "", ErrDockerTopologyDrift
	}
	defer clear(output)
	var value runningGateway
	if decodeTopologyInspection(output, &value) != nil {
		return "", ErrDockerTopologyDrift
	}
	imageOutput, err := state.runner.Run(ctx, state.environment, state.binary, "image", "inspect", "--format", runningGatewayImageInspection, gatewayImage)
	if err != nil {
		return "", ErrDockerTopologyDrift
	}
	defer clear(imageOutput)
	var image runningGatewayImage
	if decodeTopologyInspection(imageOutput, &image) != nil {
		return "", ErrDockerTopologyDrift
	}
	if err := state.validateRunningConfiguration(containerID, value, image); err != nil {
		return "", err
	}
	if err := state.verifyFrozenTopology(); err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return "", errors.Join(ErrDockerTopologyDrift, ctx.Err())
	}
	return value.StartedAt, nil
}

func (state *dockerTopologyState) validateRunningConfiguration(containerID string, value runningGateway, image runningGatewayImage) error {
	if state.workspace == nil || value.ID != containerID || value.Image != gatewayImage || !commitmentPattern.MatchString(value.ImageID) || image.ID != value.ImageID || image.OS != "linux" || !slices.Contains(image.RepoDigests, gatewayImage) ||
		!value.Running || value.Paused || value.Restarting || value.Dead || value.PID <= 0 || value.Network != "host" || value.Privileged || value.PIDMode != "" || value.UsernsMode != "" || len(value.CapAdd) != 0 || len(value.SecurityOpt) != 0 || !slices.Equal(value.Cmd, []string{"--config", "/etc/openshell/gateway.toml"}) || len(image.Entrypoint) == 0 || !slices.Equal(value.Entrypoint, image.Entrypoint) {
		return ErrDockerTopologyDrift
	}
	started, err := time.Parse(time.RFC3339Nano, value.StartedAt)
	if err != nil || started.IsZero() || started.After(time.Now()) || (state.startedAt != "" && value.StartedAt != state.startedAt) {
		return ErrDockerTopologyDrift
	}
	configured, ok := topologyEnvironmentMap(state.environment)
	if !ok {
		return ErrDockerTopologyDrift
	}
	if value.User != configured["DATAGROUND_RUNTIME_CONFORMANCE_UID"]+":"+configured["DATAGROUND_RUNTIME_CONFORMANCE_GID"] || !slices.Equal(value.GroupAdd, []string{configured["DATAGROUND_RUNTIME_CONFORMANCE_DOCKER_GID"]}) {
		return ErrDockerTopologyDrift
	}
	expected, ok := topologyEnvironmentMap(image.Env)
	if !ok {
		return ErrDockerTopologyDrift
	}
	expected["OPENSHELL_GATEWAY_CONFIG"] = "/etc/openshell/gateway.toml"
	expected["OPENSHELL_DB_URL"] = "sqlite:" + state.workspace.statePath + "/gateway.db?mode=rwc"
	expected["XDG_DATA_HOME"] = state.workspace.statePath
	expected["HOME"] = state.workspace.statePath
	actual, ok := topologyEnvironmentMap(value.Env)
	if !ok || len(actual) != len(expected) {
		return ErrDockerTopologyDrift
	}
	for name, want := range expected {
		if actual[name] != want {
			return ErrDockerTopologyDrift
		}
	}
	mounts := map[string]runningGatewayMount{
		"/var/run/docker.sock":                            {Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock", RW: true},
		state.workspace.statePath:                         {Type: "bind", Source: state.workspace.statePath, Destination: state.workspace.statePath, RW: true},
		"/run/dataground-runtime-conformance/gateway-jwt": {Type: "bind", Source: state.workspace.jwtPath, Destination: "/run/dataground-runtime-conformance/gateway-jwt"},
		"/etc/openshell/gateway.toml":                     {Type: "bind", Source: state.workspace.gatewayPath, Destination: "/etc/openshell/gateway.toml"},
	}
	if len(value.Mounts) != len(mounts) {
		return ErrDockerTopologyDrift
	}
	for _, mount := range value.Mounts {
		if expected, ok := mounts[mount.Destination]; !ok || mount != expected {
			return ErrDockerTopologyDrift
		}
		delete(mounts, mount.Destination)
	}
	return nil
}
func topologyEnvironmentMap(values []string) (map[string]string, bool) {
	result := make(map[string]string, len(values))
	for _, value := range values {
		name, content, found := strings.Cut(value, "=")
		if !found || name == "" {
			return nil, false
		}
		if _, duplicate := result[name]; duplicate {
			return nil, false
		}
		result[name] = content
	}
	return result, true
}
func (state *dockerTopologyState) verifyFrozenTopology() error {
	workspace := state.workspace
	if workspace == nil {
		return ErrDockerTopologyDrift
	}
	openedRoot, err := workspace.parent.Stat()
	if err != nil {
		return ErrDockerTopologyDrift
	}
	for _, directory := range []struct {
		path string
		info os.FileInfo
	}{
		{workspace.root, openedRoot}, {workspace.path, workspace.directoryInfo}, {workspace.statePath, workspace.stateInfo}, {workspace.jwtPath, workspace.jwtInfo},
	} {
		current, err := os.Lstat(directory.path)
		if err != nil || !safeRuntimeTopologyDirectory(current) || !os.SameFile(current, directory.info) {
			return ErrDockerTopologyDrift
		}
	}
	digest := runtimeGatewayConfigSHA256
	if state.candidate.image != "" {
		digest = state.candidate.gatewaySHA256
	}
	for _, file := range []struct {
		path, digest string
		info         os.FileInfo
	}{
		{state.workspace.composePath, runtimeComposeSHA256, state.workspace.composeInfo},
		{state.workspace.gatewayPath, digest, state.workspace.gatewayInfo},
	} {
		content, err := readRuntimeTopologyFile(file.path, file.digest)
		clear(content)
		current, statErr := os.Lstat(file.path)
		if err != nil || statErr != nil || !os.SameFile(file.info, current) || !safeRuntimeTopologyFile(current) || !current.ModTime().Equal(file.info.ModTime()) {
			return ErrDockerTopologyDrift
		}
	}
	return nil
}

// Check re-observes the owned gateway without creating or restarting it. A
// replacement, restart or drift poisons this run while retaining cleanup authority.
func (topology *DockerTopology) Check(ctx context.Context) error {
	if topology == nil || topology.state == nil || ctx == nil {
		return ErrDockerTopologyConfiguration
	}
	state := topology.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.started || state.starting || !state.active || state.failed || state.cleaning || state.removed {
		return ErrDockerTopologyOrder
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	id, err := state.observeContainer(ctx)
	if err == nil && id != state.containerID {
		err = ErrDockerTopologyDrift
	}
	if err == nil {
		err = state.verifyContainer(ctx, id)
	}
	if err == nil {
		_, err = state.verifyRunningConfiguration(ctx, id)
	}
	if err != nil {
		state.failed = true
		return errors.Join(ErrDockerTopologyDrift, ctx.Err())
	}
	return nil
}
