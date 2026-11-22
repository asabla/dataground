package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/execution"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

type questionDiagnosticTurn struct {
	mu                    sync.Mutex
	events                chan dgruntime.Event
	done                  chan struct{}
	err                   error
	pending               bool
	name, failure, answer string
	deliveries            int
	closeOnce             sync.Once
}

func newQuestionDiagnosticTurn(name, failure string) *questionDiagnosticTurn {
	q := &questionDiagnosticTurn{events: make(chan dgruntime.Event, 8), done: make(chan struct{}), pending: true, name: name, failure: failure, answer: "fixture-answer"}
	prompts := []domain.QuestionPrompt{{ID: "item_1", Title: "Format", Prompt: "Choose the output format.", AllowFreeText: true, Options: []domain.QuestionOption{{ID: "option_1", Label: "Text", Description: "Plain text."}, {ID: "option_2", Label: "JSON", Description: "Structured data."}}}}
	if failure == "no-free-text" {
		prompts[0].AllowFreeText = false
	}
	q.events <- dgruntime.Event{Sequence: 1, Type: "interaction.question.requested", Payload: map[string]any{"questionId": "question-1", "questions": prompts}}
	if failure == "early-terminal" {
		q.finish(nil)
	}
	return q
}
func (q *questionDiagnosticTurn) Events() <-chan dgruntime.Event { return q.events }
func (q *questionDiagnosticTurn) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-q.done:
		q.mu.Lock()
		defer q.mu.Unlock()
		return q.err
	}
}
func (q *questionDiagnosticTurn) Close() error { q.finish(dgruntime.ErrClosed); return nil }
func (q *questionDiagnosticTurn) ResolveApproval(context.Context, string, dgruntime.ApprovalDecision) error {
	return errors.New("unexpected approval")
}
func (q *questionDiagnosticTurn) finish(err error) {
	q.closeOnce.Do(func() {
		q.mu.Lock()
		q.pending = false
		q.err = err
		q.mu.Unlock()
		close(q.done)
	})
}
func (q *questionDiagnosticTurn) QuestionPending(context.Context, string) (bool, error) {
	q.mu.Lock()
	pending := q.pending
	q.mu.Unlock()
	if pending && q.name == "unanswered-expiry" {
		q.finish(dgruntime.ErrQuestionExpired)
	}
	return pending, nil
}
func (q *questionDiagnosticTurn) Interrupt(context.Context) error {
	if q.failure == "interrupt" {
		return errors.New("private interrupt failure")
	}
	q.finish(dgruntime.ErrTurnInterrupted)
	return nil
}
func (q *questionDiagnosticTurn) AnswerQuestion(_ context.Context, _ string, answers []domain.QuestionAnswer) error {
	q.mu.Lock()
	if !q.pending {
		q.mu.Unlock()
		return dgruntime.ErrQuestionNotFound
	}
	q.deliveries++
	q.mu.Unlock()
	if q.failure == "delivery" {
		return errors.New("private delivery failure")
	}
	if len(answers) != 1 || answers[0].Text == nil || *answers[0].Text != q.answer {
		return domain.ErrQuestionInvalid
	}
	text := q.answer
	if q.failure == "wrong-answer" {
		text = "other"
	}
	q.events <- dgruntime.Event{Sequence: 2, Type: "output.message.completed", Payload: map[string]any{"text": text}}
	if q.failure == "duplicate-question" {
		q.events <- dgruntime.Event{Sequence: 3, Type: "interaction.question.requested", Payload: map[string]any{}}
	}
	q.finish(nil)
	return nil
}

func TestQuestionDiagnosticRequiresObservedPendingRequestAndTerminalOutcome(t *testing.T) {
	for _, name := range questionDiagnosticChecks {
		failures := []string{"", "no-free-text", "early-terminal"}
		if name == "explicit-answer-resumption" {
			failures = append(failures, "delivery", "wrong-answer", "duplicate-question")
		}
		if name == "pending-question-interruption" {
			failures = append(failures, "interrupt")
		}
		for _, failure := range failures {
			t.Run(name+"/"+failure, func(t *testing.T) {
				q := newQuestionDiagnosticTurn(name, failure)
				defer q.Close()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				err := observeQuestionDiagnostic(ctx, q, name, q.answer)
				if (err == nil) != (failure == "") {
					t.Fatalf("case %s: %v", failure, err)
				}
				if err != nil && strings.Contains(err.Error(), "private") {
					t.Fatal("native error escaped")
				}
				q.mu.Lock()
				sent := q.deliveries
				q.mu.Unlock()
				if name != "explicit-answer-resumption" && sent != 0 {
					t.Fatal("unanswered case sent an answer")
				}
			})
		}
	}
}

func TestQuestionDiagnosticSelectionRequiresOwnedSourceBeforeEffects(t *testing.T) {
	f := newLauncherFixture()
	config := f.config()
	config.questions = true
	if _, err := launch(context.Background(), config, f.dependencies()); err == nil || len(f.events) != 0 {
		t.Fatal("question selection reached effects without owned source")
	}
	p, _, _ := newOwnedProbe(t, "")
	p.questions = true
	if _, err := checkQuestionDiagnostic(context.Background(), p.runID, "model", p); err == nil || p.starts != 0 {
		t.Fatal("question diagnostic ran before source preparation")
	}
}

func TestQuestionDiagnosticExportRequiresThreeAdditionalClosedOwners(t *testing.T) {
	p, _, _ := newOwnedProbe(t, "")
	p.questions = true
	closeOwnedProbeSessions(t, p, ownedDiagnosticSessionCount-1)
	if _, err := p.Export(context.Background(), ownedProbeRequest(p)); err == nil {
		t.Fatal("original session count authorized question diagnostic export")
	}
	closeOwnedProbeSessions(t, p, 3)
	if _, err := p.Export(context.Background(), ownedProbeRequest(p)); err != nil {
		t.Fatal(err)
	}
	proof := p.Evidence()
	if !validOwnedWorkspaceDiagnosticQuestions(proof, p.runID, true) || validOwnedWorkspaceDiagnostic(proof, p.runID) || proof.OwnedSessions != 14 {
		t.Fatal("question export did not preserve its distinct session count")
	}
}

func TestQuestionDiagnosticSerializationRequiresExactOwnedCountAndOrderedWindows(t *testing.T) {
	base := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	config := testConfig(&caseRunner{base: base}, successfulCleanup)
	config.Provenance.WorkflowRunID = 0
	config.diagnosticModel = "selected-model"
	config.candidateImage = "sha256:" + strings.Repeat("a", 64)
	config.policyProfile = RosettaRuntimePolicyProfile
	config.supervisorCandidateImage = "sha256:" + strings.Repeat("b", 64)
	config.candidateGatewaySHA256 = strings.Repeat("c", 64)
	run, err := newEvidenceRun(config, clock(base, base.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	result, err := run.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source, _ := workspaceDiagnosticSource(false)
	result.workspaceSourceRequired = true
	result.workspaceSource = &diagnosticWorkspaceSource{Profile: workspaceDiagnosticProfile, RunID: config.RunID, StartedAt: base.Add(-3 * time.Second).Format(time.RFC3339Nano), FinishedAt: base.Add(-2 * time.Second).Format(time.RFC3339Nano), ArchiveDigest: source.Receipt().ArchiveDigest, Checks: workspaceDiagnosticChecks}
	result.workspaceExportRequired = true
	proof := diagnosticWorkspaceExport{Profile: ownedWorkspaceDiagnosticProfile, RunID: config.RunID, OwnedSessions: 14, Changes: ownedProbeChanges(t, false).Receipt(), Checks: ownedWorkspaceChecks}
	for _, c := range result.record.Checks {
		if c.Name == CheckArtifactExport {
			proof.StartedAt = c.StartedAt
			proof.FinishedAt = c.FinishedAt
		}
	}
	result.workspaceExport = &proof
	result.questionsRequired = true
	questions := diagnosticQuestions{Profile: questionDiagnosticProfile, RunID: config.RunID, StartedAt: base.Add(-2 * time.Second).Format(time.RFC3339Nano), FinishedAt: base.Add(-time.Second).Format(time.RFC3339Nano), Checks: questionDiagnosticChecks}
	result.questions = &questions
	encoded, err := json.Marshal(LocalDiagnosticResult{result: result})
	if err != nil || !strings.Contains(string(encoded), questionDiagnosticSchema) {
		t.Fatalf("complete question diagnostic: %v", err)
	}
	for _, mutate := range []func(*Result){
		func(v *Result) { v.questionsRequired = false },
		func(v *Result) { v.questions = nil },
		func(v *Result) { v.workspaceExportRequired = false },
		func(v *Result) { v.workspaceExport.OwnedSessions = 11 },
		func(v *Result) { v.questions.RunID = strings.Repeat("f", 32) },
		func(v *Result) { v.questions.Checks[0] = "omitted" },
		func(v *Result) { v.questions.FinishedAt = v.questions.StartedAt },
		func(v *Result) { v.questions.StartedAt = v.workspaceSource.StartedAt },
		func(v *Result) { v.questions.FinishedAt = v.record.Run.FinishedAt },
	} {
		copy := result
		copyProof := proof
		copyQuestions := questions
		copy.workspaceExport = &copyProof
		copy.questions = &copyQuestions
		mutate(&copy)
		if _, err := json.Marshal(LocalDiagnosticResult{result: copy}); err == nil {
			t.Fatal("invalid or downgraded question evidence serialized")
		}
	}
}

type ownedQuestionProbeSession struct {
	*codexProbeSession
	binding string
}

func (s *ownedQuestionProbeSession) OwnerBinding() (string, error) { return s.binding, nil }
func (s *ownedQuestionProbeSession) ConfirmedOwnerBinding() (string, error) {
	select {
	case <-s.done:
		return s.binding, s.closeErr
	default:
		return "", ErrCase
	}
}

func TestQuestionDiagnosticComposesPinnedAdapterAndRequiresOwnerCleanup(t *testing.T) {
	for _, failCleanup := range []bool{false, true} {
		t.Run(fmt.Sprint(failCleanup), func(t *testing.T) {
			runID := strings.Repeat("a", 32)
			p := &ownedDiagnosticProvider{questions: true, runID: runID, starts: 1, closed: 1, ref: execution.ExecutionRef{IsolationDomainID: runtimeIsolationDomain(runID), ID: runtimeExecutionID(runID)}}
			opened := 0
			p.open = func(context.Context, execution.ExecutionRef) (ownedDiagnosticSession, error) {
				index := opened
				opened++
				session := newCodexProbeSession(func(server *codexProbeServer) {
					server.startWithModel("fixture-model", "dataground_openshell_codex")
					var parameters struct {
						Config struct {
							Features map[string]bool `json:"features"`
						} `json:"config"`
						Sandbox  string `json:"sandbox"`
						Approval string `json:"approvalPolicy"`
					}
					if json.Unmarshal(server.threadParams, &parameters) != nil || !parameters.Config.Features["default_mode_request_user_input"] || parameters.Sandbox != "read-only" || parameters.Approval != "never" {
						t.Error("question diagnostic changed native restrictions or omitted activation")
					}
					server.request("native-question", "item/tool/requestUserInput", map[string]any{
						"threadId": "native-thread", "turnId": "native-turn", "itemId": "native-item",
						"questions": []any{map[string]any{"id": "native-format", "header": "Format", "question": "Choose output.", "isOther": true, "options": []any{map[string]string{"label": "Text", "description": "Plain text."}, map[string]string{"label": "JSON", "description": "Structured data."}}}},
					})
					switch index {
					case 0:
						response := server.read()
						var result struct {
							Answers map[string]struct {
								Answers []string `json:"answers"`
							} `json:"answers"`
						}
						if json.Unmarshal(response.Result, &result) != nil || len(result.Answers["native-format"].Answers) != 1 || result.Answers["native-format"].Answers[0] != "dg-answer-"+runID {
							t.Error("question diagnostic did not deliver its exact synthetic answer")
						}
						server.notify("item/completed", map[string]any{"threadId": "native-thread", "turnId": "native-turn", "item": map[string]any{"id": "native-message", "type": "agentMessage", "text": "dg-answer-" + runID, "phase": "final_answer"}})
						server.notify("turn/completed", map[string]any{"threadId": "native-thread", "turn": map[string]any{"id": "native-turn", "status": "completed"}})
					case 1:
						if _, err := server.reader.ReadBytes('\n'); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
							t.Error("expiry sent a native answer")
						}
					case 2:
						server.interrupt()
					}
				})
				if failCleanup {
					session.closeErr = errors.New("private cleanup failure")
				}
				return &ownedQuestionProbeSession{codexProbeSession: session, binding: fmt.Sprintf("sha256:%064x", index+2)}, nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			proof, err := checkQuestionDiagnostic(ctx, runID, "fixture-model", p)
			if failCleanup {
				if err == nil || proof != nil || !p.failed || strings.Contains(err.Error(), "private") {
					t.Fatal("failed cleanup released evidence or raw error")
				}
				return
			}
			if err != nil || !validQuestionDiagnostic(proof, runID) || p.starts != 4 || p.closed != 4 || opened != 3 {
				t.Fatalf("incomplete question composition: %v", err)
			}
			if _, err := checkQuestionDiagnostic(ctx, runID, "fixture-model", p); err == nil || opened != 3 {
				t.Fatal("diagnostic replay started more native sessions")
			}
		})
	}
}
