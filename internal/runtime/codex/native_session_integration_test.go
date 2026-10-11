package codex_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
)

// The model stream is an upstream-supported offline fixture; the app-server,
// native thread state and DataGround adapter are real. This is compatibility
// evidence only, not model, credential, sandbox or release certification.
func TestNativeCodexSequentialSession(t *testing.T) {
	binary := os.Getenv("DATAGROUND_TEST_CODEX_SESSION_BINARY")
	if binary == "" {
		t.Skip("DATAGROUND_TEST_CODEX_SESSION_BINARY selects the pinned native app-server")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("native binary must be an absolute path")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	for _, path := range []string{home, workspace} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fixture := filepath.Join(root, "response.sse")
	environment := []string{"PATH=/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + home, "CODEX_RS_SSE_FIXTURE=" + fixture, "NO_COLOR=1"}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	version := exec.CommandContext(ctx, binary, "--version")
	version.Env, version.Dir = environment, workspace
	data, err := version.Output()
	if err != nil || strings.TrimSpace(string(data)) != "codex-cli 0.117.0" {
		t.Fatal("native binary must match the pinned Codex 0.117.0 version")
	}
	command := exec.CommandContext(ctx, binary,
		"-c", `model_provider="dataground_session_fixture"`,
		"-c", `model_providers.dataground_session_fixture.name="DataGround offline fixture"`,
		"-c", `model_providers.dataground_session_fixture.base_url="http://127.0.0.1:1"`,
		"-c", `model_providers.dataground_session_fixture.wire_api="responses"`,
		"-c", `model_providers.dataground_session_fixture.requires_openai_auth=false`,
		"-c", `model_providers.dataground_session_fixture.supports_websockets=false`,
		"app-server")
	command.Env, command.Dir, command.WaitDelay = environment, workspace, time.Second
	native, err := startNativeSessionProcess(command)
	if err != nil {
		t.Fatal("start native app-server")
	}
	defer native.Close()
	session, err := codex.NewSession(native)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var prior dgruntime.Turn
	for index := 1; index <= 3; index++ {
		text := fmt.Sprintf("Offline response %d.", index)
		writeSessionFixture(t, fixture, index, text)
		turn, err := session.StartTurn(ctx, dgruntime.StartRequest{Prompt: fmt.Sprintf("Fixture turn %d", index), WorkingDir: workspace, Model: "gpt-5.4"})
		if err != nil {
			t.Fatalf("start native turn %d: %v", index, err)
		}
		if prior != nil {
			if err := prior.Close(); err != nil {
				t.Fatal(err)
			}
			if err := prior.Interrupt(ctx); !errors.Is(err, dgruntime.ErrClosed) {
				t.Fatal("old turn retained interrupt authority")
			}
			if err := prior.Wait(ctx); err != nil {
				t.Fatal("old terminal result changed")
			}
		}
		events := observeNativeSessionTurn(t, ctx, turn)
		messages, usages := 0, 0
		for sequence, event := range events {
			if event.Sequence != uint64(sequence+1) {
				t.Fatal("native event sequence is not turn-local and ordered")
			}
			encoded, err := json.Marshal(event)
			if err != nil || bytes.Contains(encoded, []byte("private-native-item")) || bytes.Contains(encoded, []byte(root)) {
				t.Fatal("native routing state escaped normalization")
			}
			switch event.Type {
			case "output.message.completed":
				messages++
				if event.Payload["text"] != text || event.Payload["phase"] != "final" {
					t.Fatal("native completed message did not match this turn")
				}
			case "usage.session.recorded":
				usages++
				if event.Payload["totalTokens"] != 12*index {
					t.Fatalf("native cumulative usage mismatch on turn %d: %#v", index, event.Payload)
				}
			case "usage.recorded":
				t.Fatal("session total was reported as invocation usage")
			}
		}
		if messages != 1 || usages == 0 {
			types := make([]string, len(events))
			for i, event := range events {
				types[i] = event.Type
			}
			t.Fatalf("native turn %d message count %d, usage count %d, event types %v", index, messages, usages, types)
		}
		prior = turn
	}
	native.input.mu.Lock()
	defer native.input.mu.Unlock()
	if native.input.initializations != 1 || native.input.threads != 1 || native.input.turns != 3 || native.input.scopeChanged {
		t.Fatal("sequential turns did not use one initialized native thread")
	}
}

func writeSessionFixture(t *testing.T, path string, index int, text string) {
	t.Helper()
	item := map[string]any{"id": fmt.Sprintf("private-native-item-%d", index), "type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": text}}}
	frames := []any{
		map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("fixture-%d", index)}},
		map[string]any{"type": "response.output_item.added", "item": item},
		map[string]any{"type": "response.output_item.done", "item": item},
		map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("fixture-%d", index), "usage": map[string]any{"input_tokens": 10, "output_tokens": 2, "total_tokens": 12, "input_tokens_details": map[string]int{"cached_tokens": 0}, "output_tokens_details": map[string]int{"reasoning_tokens": 0}}}},
	}
	var content bytes.Buffer
	for _, frame := range frames {
		encoded, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		content.WriteString("data: ")
		content.Write(encoded)
		content.WriteByte('\n')
	}
	if err := os.WriteFile(path, content.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func observeNativeSessionTurn(t *testing.T, ctx context.Context, turn dgruntime.Turn) []dgruntime.Event {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- turn.Wait(ctx) }()
	events := []dgruntime.Event{}
	eventStream := turn.Events()
	appendEvent := func(event dgruntime.Event) {
		if len(events) >= 128 {
			t.Fatal("native turn exceeded the fixture event budget")
		}
		events = append(events, event)
	}
	for {
		select {
		case event, open := <-eventStream:
			if !open {
				eventStream = nil
				continue
			}
			appendEvent(event)
		case err := <-done:
			if err != nil {
				t.Fatalf("native turn completion: %v", err)
			}
			for {
				select {
				case event, open := <-eventStream:
					if !open {
						return events
					}
					appendEvent(event)
				default:
					return events
				}
			}
		case <-ctx.Done():
			t.Fatal("native session deadline exceeded")
		}
	}
}

type nativeSessionProcess struct {
	command        *exec.Cmd
	input          *nativeSessionInput
	output, errors io.ReadCloser
	done           chan struct{}
	waitErr        error
	closeOnce      sync.Once
}

func startNativeSessionProcess(command *exec.Cmd) (*nativeSessionProcess, error) {
	input, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		input.Close()
		output.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		input.Close()
		output.Close()
		stderr.Close()
		return nil, err
	}
	process := &nativeSessionProcess{command: command, input: &nativeSessionInput{WriteCloser: input}, output: output, errors: stderr, done: make(chan struct{})}
	go func() { process.waitErr = command.Wait(); close(process.done) }()
	return process, nil
}
func (p *nativeSessionProcess) Input() io.WriteCloser { return p.input }
func (p *nativeSessionProcess) Output() io.ReadCloser { return p.output }
func (p *nativeSessionProcess) Errors() io.ReadCloser { return p.errors }
func (p *nativeSessionProcess) Wait() error           { <-p.done; return p.waitErr }
func (p *nativeSessionProcess) Close() error {
	p.closeOnce.Do(func() {
		_ = p.command.Process.Kill()
		_ = p.input.Close()
		_ = p.output.Close()
		_ = p.errors.Close()
		<-p.done
	})
	return nil
}

type nativeSessionInput struct {
	io.WriteCloser
	mu                              sync.Mutex
	initializations, threads, turns int
	thread                          string
	scopeChanged                    bool
}

func (input *nativeSessionInput) Write(data []byte) (int, error) {
	var frame struct {
		Method string `json:"method"`
		Params struct {
			ThreadID string `json:"threadId"`
		} `json:"params"`
	}
	if json.Unmarshal(data, &frame) != nil {
		return 0, errors.New("invalid native input")
	}
	input.mu.Lock()
	switch frame.Method {
	case "initialize":
		input.initializations++
	case "thread/start":
		input.threads++
	case "turn/start":
		input.turns++
		if frame.Params.ThreadID == "" || (input.thread != "" && input.thread != frame.Params.ThreadID) {
			input.scopeChanged = true
		}
		input.thread = frame.Params.ThreadID
	}
	input.mu.Unlock()
	return input.WriteCloser.Write(data)
}
