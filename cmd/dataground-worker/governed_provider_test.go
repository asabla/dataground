package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestGovernedAdapterRequiresTheMediatedProvider(t *testing.T) {
	t.Setenv("access_token", "host-secret-must-not-enter-protocol")
	for _, selected := range []string{"dataground_openshell_codex", "openai", ""} {
		t.Run("provider="+selected, func(t *testing.T) {
			requests, input := io.Pipe()
			output, responses := io.Pipe()
			session := &governedProviderTestSession{input: input, output: output, closed: make(chan struct{})}
			t.Cleanup(func() {
				session.Close()
				requests.Close()
				responses.Close()
			})
			done := make(chan error, 1)
			go func() {
				defer requests.Close()
				defer responses.Close()
				decoder, encoder := json.NewDecoder(requests), json.NewEncoder(responses)
				for _, method := range []string{"initialize", "initialized", "thread/start", "turn/start"} {
					var message struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
						Params map[string]any  `json:"params"`
					}
					if err := decoder.Decode(&message); err != nil {
						if method == "turn/start" && selected != "dataground_openshell_codex" && errors.Is(err, io.EOF) {
							done <- nil
						} else {
							done <- errors.New("native request missing")
						}
						return
					}
					if message.Method != method {
						done <- errors.New("unexpected native method")
						return
					}
					if method == "initialized" {
						continue
					}
					result := map[string]any{}
					if method == "thread/start" {
						raw, _ := json.Marshal(message.Params)
						if message.Params["modelProvider"] != "dataground_openshell_codex" || message.Params["config"] == nil || message.Params["model"] != "test-model" || strings.Contains(string(raw), "host-secret") {
							done <- errors.New("governed adapter did not select the fixed credential-free bridge")
							return
						}
						result = map[string]any{"modelProvider": selected, "thread": map[string]any{"id": "test-thread"}}
					}
					if method == "turn/start" {
						if selected != "dataground_openshell_codex" {
							done <- errors.New("provider substitution reached a native turn")
							return
						}
						result = map[string]any{"turn": map[string]any{"id": "test-turn"}}
					}
					if err := encoder.Encode(map[string]any{"id": message.ID, "result": result}); err != nil {
						done <- err
						return
					}
				}
				// Keep the transport open until the client releases it. An early EOF
				// could race with Start and obscure the selected-provider assertion.
				_, err := io.Copy(io.Discard, requests)
				done <- err
			}()
			client, err := (governedCodexRuntimeAdapterFactory{}).New(session)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "test", Model: "test-model"})
			if selected == "dataground_openshell_codex" {
				if err != nil || turn == nil {
					t.Fatal("fixed provider failed", err)
				}
			} else if turn != nil || !errors.Is(err, dgruntime.ErrProtocol) {
				t.Fatal("provider substitution accepted", err)
			}
			client.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("native peer did not stop")
			}
		})
	}
}

type governedProviderTestSession struct {
	input  *io.PipeWriter
	output *io.PipeReader
	closed chan struct{}
	once   sync.Once
}

func (s *governedProviderTestSession) Input() io.WriteCloser { return s.input }
func (s *governedProviderTestSession) Output() io.ReadCloser { return s.output }
func (*governedProviderTestSession) Errors() io.ReadCloser {
	return io.NopCloser(strings.NewReader(""))
}
func (s *governedProviderTestSession) Wait() error { <-s.closed; return nil }
func (s *governedProviderTestSession) Close() error {
	s.once.Do(func() { s.input.Close(); s.output.Close(); close(s.closed) })
	return nil
}
