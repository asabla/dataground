package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
)

func TestOpenShellClientSelectsOnlyTheMediatedProvider(t *testing.T) {
	t.Setenv("access_token", "host-secret-must-not-enter-protocol")
	t.Setenv("account_id", "host-account-must-not-enter-protocol")
	for _, selected := range []string{"dataground_openshell_codex", "openai", ""} {
		t.Run("selected="+selected, func(t *testing.T) {
			session := newScriptedSession(t, func(server *scriptServer) {
				initialize := server.read()
				server.requireMethod(initialize, "initialize")
				server.respond(initialize.ID, map[string]any{})
				server.requireMethod(server.read(), "initialized")
				thread := server.read()
				server.requireMethod(thread, "thread/start")
				var params map[string]any
				server.decodeParams(thread, &params)
				var expected map[string]any
				if err := json.Unmarshal([]byte(`{"model_providers":{"dataground_openshell_codex":{"name":"DataGround OpenShell Codex","base_url":"https://chatgpt.com/backend-api/codex","env_key":"access_token","env_http_headers":{"ChatGPT-Account-Id":"account_id"},"wire_api":"responses","requires_openai_auth":false,"supports_websockets":false}}}`), &expected); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(params["config"], expected) || params["modelProvider"] != "dataground_openshell_codex" || params["model"] != "selected-model" {
					t.Fatal("provider selection did not match the fixed bridge")
				}
				if params["approvalPolicy"] != "never" || params["sandbox"] != "read-only" || strings.Contains(string(thread.Raw), "host-secret") || strings.Contains(string(thread.Raw), "host-account") {
					t.Fatal("provider selection weakened isolation or read host credentials")
				}
				server.respond(thread.ID, map[string]any{"modelProvider": selected, "thread": map[string]any{"id": "native-thread"}})
				if selected != "dataground_openshell_codex" {
					if _, err := server.reader.ReadByte(); !errors.Is(err, io.EOF) {
						t.Fatal("provider mismatch reached a turn")
					}
					return
				}
				turn := server.read()
				server.requireMethod(turn, "turn/start")
				server.respond(turn.ID, map[string]any{"turn": map[string]any{"id": "native-turn"}})
			})
			client, err := codex.NewOpenShellConformance(session)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "hello", Model: "selected-model"})
			if selected == "dataground_openshell_codex" {
				if err != nil || turn == nil {
					t.Fatalf("explicit provider rejected: %v", err)
				}
			} else if turn != nil || !errors.Is(err, dgruntime.ErrProtocol) {
				t.Fatalf("provider substitution accepted: %v", err)
			}
			if err := session.scriptError(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
