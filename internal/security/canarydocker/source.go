package canarydocker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/asabla/dataground/internal/security/canarysource"
)

const (
	composeServiceLabel = "com.docker.compose.service"
	composeProjectLabel = "com.docker.compose.project"
	runLabel            = "dataground.dev/credential-evidence-run"
	gatewayLabel        = "dataground.dev/credential-evidence-gateway"
	providerLabel       = "dataground.dev/credential-evidence-provider"

	gatewayService      = "gateway"
	maxMetadataBytes    = 16 * 1024
	dockerMetadataLines = 8
)

var (
	ErrInvalidConfiguration = errors.New("invalid Docker credential source configuration")
	ErrCredentialSource     = errors.New("Docker credential evidence source unavailable")
	ErrSerialization        = errors.New("Docker credential source cannot be serialized")

	runIDPattern       = regexp.MustCompile(`^[0-9a-f]{32}$`)
	resourcePattern    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,126}[a-z0-9])?$`)
	containerIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	projectPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	imagePattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9./:_-]*@sha256:[0-9a-f]{64}$`)
)

const dockerMetadataFormat = `{{.Id}}{{println}}{{.Config.Image}}{{println}}{{.State.Running}}{{println}}{{index .Config.Labels "` +
	composeServiceLabel + `"}}{{println}}{{index .Config.Labels "` +
	composeProjectLabel + `"}}{{println}}{{index .Config.Labels "` +
	runLabel + `"}}{{println}}{{index .Config.Labels "` +
	gatewayLabel + `"}}{{println}}{{index .Config.Labels "` +
	providerLabel + `"}}`

const dockerArgumentsFormat = `{{json .Path}}{{println}}{{json .Args}}`

type Config struct {
	RunID          string
	GatewayName    string
	ProviderName   string
	ContainerID    string
	GatewayImage   string
	ComposeProject string
	DockerBinary   string
}

type Sources struct {
	state *state
}

type state struct {
	mu      sync.Mutex
	config  Config
	runner  dockerRunner
	next    int
	opening bool
	failed  bool
}

type containerSnapshot struct {
	id           string
	image        string
	running      bool
	service      string
	project      string
	runID        string
	gatewayName  string
	providerName string
}

type dockerRunner interface {
	Snapshot(context.Context, string, string) (containerSnapshot, error)
	Open(context.Context, bool, string, ...string) (io.ReadCloser, error)
}

func New(ctx context.Context, config Config) (*Sources, error) {
	if ctx == nil {
		return nil, ErrInvalidConfiguration
	}
	binary := config.DockerBinary
	if binary == "" {
		binary = "docker"
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	config.DockerBinary = filepath.Clean(resolved)
	return newWithRunner(ctx, config, execDockerRunner{})
}

func newWithRunner(ctx context.Context, config Config, runner dockerRunner) (*Sources, error) {
	if ctx == nil || isNil(runner) || !validConfig(config) {
		return nil, ErrInvalidConfiguration
	}
	snapshot, err := runner.Snapshot(ctx, config.DockerBinary, config.ContainerID)
	if err != nil || !snapshot.matches(config) {
		return nil, ErrInvalidConfiguration
	}
	return &Sources{state: &state{config: config, runner: runner}}, nil
}

func validConfig(config Config) bool {
	return runIDPattern.MatchString(config.RunID) &&
		resourcePattern.MatchString(config.GatewayName) &&
		resourcePattern.MatchString(config.ProviderName) &&
		containerIDPattern.MatchString(config.ContainerID) &&
		imagePattern.MatchString(config.GatewayImage) &&
		projectPattern.MatchString(config.ComposeProject) &&
		filepath.IsAbs(config.DockerBinary) &&
		filepath.Clean(config.DockerBinary) == config.DockerBinary
}

func (snapshot containerSnapshot) matches(config Config) bool {
	return snapshot.id == config.ContainerID &&
		snapshot.image == config.GatewayImage &&
		snapshot.running &&
		snapshot.service == gatewayService &&
		snapshot.project == config.ComposeProject &&
		snapshot.runID == config.RunID &&
		snapshot.gatewayName == config.GatewayName &&
		snapshot.providerName == config.ProviderName
}

func (sources *Sources) OpenProviderArguments(
	ctx context.Context,
	request canarysource.Request,
) (io.ReadCloser, error) {
	if sources == nil || sources.state == nil {
		return nil, ErrCredentialSource
	}
	return sources.open(ctx, request, 0, "provider-arguments", sources.state.config.ProviderName, []string{
		"inspect", "--type", "container", "--format", dockerArgumentsFormat,
		sources.state.config.ContainerID,
	})
}

func (sources *Sources) OpenGatewayLogs(
	ctx context.Context,
	request canarysource.Request,
) (io.ReadCloser, error) {
	if sources == nil || sources.state == nil {
		return nil, ErrCredentialSource
	}
	return sources.open(ctx, request, 1, "gateway-logs", sources.state.config.GatewayName, []string{
		"logs", "--timestamps", sources.state.config.ContainerID,
	})
}

func (sources *Sources) open(
	ctx context.Context,
	request canarysource.Request,
	index int,
	surface string,
	resourceName string,
	args []string,
) (io.ReadCloser, error) {
	if sources == nil || sources.state == nil || ctx == nil {
		return nil, ErrCredentialSource
	}
	state := sources.state
	state.mu.Lock()
	if state.failed ||
		state.opening ||
		state.next != index ||
		request.RunID != state.config.RunID ||
		request.Surface != surface ||
		request.ResourceName != resourceName {
		state.failed = true
		state.mu.Unlock()
		return nil, ErrCredentialSource
	}
	state.opening = true
	config := state.config
	runner := state.runner
	state.mu.Unlock()

	snapshot, err := runner.Snapshot(ctx, config.DockerBinary, config.ContainerID)
	if err != nil || !snapshot.matches(config) {
		state.finish(true)
		return nil, ErrCredentialSource
	}
	stream, err := runner.Open(ctx, surface == "gateway-logs", config.DockerBinary, args...)
	if err != nil || isNil(stream) {
		if !isNil(stream) {
			_ = stream.Close()
		}
		state.finish(true)
		return nil, ErrCredentialSource
	}
	return &sourceStream{source: stream, state: state}, nil
}

func (state *state) finish(failed bool) {
	state.mu.Lock()
	state.opening = false
	state.failed = state.failed || failed
	if !failed {
		state.next++
	}
	state.mu.Unlock()
}

type sourceStream struct {
	mu         sync.Mutex
	source     io.ReadCloser
	state      *state
	eof        bool
	readFailed bool
	closed     bool
	closeErr   error
}

func (stream *sourceStream) Read(buffer []byte) (int, error) {
	count, err := stream.source.Read(buffer)
	stream.mu.Lock()
	switch {
	case errors.Is(err, io.EOF):
		stream.eof = true
	case err != nil:
		stream.readFailed = true
	}
	stream.mu.Unlock()
	if err != nil && !errors.Is(err, io.EOF) {
		return count, ErrCredentialSource
	}
	return count, err
}

func (stream *sourceStream) Close() error {
	stream.mu.Lock()
	if stream.closed {
		err := stream.closeErr
		stream.mu.Unlock()
		return err
	}
	stream.closed = true
	complete := stream.eof && !stream.readFailed
	stream.mu.Unlock()

	closeErr := stream.source.Close()
	failed := !complete || closeErr != nil
	stream.state.finish(failed)

	stream.mu.Lock()
	if failed {
		stream.closeErr = ErrCredentialSource
	}
	err := stream.closeErr
	stream.mu.Unlock()
	return err
}

func (Sources) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

type execDockerRunner struct{}

func (execDockerRunner) Snapshot(
	ctx context.Context,
	binary string,
	containerID string,
) (containerSnapshot, error) {
	if ctx == nil || binary == "" || containerID == "" {
		return containerSnapshot{}, ErrCredentialSource
	}
	var output limitedBuffer
	output.remaining = maxMetadataBytes
	command := exec.CommandContext(
		ctx,
		binary,
		"inspect",
		"--type",
		"container",
		"--format",
		dockerMetadataFormat,
		containerID,
	)
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return containerSnapshot{}, ErrCredentialSource
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != dockerMetadataLines {
		return containerSnapshot{}, ErrCredentialSource
	}
	return containerSnapshot{
		id:           lines[0],
		image:        lines[1],
		running:      lines[2] == "true",
		service:      lines[3],
		project:      lines[4],
		runID:        lines[5],
		gatewayName:  lines[6],
		providerName: lines[7],
	}, nil
}

func (execDockerRunner) Open(
	ctx context.Context,
	includeStderr bool,
	binary string,
	args ...string,
) (io.ReadCloser, error) {
	if ctx == nil || binary == "" {
		return nil, ErrCredentialSource
	}
	commandContext, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	command := exec.CommandContext(commandContext, binary, args...)
	command.Stdout = writer
	if includeStderr {
		command.Stderr = writer
	}
	if err := command.Start(); err != nil {
		cancel()
		_ = reader.Close()
		_ = writer.Close()
		return nil, ErrCredentialSource
	}
	stream := &commandStream{
		reader: reader,
		cancel: cancel,
		done:   make(chan error, 1),
	}
	go func() {
		err := command.Wait()
		if err != nil {
			_ = writer.CloseWithError(ErrCredentialSource)
		} else {
			_ = writer.Close()
		}
		stream.done <- err
		close(stream.done)
	}()
	return stream, nil
}

type commandStream struct {
	mu     sync.Mutex
	reader *io.PipeReader
	cancel context.CancelFunc
	done   chan error
	eof    bool
	closed bool
}

func (stream *commandStream) Read(buffer []byte) (int, error) {
	count, err := stream.reader.Read(buffer)
	if errors.Is(err, io.EOF) {
		stream.mu.Lock()
		stream.eof = true
		stream.mu.Unlock()
	}
	return count, err
}

func (stream *commandStream) Close() error {
	stream.mu.Lock()
	if stream.closed {
		stream.mu.Unlock()
		return nil
	}
	stream.closed = true
	complete := stream.eof
	stream.mu.Unlock()

	if !complete {
		stream.cancel()
	}
	closeErr := stream.reader.Close()
	waitErr := <-stream.done
	stream.cancel()
	if !complete || closeErr != nil || waitErr != nil {
		return ErrCredentialSource
	}
	return nil
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	if len(value) > buffer.remaining {
		return 0, ErrCredentialSource
	}
	count, err := buffer.buffer.Write(value)
	buffer.remaining -= count
	return count, err
}

func (buffer *limitedBuffer) String() string {
	return buffer.buffer.String()
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var (
	_ canarysource.DockerSources = (*Sources)(nil)
	_ json.Marshaler             = (*Sources)(nil)
	_ dockerRunner               = execDockerRunner{}
)
