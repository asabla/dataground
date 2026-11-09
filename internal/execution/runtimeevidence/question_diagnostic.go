package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/domain"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
)

const questionDiagnosticSchema = "dataground.dev.openshell-runtime-diagnostic/v8"
const questionDiagnosticProfile = "openshell-owned-question-diagnostic/v1"

var questionDiagnosticChecks = [3]string{"explicit-answer-resumption", "unanswered-expiry", "pending-question-interruption"}

// Content stays in the disposable native turn. The retained proof describes
// only completed cases and their shared owned-runtime execution boundary.
type diagnosticQuestions struct {
	Profile    string    `json:"profile"`
	RunID      string    `json:"runId"`
	StartedAt  string    `json:"startedAt"`
	FinishedAt string    `json:"finishedAt"`
	Checks     [3]string `json:"checks"`
}

func validQuestionDiagnostic(value *diagnosticQuestions, runID string) bool {
	if value == nil || value.Profile != questionDiagnosticProfile || value.RunID != runID || !runIDPattern.MatchString(runID) || value.Checks != questionDiagnosticChecks {
		return false
	}
	start, a := time.Parse(time.RFC3339Nano, value.StartedAt)
	end, b := time.Parse(time.RFC3339Nano, value.FinishedAt)
	return a == nil && b == nil && end.After(start) && end.Sub(start) <= 6*time.Minute
}

func checkQuestionDiagnostic(ctx context.Context, runID, model string, provider *ownedDiagnosticProvider) (*diagnosticQuestions, error) {
	if ctx == nil || provider == nil || !provider.questions || !runIDPattern.MatchString(runID) || !diagnosticModelPattern.MatchString(model) || provider.runID != runID {
		return nil, ErrCase
	}
	provider.mu.Lock()
	ready := !provider.failed && !provider.active && !provider.exporting && provider.evidence == nil && provider.starts == 1 && provider.closed == 1 && provider.ref.IsolationDomainID == runtimeIsolationDomain(runID) && provider.ref.ID == runtimeExecutionID(runID)
	provider.mu.Unlock()
	if !ready {
		return nil, ErrCase
	}
	started := time.Now().UTC()
	for _, name := range questionDiagnosticChecks {
		if err := runQuestionDiagnosticCase(ctx, provider, model, name, "dg-answer-"+runID); err != nil {
			provider.fail()
			return nil, &LocalDiagnosticError{stage: "question-" + name}
		}
	}
	proof := &diagnosticQuestions{Profile: questionDiagnosticProfile, RunID: runID, StartedAt: started.Format(time.RFC3339Nano), FinishedAt: time.Now().UTC().Format(time.RFC3339Nano), Checks: questionDiagnosticChecks}
	if !validQuestionDiagnostic(proof, runID) {
		return nil, ErrCase
	}
	return proof, nil
}

func runQuestionDiagnosticCase(ctx context.Context, provider *ownedDiagnosticProvider, model, name, answer string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	session, err := provider.StartRuntime(ctx, provider.ref)
	if err != nil {
		return ErrCase
	}
	tracked := &trackedCodexProbeSession{RuntimeSession: session}
	client, err := codex.NewOpenShellProvider(tracked)
	if err != nil {
		_ = tracked.Close()
		return ErrCase
	}
	defer client.Close()
	timeout := time.Minute
	if name == "unanswered-expiry" {
		timeout = time.Second
	}
	turn, err := client.Start(ctx, dgruntime.StartRequest{
		Prompt:     "Call request_user_input exactly once with one non-secret question asking which output format to use and two options, Text and JSON, with descriptions. Wait for the answer. Then return exactly the answer text, without any other output. Do not execute commands, change files, or use other tools.",
		WorkingDir: "/sandbox/workspace", Model: model, ApprovalMode: dgruntime.ApprovalLocked,
		SandboxMode: dgruntime.SandboxReadOnly, QuestionMode: dgruntime.QuestionInteractive, QuestionTimeout: timeout,
	})
	if err != nil {
		return ErrCase
	}
	if err := observeQuestionDiagnostic(ctx, turn, name, answer); err != nil {
		return ErrCase
	}
	if client.Close() != nil || tracked.result() != nil || ctx.Err() != nil {
		return ErrCase
	}
	return nil
}

func observeQuestionDiagnostic(ctx context.Context, turn dgruntime.Turn, name, answer string) error {
	questions, ok := turn.(dgruntime.QuestionTurn)
	if !ok {
		return ErrCase
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	waited := make(chan error, 1)
	go func() { waited <- turn.Wait(ctx) }()
	var terminal error
	finished, resumed := false, false
	questionID := ""
	var answers []domain.QuestionAnswer
	count, size := 0, 0
	consume := func(event dgruntime.Event) error {
		encoded, err := json.Marshal(event)
		size += len(encoded)
		count++
		if err != nil || count > maxCodexProbeEvents || size > maxCodexProbeEventBytes {
			return ErrCase
		}
		if event.Type == "output.message.completed" && questionID != "" {
			text, _ := event.Payload["text"].(string)
			resumed = resumed || strings.TrimSpace(text) == answer
		}
		if event.Type != "interaction.question.requested" {
			return nil
		}
		id, validID := event.Payload["questionId"].(string)
		prompts, validPrompts := event.Payload["questions"].([]domain.QuestionPrompt)
		if finished || questionID != "" || !validID || id != "question-1" || !validPrompts || domain.ValidateQuestionPrompts(prompts) != nil || len(prompts) != 1 || !prompts[0].AllowFreeText || len(prompts[0].Options) != 2 {
			return ErrCase
		}
		questionID = id
		answers = []domain.QuestionAnswer{{QuestionID: prompts[0].ID, Text: &answer}}
		if pending, err := questions.QuestionPending(ctx, id); err != nil || !pending {
			return ErrCase
		}
		switch name {
		case "explicit-answer-resumption":
			return questions.AnswerQuestion(ctx, id, answers)
		case "unanswered-expiry":
			return nil
		case "pending-question-interruption":
			return turn.Interrupt(ctx)
		default:
			return ErrCase
		}
	}
	for !finished {
		select {
		case event, open := <-turn.Events():
			if !open {
				terminal = turn.Wait(ctx)
				finished = true
			} else if consume(event) != nil {
				return ErrCase
			}
		case terminal = <-waited:
			finished = true
		case <-ctx.Done():
			return ErrCase
		}
	}
	// Wait may finish before the consumer reads the queued final message.
	for {
		select {
		case event, open := <-turn.Events():
			if open {
				if consume(event) != nil {
					return ErrCase
				}
				continue
			}
		default:
		}
		break
	}
	if questionID == "" || ctx.Err() != nil {
		return ErrCase
	}
	valid := name == "explicit-answer-resumption" && terminal == nil && resumed || name == "unanswered-expiry" && errors.Is(terminal, dgruntime.ErrQuestionExpired) || name == "pending-question-interruption" && errors.Is(terminal, dgruntime.ErrTurnInterrupted)
	if !valid {
		return ErrCase
	}
	if pending, err := questions.QuestionPending(ctx, questionID); err != nil || pending {
		return ErrCase
	}
	if !errors.Is(questions.AnswerQuestion(ctx, questionID, answers), dgruntime.ErrQuestionNotFound) {
		return ErrCase
	}
	return nil
}
