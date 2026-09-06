package reconcile

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

type runtimeQuestionStoreStub struct {
	mu                                    sync.Mutex
	value                                 persistence.InvocationRuntimeQuestion
	autoAnswer                            bool
	beginErr, completeErr                 error
	beginCalls, completeCalls, closeCalls int
	mutateRead                            func(*persistence.InvocationRuntimeQuestion)
}

func (store *runtimeQuestionStoreStub) RecordInvocationRuntimeQuestionRequest(_ context.Context, _ persistence.OperationClaim, effect persistence.EffectRecord, target persistence.InvocationRuntimeTarget, request persistence.InvocationRuntimeQuestionRequest) (persistence.InvocationRuntimeQuestion, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.value = persistence.InvocationRuntimeQuestion{Contract: persistence.InvocationRuntimeQuestionContract, ID: "qst_00000000000000000001", IsolationDomainID: target.IsolationDomainID, InvocationID: target.InvocationID, OperationID: target.OperationID, ServiceID: target.ServiceID, RevisionID: target.RevisionID, EffectID: effect.EffectID, CorrelationID: target.CorrelationID, RequestedBy: target.ActorID, SourceSequence: request.SourceSequence, Prompts: request.Prompts, ExpiresAt: request.ExpiresAt, State: "pending", Version: 1}
	return store.value, nil
}
func (store *runtimeQuestionStoreStub) GetInvocationRuntimeQuestion(_ context.Context, scope, invocation, id string) (persistence.InvocationRuntimeQuestion, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if scope != store.value.IsolationDomainID || invocation != store.value.InvocationID || id != store.value.ID {
		return persistence.InvocationRuntimeQuestion{}, persistence.ErrInvocationRuntimeQuestionMissing
	}
	if store.autoAnswer && store.value.State == "pending" {
		store.value.State = "answered"
		store.value.Version = 2
		store.value.Answers = []domain.QuestionAnswer{{QuestionID: "item_1", OptionIDs: []string{"option_2"}}}
		store.value.AnsweredBy = "actual-controller"
		store.value.AnswerCorrelationID = "cor_00000000000000000002"
		store.value.AnsweredAt = time.Now()
	}
	value := store.value
	if store.mutateRead != nil {
		store.mutateRead(&value)
	}
	return value, nil
}
func (store *runtimeQuestionStoreStub) BeginInvocationRuntimeQuestionDelivery(ctx context.Context, _ persistence.OperationClaim, _ persistence.EffectRecord, id string, authorize persistence.InvocationRuntimeQuestionAuthorizer) (persistence.InvocationRuntimeQuestion, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.beginCalls++
	if store.beginErr != nil {
		return persistence.InvocationRuntimeQuestion{}, store.beginErr
	}
	if id != store.value.ID || store.value.State != "answered" {
		return persistence.InvocationRuntimeQuestion{}, persistence.ErrInvocationRuntimeQuestionConflict
	}
	if err := authorize(ctx, store.value, persistence.InvocationQuestionEffect); err != nil {
		return persistence.InvocationRuntimeQuestion{}, err
	}
	store.value.State = "delivering"
	store.value.Version = 3
	return store.value, nil
}
func (store *runtimeQuestionStoreStub) CompleteInvocationRuntimeQuestionDelivery(_ context.Context, _ persistence.OperationClaim, _ persistence.EffectRecord, _ string) (persistence.InvocationRuntimeQuestion, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.completeCalls++
	if store.completeErr != nil {
		return persistence.InvocationRuntimeQuestion{}, store.completeErr
	}
	store.value.State = "delivered"
	store.value.Version = 4
	return store.value, nil
}
func (store *runtimeQuestionStoreStub) CloseInvocationRuntimeQuestion(_ context.Context, _ persistence.OperationClaim, _ persistence.EffectRecord, id, reason string) (persistence.InvocationRuntimeQuestion, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.closeCalls++
	if id != store.value.ID {
		return persistence.InvocationRuntimeQuestion{}, persistence.ErrInvocationRuntimeQuestionMissing
	}
	if store.value.State == "delivering" {
		store.value.State = "delivery_unknown"
	} else {
		store.value.State = "closed"
	}
	store.value.Version++
	store.value.CloseReason = reason
	return store.value, nil
}

type runtimeQuestionAuthorizerStub struct {
	value persistence.InvocationRuntimeQuestion
	phase string
	err   error
}

func (authorizer *runtimeQuestionAuthorizerStub) AuthorizeInvocationQuestion(_ context.Context, question persistence.InvocationRuntimeQuestion, phase string) error {
	authorizer.value = question
	authorizer.phase = phase
	return authorizer.err
}

type questionTurnStub struct {
	approvalTurnStub
	active      bool
	answerErr   error
	answerCalls int
	answers     []domain.QuestionAnswer
	answerID    string
	deadline    time.Time
}

func (turn *questionTurnStub) QuestionPending(context.Context, string) (bool, error) {
	return turn.active, nil
}
func (turn *questionTurnStub) AnswerQuestion(ctx context.Context, id string, answers []domain.QuestionAnswer) error {
	turn.answerCalls++
	turn.answerID = id
	turn.answers = answers
	turn.deadline, _ = ctx.Deadline()
	turn.active = false
	return turn.answerErr
}
func questionMediationFixture(t *testing.T) (*invocationRuntimeQuestions, *runtimeQuestionStoreStub, *runtimeQuestionAuthorizerStub, *questionTurnStub, persistence.OperationClaim, dgruntime.Event) {
	t.Helper()
	claim, effect, target := runtimeDriverFixture()
	claim.DeadlineAt = time.Now().Add(2 * time.Minute)
	claim.LeaseExpiresAt = time.Now().Add(30 * time.Second)
	store := &runtimeQuestionStoreStub{autoAnswer: true}
	authorizer := &runtimeQuestionAuthorizerStub{}
	turn := &questionTurnStub{active: true}
	driver := &InvocationRuntimeDriver{questionStore: store, questionAuthorizer: authorizer, readiness: func(context.Context) error { return nil }}
	questions := &invocationRuntimeQuestions{driver: driver, target: target, effect: effect, turn: turn, enabled: true, timeout: time.Minute}
	prompts := []domain.QuestionPrompt{{ID: "item_1", Title: "Target", Prompt: "Private prompt", Options: []domain.QuestionOption{{ID: "option_1", Label: "First"}, {ID: "option_2", Label: "Second"}}}}
	event := dgruntime.Event{Sequence: 1, Type: "interaction.question.requested", Payload: map[string]any{"questionId": "question-1", "questions": prompts, "expiresAt": time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}}
	return questions, store, authorizer, turn, claim, event
}

func TestInvocationRuntimeQuestionsDeliverOnlyAnAuthorizedFrozenAnswer(t *testing.T) {
	questions, store, authorizer, turn, claim, event := questionMediationFixture(t)
	if handled, err := questions.record(context.Background(), claim, event, false); !handled || err != nil {
		t.Fatal(err)
	}
	if err := questions.poll(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if authorizer.value.AnsweredBy != "actual-controller" || authorizer.value.AnswerCorrelationID != "cor_00000000000000000002" || authorizer.value.Version != 2 || authorizer.phase != persistence.InvocationQuestionEffect {
		t.Fatal("effect authorization lost the accepted controller or version")
	}
	if turn.answerCalls != 1 || turn.answerID != "question-1" || !reflect.DeepEqual(turn.answers, store.value.Answers) || store.value.State != "delivered" || store.completeCalls != 1 || questions.pending != nil || turn.deadline.After(claim.LeaseExpiresAt) {
		t.Fatal("answer delivery lost frozen payload, lease deadline or single-use acknowledgement")
	}
	if err := questions.poll(context.Background(), claim); err != nil || turn.answerCalls != 1 {
		t.Fatal("completed question was repeated")
	}
}

func TestInvocationRuntimeQuestionsFailClosedBeforeAndAfterDeliveryReservation(t *testing.T) {
	for _, test := range []struct {
		name            string
		configure       func(*runtimeQuestionStoreStub, *runtimeQuestionAuthorizerStub, *questionTurnStub)
		sent, completed int
		final           string
	}{
		{"policy denial", func(_ *runtimeQuestionStoreStub, authorizer *runtimeQuestionAuthorizerStub, _ *questionTurnStub) {
			authorizer.err = ErrInvocationQuestionDenied
		}, 0, 0, "closed"},
		{"expired claim", func(store *runtimeQuestionStoreStub, _ *runtimeQuestionAuthorizerStub, _ *questionTurnStub) {
			store.beginErr = persistence.ErrLeaseLost
		}, 0, 0, "closed"},
		{"native clearance during delivery", func(_ *runtimeQuestionStoreStub, _ *runtimeQuestionAuthorizerStub, turn *questionTurnStub) {
			turn.answerErr = dgruntime.ErrQuestionNotFound
		}, 1, 0, "delivery_unknown"},
		{"lost acknowledgement", func(store *runtimeQuestionStoreStub, _ *runtimeQuestionAuthorizerStub, _ *questionTurnStub) {
			store.completeErr = errors.New("acknowledgement lost")
		}, 1, 1, "delivery_unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			questions, store, authorizer, turn, claim, event := questionMediationFixture(t)
			test.configure(store, authorizer, turn)
			if _, err := questions.record(context.Background(), claim, event, false); err != nil {
				t.Fatal(err)
			}
			if err := questions.poll(context.Background(), claim); err == nil {
				t.Fatal("unsafe delivery succeeded")
			}
			if err := questions.close(context.Background(), claim, "runtime-ended"); err != nil {
				t.Fatal(err)
			}
			if turn.answerCalls != test.sent || store.completeCalls != test.completed || store.value.State != test.final {
				t.Fatal("ambiguous delivery could be repeated")
			}
		})
	}
}

func TestInvocationRuntimeQuestionsRetireClearedAndQueuedTerminalRequests(t *testing.T) {
	for _, ended := range []bool{false, true} {
		questions, store, _, turn, claim, event := questionMediationFixture(t)
		if _, err := questions.record(context.Background(), claim, event, ended); err != nil {
			t.Fatal(err)
		}
		turn.active = false
		if err := questions.poll(context.Background(), claim); err != nil {
			t.Fatal(err)
		}
		if turn.answerCalls != 0 || store.closeCalls != 1 || store.value.State != "closed" || questions.pending != nil {
			t.Fatal("cleared or terminal question retained delivery authority")
		}
	}
}

func TestInvocationRuntimeQuestionsRejectSubstitutionAndUndurableEvents(t *testing.T) {
	for _, mutate := range []func(*persistence.InvocationRuntimeQuestion){
		func(q *persistence.InvocationRuntimeQuestion) { q.IsolationDomainID = "iso_00000000000000000002" },
		func(q *persistence.InvocationRuntimeQuestion) { q.EffectID = "other-effect" },
		func(q *persistence.InvocationRuntimeQuestion) { q.SourceSequence++ },
		func(q *persistence.InvocationRuntimeQuestion) { q.Prompts = nil },
		func(q *persistence.InvocationRuntimeQuestion) { q.State = "delivery_unknown" },
	} {
		questions, store, _, turn, claim, event := questionMediationFixture(t)
		store.mutateRead = mutate
		if _, err := questions.record(context.Background(), claim, event, false); err != nil {
			t.Fatal(err)
		}
		if err := questions.poll(context.Background(), claim); err == nil || turn.answerCalls != 0 || store.beginCalls != 0 {
			t.Fatal("substituted or ambiguous question reached delivery")
		}
	}
	questions, _, _, _, claim, event := questionMediationFixture(t)
	questions.enabled = false
	if handled, err := questions.record(context.Background(), claim, event, false); !handled || !errors.Is(err, dgruntime.ErrQuestionMode) {
		t.Fatal("disabled question reached persistence")
	}
	if err := questions.driver.recordRuntimeEvent(context.Background(), claim, event); !errors.Is(err, dgruntime.ErrQuestionMode) {
		t.Fatal("raw question payload entered ordinary journal path")
	}
}

type streamingQuestionTurn struct {
	questionTurnStub
	events <-chan dgruntime.Event
	done   chan struct{}
}

func (turn *streamingQuestionTurn) Events() <-chan dgruntime.Event { return turn.events }
func (turn *streamingQuestionTurn) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-turn.done:
		return nil
	}
}
func (turn *streamingQuestionTurn) AnswerQuestion(ctx context.Context, id string, answers []domain.QuestionAnswer) error {
	err := turn.questionTurnStub.AnswerQuestion(ctx, id, answers)
	if err == nil {
		close(turn.done)
	}
	return err
}

func TestInvocationRuntimeQuestionWaitKeepsDrainingEventsAndRenewingLease(t *testing.T) {
	questions, store, _, _, claim, event := questionMediationFixture(t)
	store.autoAnswer = false
	events := make(chan dgruntime.Event, 1)
	turn := &streamingQuestionTurn{questionTurnStub: questionTurnStub{active: true}, events: events, done: make(chan struct{})}
	runtimeStore := &runtimeStoreStub{}
	questions.driver.store = runtimeStore
	questions.driver.leaseDuration = time.Minute
	questions.driver.renewInterval = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	produced := make(chan struct{})
	go func() {
		defer close(produced)
		defer close(events)
		select {
		case events <- event:
		case <-ctx.Done():
			return
		}
		for i := uint64(2); i <= 501; i++ {
			select {
			case events <- dgruntime.Event{Sequence: i, Type: "output.text.delta", Payload: map[string]any{"text": "x"}}:
			case <-ctx.Done():
				return
			}
		}
		store.mu.Lock()
		store.autoAnswer = true
		store.mu.Unlock()
	}()
	output, err := newInvocationRuntimeOutput(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = questions.driver.runTurn(ctx, claim, questions.effect, turn, output, questions.target, execution.ExecutionRef{}, nil, true, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	<-produced
	if len(runtimeStore.events) != 500 || runtimeStore.completeCalls != 1 || runtimeStore.renewCalls == 0 || turn.answerCalls != 1 || store.value.State != "delivered" {
		t.Fatal("question wait blocked event replay, lease renewal or finalization")
	}
	for _, recorded := range runtimeStore.events {
		if recorded.Type == "interaction.question.requested" {
			t.Fatal("private question prompt entered ordinary journal path")
		}
	}
}

func TestInvocationRuntimeQuestionQueuedAtTurnCompletionNeverDelivers(t *testing.T) {
	questions, store, _, _, claim, event := questionMediationFixture(t)
	turn := &streamingQuestionTurn{questionTurnStub: questionTurnStub{active: true}, events: runtimeEvents(event, dgruntime.Event{Sequence: 2, Type: "output.text.delta", Payload: map[string]any{"text": "finished"}}), done: make(chan struct{})}
	close(turn.done)
	runtimeStore := &runtimeStoreStub{}
	questions.driver.store = runtimeStore
	questions.driver.leaseDuration = time.Minute
	questions.driver.renewInterval = time.Second
	output, err := newInvocationRuntimeOutput(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = questions.driver.runTurn(context.Background(), claim, questions.effect, turn, output, questions.target, execution.ExecutionRef{}, nil, true, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if store.value.State != "closed" || store.value.CloseReason != "runtime-ended" || turn.answerCalls != 0 || store.closeCalls != 1 {
		t.Fatal("queued terminal question was not closed without delivery")
	}
}

func TestInvocationRuntimeQuestionsRecheckReadinessAfterReservation(t *testing.T) {
	questions, store, _, turn, claim, event := questionMediationFixture(t)
	if _, err := questions.record(context.Background(), claim, event, false); err != nil {
		t.Fatal(err)
	}
	checks := 0
	questions.driver.readiness = func(context.Context) error {
		checks++
		if checks == 3 {
			return errors.New("certification withdrawn")
		}
		return nil
	}
	if err := questions.poll(context.Background(), claim); err == nil {
		t.Fatal("readiness loss after reservation was ignored")
	}
	if err := questions.close(context.Background(), claim, "runtime-ended"); err != nil {
		t.Fatal(err)
	}
	if turn.answerCalls != 0 || store.value.State != "delivery_unknown" {
		t.Fatal("reserved answer was delivered or made retryable after readiness loss")
	}
}

func TestInvocationRuntimeQuestionsRejectMalformedAndOverlappingRequests(t *testing.T) {
	for _, mutate := range []func(*invocationRuntimeQuestions, *dgruntime.Event){
		func(_ *invocationRuntimeQuestions, event *dgruntime.Event) {
			event.Payload["questionId"] = "native-secret"
		},
		func(_ *invocationRuntimeQuestions, event *dgruntime.Event) {
			event.Payload["nativeId"] = "native-secret"
		},
		func(_ *invocationRuntimeQuestions, event *dgruntime.Event) { event.Payload["expiresAt"] = "invalid" },
		func(_ *invocationRuntimeQuestions, event *dgruntime.Event) {
			event.Payload["expiresAt"] = time.Now().Add(5 * time.Minute).Format(time.RFC3339Nano)
		},
		func(_ *invocationRuntimeQuestions, event *dgruntime.Event) {
			event.Payload["questions"] = []domain.QuestionPrompt{}
		},
		func(_ *invocationRuntimeQuestions, event *dgruntime.Event) { event.Sequence = 0 },
	} {
		questions, store, _, _, claim, event := questionMediationFixture(t)
		mutate(questions, &event)
		if _, err := questions.record(context.Background(), claim, event, false); err == nil || store.value.ID != "" {
			t.Fatal("invalid question entered durable request store")
		}
	}
	questions, store, _, _, claim, event := questionMediationFixture(t)
	if _, err := questions.record(context.Background(), claim, event, false); err != nil {
		t.Fatal(err)
	}
	event.Sequence++
	if _, err := questions.record(context.Background(), claim, event, false); !errors.Is(err, persistence.ErrInvocationRuntimeQuestionConflict) || store.closeCalls != 0 {
		t.Fatal("overlapping question replaced live authority")
	}
}

func TestInvocationRuntimeQuestionExpiryUsesConservativeDatabasePrecision(t *testing.T) {
	for _, clamp := range []bool{false, true} {
		t.Run(map[bool]string{false: "native deadline", true: "operation deadline"}[clamp], func(t *testing.T) {
			questions, store, _, _, claim, event := questionMediationFixture(t)
			expires := time.Now().Add(30 * time.Second).UTC().Truncate(time.Microsecond).Add(123 * time.Nanosecond)
			event.Payload["expiresAt"] = expires.Format(time.RFC3339Nano)
			if clamp {
				claim.DeadlineAt = expires.Add(-time.Second)
				expires = claim.DeadlineAt
			}
			if handled, err := questions.record(context.Background(), claim, event, false); !handled || err != nil {
				t.Fatal(err)
			}
			if !store.value.ExpiresAt.Equal(expires.Truncate(time.Microsecond)) || store.value.ExpiresAt.After(expires) {
				t.Fatal("question deadline did not retain conservative database precision")
			}
		})
	}
}
