package codex

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestQueuedQuestionAnswerCannotOutliveNativeClearance(t *testing.T) {
	wire := &approvalWire{}
	pending := &pendingQuestion{
		requestID: json.RawMessage(`"native-request"`), nativeKey: "string:native-request", nativeIDs: []string{"native-item"},
		prompts:   []domain.QuestionPrompt{{ID: "item_1", Title: "Context", Prompt: "Add context.", AllowFreeText: true}},
		expiresAt: time.Now().Add(time.Minute), timer: time.NewTimer(time.Minute),
	}
	defer pending.timer.Stop()
	client := &Client{input: wire, threadID: "thread", turnID: "turn", questions: map[string]*pendingQuestion{"question-1": pending}, nativeRequests: map[string]struct{}{"string:native-request": {}}, done: make(chan struct{}), closed: make(chan struct{})}
	client.writeMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	text := "explicit context"
	result := make(chan error, 1)
	go func() {
		result <- client.AnswerQuestion(ctx, "question-1", []domain.QuestionAnswer{{QuestionID: "item_1", Text: &text}})
	}()
	for {
		client.stateMu.Lock()
		queued := pending.resolving
		client.stateMu.Unlock()
		if queued {
			break
		}
		if ctx.Err() != nil {
			client.writeMu.Unlock()
			t.Fatal("answer did not reach writer")
		}
		runtime.Gosched()
	}
	client.handleResolvedRequest(wireMessage{Params: json.RawMessage(`{"threadId":"thread","requestId":"native-request"}`)})
	client.writeMu.Unlock()
	if err := <-result; !errors.Is(err, dgruntime.ErrQuestionNotFound) {
		t.Fatalf("cleared answer reached writer: %v", err)
	}
	wire.mu.Lock()
	writes := wire.writes
	wire.mu.Unlock()
	if writes != 0 {
		t.Fatal("cleared question answer reached native transport")
	}
	client.stateMu.Lock()
	remaining := len(client.questions)
	client.stateMu.Unlock()
	if remaining != 0 {
		t.Fatal("rejected write restored the question")
	}
}
