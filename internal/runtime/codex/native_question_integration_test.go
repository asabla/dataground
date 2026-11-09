package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
)

// Offline responses exercise the real pinned tool handler without provider
// credentials or network access. They are not live question certification.
func TestNativeCodexQuestion(t *testing.T) {
	for _, outcome := range []string{"answer", "free-text", "expire", "interrupt"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			native, workspace, fixture := newNativeFixtureProcess(t, ctx)
			writeNativeQuestionFixture(t, fixture)
			client, err := codex.New(native)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			timeout := 20 * time.Second
			if outcome == "expire" {
				timeout = time.Second
			}
			turn, err := client.Start(ctx, dgruntime.StartRequest{
				Prompt: "Ask which output format to use before completing the task.", WorkingDir: workspace,
				Model: "gpt-5.4", QuestionMode: dgruntime.QuestionInteractive, QuestionTimeout: timeout,
			})
			if err != nil {
				t.Fatal(err)
			}
			question := awaitNativeQuestion(t, ctx, turn)
			questionID, ok := question.Payload["questionId"].(string)
			prompts, promptsOK := question.Payload["questions"].([]domain.QuestionPrompt)
			if !ok || questionID != "question-1" || !promptsOK || len(prompts) != 1 || len(prompts[0].Options) != 2 || prompts[0].Prompt != "Which output format should be used?" || !prompts[0].AllowFreeText {
				t.Fatal("native question did not produce the bounded platform request")
			}
			questionTurn := turn.(dgruntime.QuestionTurn)
			if pending, err := questionTurn.QuestionPending(ctx, questionID); err != nil || !pending {
				t.Fatal("native request was not pending before the decision")
			}
			wait, stopWait := context.WithTimeout(ctx, 25*time.Millisecond)
			if err := turn.Wait(wait); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("native tool did not wait for an explicit answer")
			}
			stopWait()
			answers := []domain.QuestionAnswer{{QuestionID: prompts[0].ID, OptionIDs: []string{prompts[0].Options[1].ID}}}
			if outcome == "free-text" {
				text := "Use comma-separated values."
				answers = []domain.QuestionAnswer{{QuestionID: prompts[0].ID, Text: &text}}
			}
			wantDeliveries := 0
			switch outcome {
			case "answer", "free-text":
				wantDeliveries = 1
				// The next native model request reads this final response only after
				// the tool receives the explicit answer below.
				writeSessionFixture(t, fixture, 2, "The requested format is selected.")
				if err := questionTurn.AnswerQuestion(ctx, questionID, answers); err != nil {
					t.Fatal(err)
				}
				events := observeNativeSessionTurn(t, ctx, turn)
				completed := false
				for _, event := range events {
					if event.Type == "output.message.completed" && event.Payload["text"] == "The requested format is selected." {
						completed = true
					}
				}
				if !completed {
					t.Fatal("native turn did not resume after the answer")
				}
			case "expire":
				if err := turn.Wait(ctx); !errors.Is(err, dgruntime.ErrQuestionExpired) {
					t.Fatalf("native question expiry: %v", err)
				}
			case "interrupt":
				if err := turn.Interrupt(ctx); err != nil {
					t.Fatal(err)
				}
				if err := turn.Wait(ctx); !errors.Is(err, dgruntime.ErrTurnInterrupted) {
					t.Fatalf("native question interruption: %v", err)
				}
			}
			if pending, err := questionTurn.QuestionPending(ctx, questionID); err != nil || pending {
				t.Fatal("closed native question retained delivery authority")
			}
			if err := questionTurn.AnswerQuestion(ctx, questionID, answers); !errors.Is(err, dgruntime.ErrQuestionNotFound) {
				t.Fatal("closed native question accepted another answer")
			}
			native.input.mu.Lock()
			deliveries := native.input.questionAnswers
			native.input.mu.Unlock()
			if deliveries != wantDeliveries {
				t.Fatalf("native answer deliveries: got %d, want %d", deliveries, wantDeliveries)
			}
		})
	}
}

func writeNativeQuestionFixture(t *testing.T, path string) {
	t.Helper()
	arguments, err := json.Marshal(map[string]any{"questions": []any{map[string]any{
		"id": "private-native-format", "header": "Format", "question": "Which output format should be used?",
		"options": []any{map[string]string{"label": "Text", "description": "Use plain text."}, map[string]string{"label": "JSON", "description": "Use structured JSON."}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	item := map[string]any{"id": "private-native-question", "type": "function_call", "call_id": "private-native-call", "name": "request_user_input", "arguments": string(arguments)}
	var content strings.Builder
	for _, frame := range []any{
		map[string]any{"type": "response.created", "response": map[string]any{"id": "fixture-question"}},
		map[string]any{"type": "response.output_item.added", "item": item},
		map[string]any{"type": "response.output_item.done", "item": item},
		map[string]any{"type": "response.completed", "response": map[string]any{"id": "fixture-question"}},
	} {
		encoded, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		content.WriteString("data: " + string(encoded) + "\n\n")
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func awaitNativeQuestion(t *testing.T, ctx context.Context, turn dgruntime.Turn) dgruntime.Event {
	t.Helper()
	for count := 0; count < 32; count++ {
		select {
		case event, open := <-turn.Events():
			if !open {
				t.Fatal("native turn ended before the question")
			}
			if event.Type == "interaction.question.requested" {
				return event
			}
		case <-ctx.Done():
			t.Fatal("native question deadline exceeded")
		}
	}
	t.Fatal("native question exceeded its event budget")
	return dgruntime.Event{}
}
