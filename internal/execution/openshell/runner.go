package openshell

import (
	"bytes"
	"context"
	"io"
	"os/exec"

	"github.com/asabla/dataground/internal/execution"
)

type ExecRunner struct {
	// Environment is an optional complete child environment. Nil preserves the
	// existing process environment; a non-nil slice is copied for every command.
	Environment []string
}

func (runner ExecRunner) Run(ctx context.Context, binary string, args ...string) (CommandResult, error) {
	command := runner.command(ctx, binary, args...)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	err := command.Run()
	result := CommandResult{Stdout: stdout.Bytes()}
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	return result, err
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
