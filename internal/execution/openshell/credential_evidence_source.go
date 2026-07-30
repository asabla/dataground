package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"regexp"
	"sync"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/security/canarysource"
)

const (
	credentialEvidenceOpenShellVersion  = "0.0.86"
	credentialEvidenceFilesystemProgram = `"use strict";
const fs = require("node:fs");
const path = require("node:path");
const root = "/";
const excludedDirectories = new Set(["/proc", "/sys"]);
const pending = [root];
const buffer = Buffer.alloc(64 * 1024);
const ignorableCodes = new Set(["EACCES", "EPERM", "ENOENT", "ENOTDIR", "ELOOP"]);

function isIgnorable(error) {
	return error !== null && typeof error === "object" && ignorableCodes.has(error.code);
}

function copyFile(file) {
	let descriptor;
	try {
		descriptor = fs.openSync(file, "r");
		for (;;) {
			const count = fs.readSync(descriptor, buffer, 0, buffer.length, null);
			if (count === 0) {
				return;
			}
			let written = 0;
			while (written < count) {
				written += fs.writeSync(1, buffer, written, count - written);
			}
		}
	} catch (error) {
		if (!isIgnorable(error)) {
			throw error;
		}
	} finally {
		if (descriptor !== undefined) {
			try {
				fs.closeSync(descriptor);
			} catch (error) {
				if (!isIgnorable(error)) {
					throw error;
				}
			}
		}
	}
}

while (pending.length > 0) {
	const directory = pending.pop();
	let entries;
	try {
		entries = fs.readdirSync(directory, { withFileTypes: true });
	} catch (error) {
		if (!isIgnorable(error)) {
			throw error;
		}
		continue;
	}
	entries.sort((left, right) => (left.name < right.name ? -1 : left.name > right.name ? 1 : 0));
	const children = [];
	for (const entry of entries) {
		const candidate = path.join(directory, entry.name);
		if (excludedDirectories.has(candidate)) {
			continue;
		}
		let stat;
		try {
			stat = fs.lstatSync(candidate);
		} catch (error) {
			if (!isIgnorable(error)) {
				throw error;
			}
			continue;
		}
		children.push({ path: candidate, stat });
	}
	for (const child of children) {
		if (child.stat.isFile()) {
			copyFile(child.path);
		}
	}
	for (let index = children.length - 1; index >= 0; index--) {
		if (children[index].stat.isDirectory()) {
			pending.push(children[index].path);
		}
	}
}`
)

var (
	ErrCredentialEvidenceSource        = errors.New("credential evidence source operation failed")
	ErrCredentialEvidenceSerialization = errors.New("credential evidence OpenShell source cannot be serialized")
	evidenceRunIDPattern               = regexp.MustCompile("^[a-f0-9]{32}$")
	evidenceResourceNamePattern        = regexp.MustCompile("^[a-z0-9][a-z0-9._-]{0,127}$")
)

func credentialEvidenceCommand(surface string) []string {
	switch surface {
	case "sandbox-process":
		return []string{
			"find", "/proc", "-mindepth", "2", "-maxdepth", "2", "-type", "f",
			"-name", "cmdline", "-readable", "-exec", "cat", "--", "{}", ";",
		}
	case "sandbox-environment":
		return []string{
			"find", "/proc", "-mindepth", "2", "-maxdepth", "2", "-type", "f",
			"-name", "environ", "-readable", "-exec", "cat", "--", "{}", ";",
		}
	case "sandbox-filesystem":
		return []string{"node", "-e", credentialEvidenceFilesystemProgram}
	case "sandbox-logs":
		return []string{
			"find", "/var/log", "-maxdepth", "1", "-type", "f",
			"-name", "openshell.*.log", "-readable", "-exec", "cat", "--", "{}", "+",
		}
	default:
		return nil
	}
}

func credentialEvidenceSurface(index int) string {
	switch index {
	case 0:
		return "sandbox-process"
	case 1:
		return "sandbox-environment"
	case 2:
		return "sandbox-filesystem"
	case 3:
		return "sandbox-logs"
	default:
		return ""
	}
}

// EvidenceStreamRunner starts one command and returns its stdout as a live
// stream. Implementations must report command failure through Read or Close
// without exposing stderr.
type EvidenceStreamRunner interface {
	Open(context.Context, string, ...string) (io.ReadCloser, error)
}

type CredentialEvidenceSourceConfig struct {
	RunID     string
	Execution execution.ExecutionRef
	Stream    EvidenceStreamRunner
}

// CredentialEvidenceSources binds the four sandbox-visible evidence streams
// to one persisted execution. Native sandbox and gateway coordinates remain
// private and are revalidated before every command.
type CredentialEvidenceSources struct {
	state *credentialEvidenceSourceState
}

type credentialEvidenceSourceState struct {
	mu       sync.Mutex
	provider *Provider
	runner   EvidenceStreamRunner
	runID    string
	target   credentialEvidenceTarget
	next     int
	opening  bool
	failed   bool
}

type credentialEvidenceTarget struct {
	isolationDomainID string
	executionID       string
	gatewayID         string
	endpoint          string
	sandboxName       string
	binary            string
}

func (provider *Provider) NewCredentialEvidenceSources(
	ctx context.Context,
	config CredentialEvidenceSourceConfig,
) (*CredentialEvidenceSources, error) {
	if provider == nil ||
		ctx == nil ||
		!evidenceRunIDPattern.MatchString(config.RunID) ||
		config.Execution.IsolationDomainID == "" ||
		!evidenceResourceNamePattern.MatchString(config.Execution.ID) {
		return nil, ErrCredentialEvidenceSource
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	runner := config.Stream
	if runner == nil {
		runner = ExecEvidenceStreamRunner{}
	} else if isNilEvidenceValue(runner) {
		return nil, ErrCredentialEvidenceSource
	}

	entry, gateway, err := provider.lookupExecution(ctx, config.Execution)
	if err != nil ||
		entry.Execution.IsolationDomainID != config.Execution.IsolationDomainID ||
		entry.Execution.ID != config.Execution.ID ||
		entry.Execution.GatewayID == "" ||
		entry.Execution.State != "ready" ||
		entry.SandboxName == "" ||
		gateway.Gateway.ID != entry.Execution.GatewayID ||
		gateway.Endpoint == "" ||
		provider.expected != credentialEvidenceOpenShellVersion {
		return nil, ErrCredentialEvidenceSource
	}
	if err := provider.Check(ctx); err != nil {
		return nil, ErrCredentialEvidenceSource
	}

	return &CredentialEvidenceSources{
		state: &credentialEvidenceSourceState{
			provider: provider,
			runner:   runner,
			runID:    config.RunID,
			target: credentialEvidenceTarget{
				isolationDomainID: config.Execution.IsolationDomainID,
				executionID:       config.Execution.ID,
				gatewayID:         entry.Execution.GatewayID,
				endpoint:          gateway.Endpoint,
				sandboxName:       entry.SandboxName,
				binary:            provider.binary,
			},
		},
	}, nil
}

func (sources *CredentialEvidenceSources) OpenSandboxProcess(
	ctx context.Context,
	request canarysource.Request,
) (io.ReadCloser, error) {
	return sources.open(ctx, request, "sandbox-process")
}

func (sources *CredentialEvidenceSources) OpenSandboxEnvironment(
	ctx context.Context,
	request canarysource.Request,
) (io.ReadCloser, error) {
	return sources.open(ctx, request, "sandbox-environment")
}

func (sources *CredentialEvidenceSources) OpenSandboxFilesystem(
	ctx context.Context,
	request canarysource.Request,
) (io.ReadCloser, error) {
	return sources.open(ctx, request, "sandbox-filesystem")
}

func (sources *CredentialEvidenceSources) OpenSandboxLogs(
	ctx context.Context,
	request canarysource.Request,
) (io.ReadCloser, error) {
	return sources.open(ctx, request, "sandbox-logs")
}

func (sources *CredentialEvidenceSources) open(
	ctx context.Context,
	request canarysource.Request,
	surface string,
) (io.ReadCloser, error) {
	if sources == nil || sources.state == nil || ctx == nil {
		return nil, ErrCredentialEvidenceSource
	}

	state := sources.state
	state.mu.Lock()
	if state.failed ||
		state.opening ||
		credentialEvidenceSurface(state.next) != surface ||
		request.RunID != state.runID ||
		request.Surface != surface ||
		request.ResourceName != state.target.executionID {
		state.mu.Unlock()
		return nil, ErrCredentialEvidenceSource
	}
	state.next++
	state.opening = true
	state.mu.Unlock()

	if err := ctx.Err(); err != nil {
		state.finishOpen(true)
		return nil, err
	}
	entry, gateway, err := state.provider.lookupExecution(
		ctx,
		execution.ExecutionRef{
			IsolationDomainID: state.target.isolationDomainID,
			ID:                state.target.executionID,
		},
	)
	if err != nil ||
		entry.Execution.IsolationDomainID != state.target.isolationDomainID ||
		entry.Execution.ID != state.target.executionID ||
		entry.Execution.GatewayID != state.target.gatewayID ||
		entry.Execution.State != "ready" ||
		entry.SandboxName != state.target.sandboxName ||
		gateway.Gateway.ID != state.target.gatewayID ||
		gateway.Endpoint != state.target.endpoint ||
		state.provider.binary != state.target.binary {
		state.finishOpen(true)
		return nil, ErrCredentialEvidenceSource
	}

	command := credentialEvidenceCommand(surface)
	args := state.provider.gatewayArgs(
		state.target.endpoint,
		append(
			[]string{
				"sandbox", "exec", "--name", state.target.sandboxName,
				"--no-tty", "--",
			},
			command...,
		)...,
	)
	stream, openErr := state.runner.Open(ctx, state.target.binary, args...)
	if openErr != nil || isNilEvidenceValue(stream) {
		if !isNilEvidenceValue(stream) {
			_ = stream.Close()
		}
		state.finishOpen(true)
		return nil, ErrCredentialEvidenceSource
	}
	return &credentialEvidenceStream{source: stream, state: state}, nil
}

func (state *credentialEvidenceSourceState) finishOpen(failed bool) {
	state.mu.Lock()
	state.opening = false
	state.failed = state.failed || failed
	state.mu.Unlock()
}

type credentialEvidenceStream struct {
	mu         sync.Mutex
	source     io.ReadCloser
	state      *credentialEvidenceSourceState
	eof        bool
	readFailed bool
	closed     bool
	closeErr   error
}

func (stream *credentialEvidenceStream) Read(buffer []byte) (int, error) {
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
		return count, ErrCredentialEvidenceSource
	}
	return count, err
}

func (stream *credentialEvidenceStream) Close() error {
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
	stream.state.finishOpen(failed)

	stream.mu.Lock()
	if failed {
		stream.closeErr = ErrCredentialEvidenceSource
	}
	err := stream.closeErr
	stream.mu.Unlock()
	return err
}

func (CredentialEvidenceSources) MarshalJSON() ([]byte, error) {
	return nil, ErrCredentialEvidenceSerialization
}

// ExecEvidenceStreamRunner invokes the pinned OpenShell CLI without a shell and
// exposes only stdout. The stream reports any non-zero or interrupted command
// as the package's sanitized source error when it is closed.
type ExecEvidenceStreamRunner struct{}

func (ExecEvidenceStreamRunner) Open(
	ctx context.Context,
	binary string,
	args ...string,
) (io.ReadCloser, error) {
	if ctx == nil || binary == "" {
		return nil, ErrCredentialEvidenceSource
	}
	commandContext, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(commandContext, binary, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return nil, ErrCredentialEvidenceSource
	}
	if err := command.Start(); err != nil {
		cancel()
		_ = stdout.Close()
		return nil, ErrCredentialEvidenceSource
	}
	return &evidenceCommandStream{
		stdout:  stdout,
		command: command,
		cancel:  cancel,
	}, nil
}

type evidenceCommandStream struct {
	mu      sync.Mutex
	stdout  io.ReadCloser
	command *exec.Cmd
	cancel  context.CancelFunc
	eof     bool
	closed  bool
}

func (stream *evidenceCommandStream) Read(buffer []byte) (int, error) {
	count, err := stream.stdout.Read(buffer)
	if errors.Is(err, io.EOF) {
		stream.mu.Lock()
		stream.eof = true
		stream.mu.Unlock()
	}
	return count, err
}

func (stream *evidenceCommandStream) Close() error {
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
	closeErr := stream.stdout.Close()
	waitErr := stream.command.Wait()
	stream.cancel()
	if closeErr != nil || waitErr != nil {
		return ErrCredentialEvidenceSource
	}
	return nil
}

func isNilEvidenceValue(value any) bool {
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
	_ canarysource.OpenShellSources = (*CredentialEvidenceSources)(nil)
	_ json.Marshaler                = (*CredentialEvidenceSources)(nil)
	_ EvidenceStreamRunner          = ExecEvidenceStreamRunner{}
)
