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

	"github.com/asabla/dataground/internal/domain"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
)

func nativeQuestionParams() map[string]any {
	return map[string]any{"threadId": "native-thread", "turnId": "native-turn", "itemId": "native-item", "questions": []map[string]any{
		{"id": "native-choice", "header": "Destination", "question": "Choose the destination.", "isOther": false, "options": []map[string]string{{"label": "Local", "description": "Use local storage."}, {"label": "Remote", "description": "Use remote storage."}}},
		{"id": "native-context", "header": "Context", "question": "Add context.", "isOther": true, "options": nil},
	}}
}
func completeQuestionStart(server *scriptServer) {
	initialize := server.read()
	server.requireMethod(initialize, "initialize")
	var params struct {
		Capabilities struct {
			ExperimentalAPI bool `json:"experimentalApi"`
		} `json:"capabilities"`
	}
	server.decodeParams(initialize, &params)
	if !params.Capabilities.ExperimentalAPI {
		server.t.Fatal("question mode did not opt into the pinned experimental protocol")
	}
	server.respond(initialize.ID, map[string]any{})
	server.requireMethod(server.read(), "initialized")
	thread := server.read()
	server.requireMethod(thread, "thread/start")
	server.respond(thread.ID, map[string]any{"thread": map[string]any{"id": "native-thread"}})
	turn := server.read()
	server.requireMethod(turn, "turn/start")
	server.notify("turn/started", map[string]any{"threadId": "native-thread", "turn": map[string]any{"id": "native-turn", "status": "inProgress"}})
	server.respond(turn.ID, map[string]any{"turn": map[string]any{"id": "native-turn"}})
}
func TestQuestionAnswersUseFrozenChoicesAndHideNativeIdentifiers(t *testing.T) {
	response := make(chan wireRecord, 1)
	session := newScriptedSession(t, func(server *scriptServer) {
		completeQuestionStart(server)
		server.request("native-request", "item/tool/requestUserInput", nativeQuestionParams())
		response <- server.read()
		server.notify("serverRequest/resolved", map[string]any{"threadId": "native-thread", "requestId": "native-request"})
		server.notify("turn/completed", map[string]any{"threadId": "native-thread", "turn": map[string]any{"id": "native-turn", "status": "completed"}})
	})
	client, err := codex.New(session)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "inspect", QuestionMode: dgruntime.QuestionInteractive, QuestionTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	question := waitForEvent(t, turn.Events(), "interaction.question.requested")
	encoded, _ := json.Marshal(question)
	if strings.Contains(string(encoded), "native-") {
		t.Fatalf("native IDs escaped normalization: %s", encoded)
	}
	prompts := question.Payload["questions"].([]domain.QuestionPrompt)
	if len(prompts) != 2 || prompts[0].Multiple || prompts[0].AllowFreeText || !prompts[1].AllowFreeText {
		t.Fatal("question semantics changed")
	}
	expires, err := time.Parse(time.RFC3339Nano, question.Payload["expiresAt"].(string))
	if err != nil || !expires.After(time.Now()) {
		t.Fatal("question has no bounded expiry")
	}
	prompts[0].Options[0].Label = "tampered display copy"
	questionTurn := turn.(dgruntime.QuestionTurn)
	if err := questionTurn.AnswerQuestion(ctx, "question-1", nil); !errors.Is(err, domain.ErrQuestionInvalid) {
		t.Fatal("missing answers were accepted")
	}
	select {
	case <-response:
		t.Fatal("question selected an answer before explicit input")
	case <-time.After(10 * time.Millisecond):
	}
	text := "Explicit context"
	answers := []domain.QuestionAnswer{{QuestionID: "item_1", OptionIDs: []string{"option_1"}}, {QuestionID: "item_2", Text: &text}}
	if err := questionTurn.AnswerQuestion(ctx, "question-1", answers); err != nil {
		t.Fatal(err)
	}
	var received wireRecord
	select {
	case received = <-response:
	case <-ctx.Done():
		t.Fatal("answer was not delivered")
	}
	var result struct {
		Answers map[string]struct {
			Answers []string `json:"answers"`
		} `json:"answers"`
	}
	if json.Unmarshal(received.Result, &result) != nil || string(received.ID) != `"native-request"` || !reflect.DeepEqual(result.Answers["native-choice"].Answers, []string{"Local"}) || !reflect.DeepEqual(result.Answers["native-context"].Answers, []string{"Explicit context"}) {
		t.Fatalf("answer did not bind frozen native request: %s", received.Raw)
	}
	if err := questionTurn.AnswerQuestion(ctx, "question-1", answers); !errors.Is(err, dgruntime.ErrQuestionNotFound) {
		t.Fatal("duplicate answer was accepted")
	}
	if err := turn.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestQuestionRequestsFailClosedWithoutRepresentableNonSecretContent(t *testing.T) {
	for _, name := range []string{"disabled", "secret", "scope", "unknown-field", "invalid-boolean", "one-option", "duplicate-item", "too-many-items", "wrong-case-field", "missing-description", "duplicate-field"} {
		t.Run(name, func(t *testing.T) {
			response := make(chan wireRecord, 1)
			session := newScriptedSession(t, func(server *scriptServer) {
				if name == "disabled" {
					server.completeStart("native-thread", "native-turn")
				} else {
					completeQuestionStart(server)
				}
				params := nativeQuestionParams()
				questions := params["questions"].([]map[string]any)
				switch name {
				case "secret":
					questions[0]["isSecret"] = true
					questions[0]["question"] = "private secret prompt"
				case "scope":
					params["threadId"] = "other-thread"
				case "unknown-field":
					questions[0]["unknownPermission"] = true
				case "invalid-boolean":
					questions[0]["isSecret"] = nil
				case "one-option":
					questions[0]["options"] = []map[string]string{{"label": "Only", "description": "Only option"}}
				case "duplicate-item":
					questions[1]["id"] = questions[0]["id"]
				case "wrong-case-field":
					questions[0]["IsSecret"] = false
				case "missing-description":
					questions[0]["options"] = []map[string]string{{"label": "Local"}, {"label": "Remote", "description": "Remote storage"}}
				case "too-many-items":
					params["questions"] = append(questions, questions...)
				}
				if name == "duplicate-field" {
					encoded, _ := json.Marshal(params)
					encoded = []byte(strings.Replace(string(encoded), `"isOther":false`, `"isSecret":true,"isSecret":false,"isOther":false`, 1))
					server.writeRaw([]byte(`{"id":"native-request","method":"item/tool/requestUserInput","params":` + string(encoded) + "}\n"))
				} else {
					server.request("native-request", "item/tool/requestUserInput", params)
				}
				response <- server.read()
				server.notify("turn/completed", map[string]any{"threadId": "native-thread", "turn": map[string]any{"id": "native-turn", "status": "completed"}})
			})
			client, err := codex.New(session)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			request := dgruntime.StartRequest{Prompt: "inspect"}
			if name != "disabled" {
				request.QuestionMode = dgruntime.QuestionInteractive
				request.QuestionTimeout = time.Second
			}
			turn, err := client.Start(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case rejected := <-response:
				if rejected.Error == nil || strings.Contains(string(rejected.Raw), "private secret") {
					t.Fatal("unsupported question was accepted or leaked content")
				}
			case <-ctx.Done():
				t.Fatal("question rejection timed out")
			}
			if err := turn.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			for {
				select {
				case event := <-turn.Events():
					if event.Type == "interaction.question.requested" {
						t.Fatal("unsupported question became actionable")
					}
				default:
					return
				}
			}
		})
	}
}

func TestQuestionExpiryClosesTransportWithoutInventingAnAnswer(t *testing.T) {
	closed := make(chan error, 1)
	session := newScriptedSession(t, func(server *scriptServer) {
		completeQuestionStart(server)
		server.request("native-request", "item/tool/requestUserInput", nativeQuestionParams())
		_, err := server.reader.ReadByte()
		closed <- err
	})
	client, err := codex.New(session)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "inspect", QuestionMode: dgruntime.QuestionInteractive, QuestionTimeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, turn.Events(), "interaction.question.requested")
	if err := turn.Wait(ctx); !errors.Is(err, dgruntime.ErrQuestionExpired) {
		t.Fatalf("question did not expire: %v", err)
	}
	select {
	case err := <-closed:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("expired question wrote an invented answer: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("expired transport was not closed")
	}
	if err := turn.(dgruntime.QuestionTurn).AnswerQuestion(ctx, "question-1", nil); !errors.Is(err, dgruntime.ErrQuestionNotFound) {
		t.Fatal("expired question remained actionable")
	}
}

func TestQuestionModeRequiresAnExplicitBoundedTimeout(t *testing.T) {
	for _, request := range []dgruntime.StartRequest{
		{QuestionMode: dgruntime.QuestionInteractive},
		{QuestionMode: dgruntime.QuestionInteractive, QuestionTimeout: 16 * time.Minute},
		{QuestionMode: dgruntime.QuestionInteractive, QuestionTimeout: -time.Second},
		{QuestionMode: dgruntime.QuestionDisabled, QuestionTimeout: time.Second},
		{QuestionMode: "unknown"},
	} {
		request.Prompt = "inspect"
		session := newScriptedSession(t, func(server *scriptServer) {})
		client, err := codex.New(session)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Start(context.Background(), request); !errors.Is(err, dgruntime.ErrQuestionMode) {
			t.Fatal("unbounded or unknown question mode accepted")
		}
		client.Close()
	}
}

func TestQuestionNativeClearanceStopsExpiryAndRetiresTheAnswer(t *testing.T) {
	finish := make(chan struct{})
	session := newScriptedSession(t, func(server *scriptServer) {
		completeQuestionStart(server)
		server.request("native-request", "item/tool/requestUserInput", nativeQuestionParams())
		server.notify("serverRequest/resolved", map[string]any{"threadId": "native-thread", "requestId": "native-request"})
		server.notify("item/agentMessage/delta", map[string]any{"threadId": "native-thread", "turnId": "native-turn", "delta": "question cleared"})
		<-finish
		server.notify("turn/completed", map[string]any{"threadId": "native-thread", "turn": map[string]any{"id": "native-turn", "status": "completed"}})
	})
	client, err := codex.New(session)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "inspect", QuestionMode: dgruntime.QuestionInteractive, QuestionTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, turn.Events(), "interaction.question.requested")
	waitForEvent(t, turn.Events(), "output.text.delta")
	if err := turn.(dgruntime.QuestionTurn).AnswerQuestion(ctx, "question-1", nil); !errors.Is(err, dgruntime.ErrQuestionNotFound) {
		t.Fatal("cleared question remained actionable")
	}
	select {
	case <-session.done:
		t.Fatal("cleared question closed its active turn")
	case <-time.After(100 * time.Millisecond):
	}
	close(finish)
	if err := turn.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestQuestionInterruptionRetiresPendingAnswers(t *testing.T) {
	session := newScriptedSession(t, func(server *scriptServer) {
		completeQuestionStart(server)
		server.request("native-request", "item/tool/requestUserInput", nativeQuestionParams())
		interrupt := server.read()
		server.requireMethod(interrupt, "turn/interrupt")
		server.respond(interrupt.ID, map[string]any{})
		server.notify("turn/completed", map[string]any{"threadId": "native-thread", "turn": map[string]any{"id": "native-turn", "status": "interrupted"}})
	})
	client, err := codex.New(session)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "inspect", QuestionMode: dgruntime.QuestionInteractive, QuestionTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, turn.Events(), "interaction.question.requested")
	if err := turn.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	if err := turn.(dgruntime.QuestionTurn).AnswerQuestion(ctx, "question-1", nil); !errors.Is(err, dgruntime.ErrQuestionNotFound) {
		t.Fatal("interrupted question remained actionable")
	}
	if err := turn.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestQuestionAndApprovalRequestsCannotShareANativeID(t *testing.T) {
	duplicate := make(chan wireRecord, 1)
	answer := make(chan wireRecord, 1)
	session := newScriptedSession(t, func(server *scriptServer) {
		completeQuestionStart(server)
		server.request("native-request", "item/tool/requestUserInput", nativeQuestionParams())
		server.request("native-request", "item/fileChange/requestApproval", map[string]any{"threadId": "native-thread", "turnId": "native-turn", "itemId": "native-item"})
		duplicate <- server.read()
		answer <- server.read()
	})
	client, err := codex.New(session)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "inspect", QuestionMode: dgruntime.QuestionInteractive, QuestionTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, turn.Events(), "interaction.question.requested")
	select {
	case response := <-duplicate:
		if response.Error == nil || response.Error.Code != -32600 {
			t.Fatal("locked approval decision collided with a pending question")
		}
	case <-ctx.Done():
		t.Fatal("duplicate request rejection timed out")
	}
	text := "context"
	if err := turn.(dgruntime.QuestionTurn).AnswerQuestion(ctx, "question-1", []domain.QuestionAnswer{{QuestionID: "item_1", OptionIDs: []string{"option_2"}}, {QuestionID: "item_2", Text: &text}}); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-answer:
		if response.Error != nil || !strings.Contains(string(response.Result), `"answers"`) {
			t.Fatal("original question was replaced by the colliding request")
		}
	case <-ctx.Done():
		t.Fatal("question answer timed out")
	}
}
