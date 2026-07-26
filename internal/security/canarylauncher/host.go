package canarylauncher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/asabla/dataground/internal/security/canaryharness"
	"github.com/asabla/dataground/internal/security/canaryprofile"
)

const (
	maxTopologyFileBytes = 1 << 20
	maxCommandOutputBytes = 64 << 10
	gatewayReadyTimeout  = 2 * time.Minute
	gatewayPollInterval  = 500 * time.Millisecond
	gatewayStopTimeout   = 1 * time.Minute
)

var containerIDPattern = regexp.MustCompile("^[a-f0-9]{64}$")

type commandRunner interface {
	Run(context.Context, []string, string, ...string) ([]byte, error)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(
	ctx context.Context,
	environment []string,
	binary string,
	args ...string,
) ([]byte, error) {
	if ctx == nil || binary == "" {
		return nil, ErrLaunch
	}
	var output boundedBuffer
	output.remaining = maxCommandOutputBytes
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = append([]string(nil), environment...)
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		clear(output.buffer.Bytes())
		return nil, ErrLaunch
	}
	value := append([]byte(nil), output.buffer.Bytes()...)
	clear(output.buffer.Bytes())
	return value, nil
}

type composeHost struct {
	mu          sync.Mutex
	runner      commandRunner
	binary      string
	composeFile string
	project     string
	environment []string
	active      bool
	removed     bool
}

func newComposeHost(
	runID string,
	names canaryharness.ResourceNames,
	dockerBinary string,
	composeFile string,
	runner commandRunner,
) (*composeHost, error) {
	if !runIDPattern.MatchString(runID) ||
		names.Gateway == "" ||
		names.Provider == "" ||
		dockerBinary == "" ||
		composeFile == "" ||
		runner == nil {
		return nil, ErrInvalidConfiguration
	}
	return &composeHost{
		runner: runner,
		binary: dockerBinary,
		composeFile: composeFile,
		project: "dg_canary_" + runID,
		environment: dockerEnvironment(runID, names),
	}, nil
}

func (host *composeHost) Start(ctx context.Context) (string, error) {
	if host == nil || ctx == nil {
		return "", ErrInvalidConfiguration
	}
	host.mu.Lock()
	if host.active || host.removed {
		host.mu.Unlock()
		return "", ErrLaunch
	}
	host.mu.Unlock()

	_, err := host.runner.Run(
		ctx,
		host.environment,
		host.binary,
		"compose",
		"--project-name",
		host.project,
		"--file",
		host.composeFile,
		"up",
		"--detach",
		"--remove-orphans",
	)
	if err != nil {
		return "", ErrLaunch
	}
	host.mu.Lock()
	host.active = true
	host.mu.Unlock()

	if err := waitForGateway(ctx); err != nil {
		return "", ErrLaunch
	}
	output, err := host.runner.Run(
		ctx,
		host.environment,
		host.binary,
		"compose",
		"--project-name",
		host.project,
		"--file",
		host.composeFile,
		"ps",
		"--quiet",
		"gateway",
	)
	if err != nil {
		return "", ErrLaunch
	}
	containerID := strings.TrimSpace(string(output))
	clear(output)
	if !containerIDPattern.MatchString(containerID) {
		return "", ErrLaunch
	}
	return containerID, nil
}

func (host *composeHost) Stop(ctx context.Context) error {
	if host == nil || ctx == nil {
		return ErrInvalidConfiguration
	}
	host.mu.Lock()
	if host.removed {
		host.mu.Unlock()
		return nil
	}
	if !host.active {
		host.mu.Unlock()
		return nil
	}
	host.mu.Unlock()

	_, err := host.runner.Run(
		ctx,
		host.environment,
		host.binary,
		"compose",
		"--project-name",
		host.project,
		"--file",
		host.composeFile,
		"down",
		"--volumes",
		"--remove-orphans",
	)
	if err != nil {
		return ErrLaunch
	}
	host.mu.Lock()
	host.active = false
	host.removed = true
	host.mu.Unlock()
	return nil
}

func waitForGateway(ctx context.Context) error {
	readyCtx, cancel := context.WithTimeout(ctx, gatewayReadyTimeout)
	defer cancel()
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrLaunch
		},
	}
	ticker := time.NewTicker(gatewayPollInterval)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(readyCtx, http.MethodGet, canaryprofile.HealthEndpoint, nil)
		if err != nil {
			return ErrLaunch
		}
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
			closeErr := response.Body.Close()
			if response.StatusCode == http.StatusOK && closeErr == nil {
				return nil
			}
		}
		select {
		case <-readyCtx.Done():
			return ErrLaunch
		case <-ticker.C:
		}
	}
}

func dockerEnvironment(runID string, names canaryharness.ResourceNames) []string {
	keys := [...]string{
		"DOCKER_CERT_PATH",
		"DOCKER_CONFIG",
		"DOCKER_CONTEXT",
		"DOCKER_HOST",
		"DOCKER_TLS_VERIFY",
		"HOME",
		"PATH",
		"XDG_CONFIG_HOME",
	}
	environment := make([]string, 0, len(keys)+3)
	for _, key := range keys {
		if value, exists := os.LookupEnv(key); exists {
			environment = append(environment, key+"="+value)
		}
	}
	return append(
		environment,
		"DATAGROUND_CREDENTIAL_EVIDENCE_RUN_ID="+runID,
		"DATAGROUND_CREDENTIAL_EVIDENCE_GATEWAY="+names.Gateway,
		"DATAGROUND_CREDENTIAL_EVIDENCE_PROVIDER="+names.Provider,
	)
}

func resolveBinary(value string) (string, error) {
	if value == "" {
		return "", ErrInvalidConfiguration
	}
	resolved, err := exec.LookPath(value)
	if err != nil {
		return "", ErrInvalidConfiguration
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", ErrInvalidConfiguration
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", ErrInvalidConfiguration
	}
	return filepath.Clean(resolved), nil
}

func readVerifiedFile(path string, expectedDigest string) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) || len(expectedDigest) != 64 {
		return nil, ErrInvalidConfiguration
	}
	info, err := os.Lstat(path)
	if err != nil ||
		info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() ||
		info.Size() < 0 ||
		info.Size() > maxTopologyFileBytes {
		return nil, ErrTopologyDrift
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrTopologyDrift
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxTopologyFileBytes+1))
	if err != nil || len(content) > maxTopologyFileBytes {
		clear(content)
		return nil, ErrTopologyDrift
	}
	digest := sha256Hex(content)
	if !constantTimeEqual(digest, expectedDigest) {
		clear(content)
		return nil, ErrTopologyDrift
	}
	return content, nil
}

func sha256Hex(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func constantTimeEqual(left string, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

type boundedBuffer struct {
	buffer bytes.Buffer
	remaining int
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	if len(value) > buffer.remaining {
		return 0, ErrLaunch
	}
	count, err := buffer.buffer.Write(value)
	buffer.remaining -= count
	return count, err
}

func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), gatewayStopTimeout)
}

var _ commandRunner = execCommandRunner{}
