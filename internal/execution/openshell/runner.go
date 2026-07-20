package openshell

import (
	"bytes"
	"context"
	"io"
	"os/exec"

	"github.com/asabla/dataground/internal/execution"
)

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, binary string, args ...string) (CommandResult, error) {
	command := exec.CommandContext(ctx, binary, args...)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	err := command.Run()
	result := CommandResult{Stdout: stdout.Bytes()}
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	return result, err
}

func (ExecRunner) Start(ctx context.Context, binary string, args ...string) (execution.RuntimeSession, error) {
	command := exec.CommandContext(ctx, binary, args...)
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
