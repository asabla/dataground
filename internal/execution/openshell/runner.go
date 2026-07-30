package openshell

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"

	"github.com/asabla/dataground/internal/execution"
)

const maxNativeDiagnosticBytes = 64 << 10

type diagnosticBuffer struct {
	buffer    bytes.Buffer
	remaining int
	overflow  bool
}

func (buffer *diagnosticBuffer) Write(value []byte) (int, error) {
	accepted := len(value)
	if len(value) > buffer.remaining {
		value = value[:buffer.remaining]
		buffer.overflow = true
	}
	count, err := buffer.buffer.Write(value)
	buffer.remaining -= count
	if err != nil {
		return count, err
	}
	return accepted, nil
}

type ExecRunner struct {
	// Environment is an optional complete child environment. Nil preserves the
	// existing process environment; a non-nil slice is copied for every command.
	Environment []string
}

func (runner ExecRunner) Run(ctx context.Context, binary string, args ...string) (CommandResult, error) {
	command := runner.command(ctx, binary, args...)
	var stdout bytes.Buffer
	stderr := diagnosticBuffer{remaining: maxNativeDiagnosticBytes}
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := CommandResult{Stdout: stdout.Bytes()}
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	if err != nil || result.ExitCode != 0 {
		result.FailureClass = classifyNativeFailure(
			stdout.Bytes(),
			stderr.buffer.Bytes(),
			stderr.overflow,
		)
	}
	clear(stderr.buffer.Bytes())
	return result, err
}

func classifyNativeFailure(stdout []byte, stderr []byte, overflow bool) NativeFailureClass {
	if overflow {
		return NativeFailureOverflow
	}
	message := strings.ToLower(
		stripTerminalEscapes(string(append(append([]byte(nil), stdout...), stderr...))),
	)
	switch {
	case isArgumentFailure(message):
		switch {
		case strings.Contains(message, "--gateway-endpoint"),
			strings.Contains(message, "http://127.0.0.1:8080"):
			return NativeFailureArgumentGateway
		case strings.Contains(message, "--name"),
			strings.Contains(message, "sandbox name"):
			return NativeFailureArgumentName
		case strings.Contains(message, "--from"),
			strings.Contains(message, "ghcr.io/nvidia/openshell-community/sandboxes/base"):
			return NativeFailureArgumentImage
		case strings.Contains(message, "--policy"),
			strings.Contains(message, "dataground-enforcement-"):
			return NativeFailureArgumentPolicy
		case strings.Contains(message, "--no-auto-providers"):
			return NativeFailureArgumentAutoProvider
		case strings.Contains(message, "--approval-mode"),
			strings.Contains(message, "manual"):
			return NativeFailureArgumentApproval
		case strings.Contains(message, "--label"),
			strings.Contains(message, "dataground.managed"),
			strings.Contains(message, "dataground.operation"),
			strings.Contains(message, "dataground.isolation"),
			strings.Contains(message, "dataground.execution"),
			strings.Contains(message, "dataground.create"):
			return NativeFailureArgumentLabel
		case strings.Contains(message, "--provider"),
			strings.Contains(message, "dg-canary-provider-"),
			strings.Contains(message, "provider"):
			return NativeFailureArgumentProvider
		case strings.Contains(message, "--label"),
			strings.Contains(message, "label"):
			return NativeFailureArgumentLabel
		case strings.Contains(message, "--policy"),
			strings.Contains(message, "policy"):
			return NativeFailureArgumentPolicy
		case strings.Contains(message, "--from"),
			strings.Contains(message, "image"),
			strings.Contains(message, "template"):
			return NativeFailureArgumentImage
		case strings.Contains(message, "--name"),
			strings.Contains(message, "name"):
			return NativeFailureArgumentName
		case strings.Contains(message, "true"),
			strings.Contains(message, "command"):
			return NativeFailureArgumentCommand
		default:
			return NativeFailureArgument
		}
	case strings.Contains(message, "unauthenticated"),
		strings.Contains(message, "authentication"),
		strings.Contains(message, "certificate"),
		strings.Contains(message, "tls"):
		return NativeFailureAuth
	case strings.Contains(message, "already exists"),
		strings.Contains(message, "conflict"):
		return NativeFailureConflict
	case strings.Contains(message, "database"),
		strings.Contains(message, "sqlite"),
		strings.Contains(message, "sandbox token write failed"),
		strings.Contains(message, "sandboxtokenwritefailed"):
		return NativeFailureStorage
	case strings.Contains(message, "permission denied"),
		strings.Contains(message, "operation not permitted"),
		strings.Contains(message, "read-only file system"):
		return NativeFailurePermission
	case strings.Contains(message, "compute driver"),
		strings.Contains(message, "driver rejected"),
		strings.Contains(message, "containercreatefailed"),
		strings.Contains(message, "containerstartfailed"),
		strings.Contains(message, "container exited"),
		strings.Contains(message, "container is dead"),
		strings.Contains(message, "provisioning stream ended"),
		strings.Contains(message, "sandbox entered error phase"),
		strings.Contains(message, "failed precondition"),
		strings.Contains(message, "failedprecondition"),
		strings.Contains(message, "status: internal"):
		return NativeFailureDriver
	case strings.Contains(message, "supervisor"),
		strings.Contains(message, "websocket"),
		strings.Contains(message, "ssh session"),
		strings.Contains(message, "ssh exited with status"),
		strings.Contains(message, "exec failed"),
		strings.Contains(message, "command failed"):
		return NativeFailureSupervisor
	case strings.Contains(message, "no such file"),
		strings.Contains(message, "does not exist"),
		strings.Contains(message, "not found"):
		return NativeFailureMissing
	case strings.Contains(message, "image"),
		strings.Contains(message, "manifest"),
		strings.Contains(message, "pull"):
		return NativeFailureImage
	case strings.Contains(message, "provider"),
		strings.Contains(message, "credential"):
		return NativeFailureProvider
	case strings.Contains(message, "policy"),
		strings.Contains(message, "prover"):
		return NativeFailurePolicy
	case strings.Contains(message, "network"),
		strings.Contains(message, "connection"),
		strings.Contains(message, "host.openshell.internal"),
		strings.Contains(message, "status: unavailable"),
		strings.Contains(message, "transport error"):
		return NativeFailureNetwork
	case strings.Contains(message, "timeout"),
		strings.Contains(message, "timed out"),
		strings.Contains(message, "deadline exceeded"),
		strings.Contains(message, "status: cancelled"):
		return NativeFailureTimeout
	default:
		return NativeFailureUnknown
	}
}

func stripTerminalEscapes(message string) string {
	plain := make([]byte, 0, len(message))
	for index := 0; index < len(message); index++ {
		if message[index] != 0x1b || index+1 >= len(message) || message[index+1] != '[' {
			plain = append(plain, message[index])
			continue
		}
		index += 2
		for index < len(message) {
			value := message[index]
			if value >= 0x40 && value <= 0x7e {
				break
			}
			index++
		}
	}
	return string(plain)
}

func isArgumentFailure(message string) bool {
	return strings.Contains(message, "unexpected argument") ||
		strings.Contains(message, "invalid argument") ||
		strings.Contains(message, "invalidargument") ||
		strings.Contains(message, "invalid value") ||
		strings.Contains(message, "required arguments") ||
		strings.Contains(message, "usage:")
}

func (runner ExecRunner) Start(ctx context.Context, binary string, args ...string) (execution.RuntimeSession, error) {
	command := runner.command(ctx, binary, args...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &processSession{command: command, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

func (runner ExecRunner) command(ctx context.Context, binary string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, binary, args...)
	if runner.Environment != nil {
		command.Env = append([]string(nil), runner.Environment...)
	}
	return command
}

type processSession struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
}

func (session *processSession) Input() io.WriteCloser { return session.stdin }
func (session *processSession) Output() io.ReadCloser { return session.stdout }
func (session *processSession) Errors() io.ReadCloser { return session.stderr }
func (session *processSession) Wait() error           { return session.command.Wait() }
func (session *processSession) Close() error {
	_ = session.stdin.Close()
	if session.command.Process == nil {
		return nil
	}
	return session.command.Process.Kill()
}
