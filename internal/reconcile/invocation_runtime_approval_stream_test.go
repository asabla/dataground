package reconcile

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

type streamingApprovalStore struct {
	runtimeApprovalStoreStub
	release <-chan struct{}
}

func (store *streamingApprovalStore) GetInvocationRuntimeApproval(ctx context.Context, scope, id string) (persistence.InvocationRuntimeApproval, error) {
	select {
	case <-store.release:
		store.keepPending = false
	default:
		store.keepPending = true
	}
	return store.runtimeApprovalStoreStub.GetInvocationRuntimeApproval(ctx, scope, id)
}

type streamingApprovalTurn struct {
	approvalTurnStub
	events <-chan dgruntime.Event
	done   chan struct{}
}

func (turn *streamingApprovalTurn) Events() <-chan dgruntime.Event { return turn.events }
func (turn *streamingApprovalTurn) Wait(ctx context.Context) error {
	select {
	case <-turn.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (turn *streamingApprovalTurn) ApprovalPending(ctx context.Context, _ string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	select {
	case <-turn.done:
		return false, nil
	default:
		return true, nil
	}
}
func (turn *streamingApprovalTurn) ResolveApproval(ctx context.Context, id string, decision dgruntime.ApprovalDecision) error {
	err := turn.approvalTurnStub.ResolveApproval(ctx, id, decision)
	if err == nil {
		close(turn.done)
	}
	return err
}
func approvalStreamEvent() dgruntime.Event {
	return dgruntime.Event{Sequence: 1, Type: "interaction.approval.requested", Payload: map[string]any{"approvalId": "approval-1", "action": "process.execute"}}
}

func TestInvocationRuntimeApprovalWaitDrainsEventsAndRenewsLease(t *testing.T) {
	claim, effect, target := runtimeDriverFixture()
	runtimeStore := &runtimeStoreStub{}
	release := make(chan struct{})
	store := &streamingApprovalStore{release: release}
	driver := &InvocationRuntimeDriver{store: runtimeStore, approvalStore: store, approvalAuthorizer: &approvalAuthorizerStub{}, leaseDuration: time.Minute, renewInterval: 10 * time.Millisecond}
	events := make(chan dgruntime.Event, 1)
	turn := &streamingApprovalTurn{events: events, done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	produced := make(chan struct{})
	go func() {
		defer close(produced)
		defer close(events)
		select {
		case events <- approvalStreamEvent():
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
		close(release)
	}()
	output, err := newInvocationRuntimeOutput(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = driver.runTurn(ctx, claim, effect, turn, output, target, execution.ExecutionRef{}, nil, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	<-produced
	if len(runtimeStore.events) != 500 || runtimeStore.renewCalls == 0 || runtimeStore.completeCalls != 1 || !store.completed || turn.decisionID != "approval-1" {
		t.Fatal("approval wait blocked stream, renewal, or completion")
	}
	for _, event := range runtimeStore.events {
		if event.Type == "interaction.approval.requested" {
			t.Fatal("native handle reached ordinary event persistence")
		}
	}
}

func TestInvocationRuntimeApprovalQueuedAtCompletionIsClosedWithoutDecision(t *testing.T) {
	claim, effect, target := runtimeDriverFixture()
	store := &runtimeApprovalStoreStub{}
	runtimeStore := &runtimeStoreStub{}
	driver := &InvocationRuntimeDriver{store: runtimeStore, approvalStore: store, approvalAuthorizer: &approvalAuthorizerStub{}, leaseDuration: time.Minute, renewInterval: time.Second}
	turn := &streamingApprovalTurn{events: runtimeEvents(approvalStreamEvent(), dgruntime.Event{Sequence: 2, Type: "output.text.delta", Payload: map[string]any{"text": "done"}}), done: make(chan struct{})}
	close(turn.done)
	output, err := newInvocationRuntimeOutput(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.runTurn(context.Background(), claim, effect, turn, output, target, execution.ExecutionRef{}, nil, false, 0); err != nil {
		t.Fatal(err)
	}
	if store.approval.State != "closed" || turn.decisionID != "" || store.effectiveDecision != "" || runtimeStore.completeCalls != 1 {
		t.Fatal("queued completed request remained actionable")
	}
}

type clearanceApprovalTurn struct {
	approvalTurnStub
	active bool
}

func (turn *clearanceApprovalTurn) ApprovalPending(ctx context.Context, _ string) (bool, error) {
	return turn.active, ctx.Err()
}

func TestInvocationRuntimeApprovalNativeClearanceClosesBeforeReservation(t *testing.T) {
	for _, duringAuthorization := range []bool{false, true} {
		claim, effect, target := runtimeDriverFixture()
		store := &runtimeApprovalStoreStub{}
		turn := &clearanceApprovalTurn{active: true}
		driver := &InvocationRuntimeDriver{approvalStore: store, approvalAuthorizer: approvalBoundaryAuthorizer(func(context.Context, persistence.InvocationRuntimeApproval, string) error {
			if duringAuthorization {
				turn.active = false
			}
			return nil
		})}
		approvals := &invocationRuntimeApprovals{driver: driver, target: target, effect: effect, turn: turn}
		if _, err := approvals.record(context.Background(), claim, approvalStreamEvent(), false); err != nil {
			t.Fatal(err)
		}
		if !duringAuthorization {
			turn.active = false
		}
		if err := approvals.poll(context.Background(), claim); err != nil {
			t.Fatal(err)
		}
		if approvals.pending != nil || store.approval.State != "closed" || store.effectiveDecision != "" || turn.decisionID != "" {
			t.Fatal("native clearance reached reservation or delivery")
		}
	}
}

func TestInvocationRuntimeApprovalRejectsSubstitutedReservation(t *testing.T) {
	for _, mutate := range []func(*persistence.InvocationRuntimeApproval){
		func(value *persistence.InvocationRuntimeApproval) { value.Decision = "deny" },
		func(value *persistence.InvocationRuntimeApproval) { value.ResolvedBy = "other-controller" },
		func(value *persistence.InvocationRuntimeApproval) { value.ExpiresAt = value.ExpiresAt.Add(time.Minute) },
		func(value *persistence.InvocationRuntimeApproval) { value.EffectID = "other-effect" },
	} {
		claim, effect, target := runtimeDriverFixture()
		store := &runtimeApprovalStoreStub{}
		turn := &clearanceApprovalTurn{active: true}
		store.onBegin = func() { mutate(&store.approval) }
		driver := &InvocationRuntimeDriver{approvalStore: store, approvalAuthorizer: &approvalAuthorizerStub{}}
		approvals := &invocationRuntimeApprovals{driver: driver, target: target, effect: effect, turn: turn}
		if _, err := approvals.record(context.Background(), claim, approvalStreamEvent(), false); err != nil {
			t.Fatal(err)
		}
		if err := approvals.poll(context.Background(), claim); !errors.Is(err, persistence.ErrInvocationRuntimeApprovalConflict) {
			t.Fatalf("substituted reservation: %v", err)
		}
		if turn.decisionID != "" || store.completed {
			t.Fatal("substituted reservation delivered")
		}
	}
}

type combinedInteractionTurn struct {
	questionTurnStub
	events   <-chan dgruntime.Event
	done     chan struct{}
	approved bool
}

func (turn *combinedInteractionTurn) Events() <-chan dgruntime.Event { return turn.events }
func (turn *combinedInteractionTurn) Wait(ctx context.Context) error {
	select {
	case <-turn.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (turn *combinedInteractionTurn) ResolveApproval(ctx context.Context, id string, decision dgruntime.ApprovalDecision) error {
	if err := turn.approvalTurnStub.ResolveApproval(ctx, id, decision); err != nil {
		return err
	}
	turn.approved = true
	if !turn.active {
		close(turn.done)
	}
	return nil
}
func (turn *combinedInteractionTurn) AnswerQuestion(ctx context.Context, id string, answers []domain.QuestionAnswer) error {
	if err := turn.questionTurnStub.AnswerQuestion(ctx, id, answers); err != nil {
		return err
	}
	if turn.approved {
		close(turn.done)
	}
	return nil
}
func TestInvocationRuntimeApprovalAndQuestionCanWaitTogether(t *testing.T) {
	questions, questionStore, _, _, claim, questionEvent := questionMediationFixture(t)
	questionStore.autoAnswer = true
	approvalStore := &runtimeApprovalStoreStub{}
	runtimeStore := &runtimeStoreStub{}
	questions.driver.store = runtimeStore
	questions.driver.approvalStore = approvalStore
	questions.driver.approvalAuthorizer = &approvalAuthorizerStub{}
	questions.driver.leaseDuration = time.Minute
	questions.driver.renewInterval = 10 * time.Millisecond
	approvalEvent := approvalStreamEvent()
	approvalEvent.Sequence = 2
	turn := &combinedInteractionTurn{questionTurnStub: questionTurnStub{active: true}, events: runtimeEvents(questionEvent, approvalEvent, dgruntime.Event{Sequence: 3, Type: "output.text.delta", Payload: map[string]any{"text": "both complete"}}), done: make(chan struct{})}
	output, err := newInvocationRuntimeOutput(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := questions.driver.runTurn(ctx, claim, questions.effect, turn, output, questions.target, execution.ExecutionRef{}, nil, true, time.Minute); err != nil {
		t.Fatal(err)
	}
	if questionStore.value.State != "delivered" || approvalStore.approval.State != "delivered" || turn.answerCalls != 1 || turn.decisionID != "approval-1" || runtimeStore.completeCalls != 1 {
		t.Fatal("concurrent question and approval lost independent delivery")
	}
}

func TestInvocationRuntimeApprovalRoutingIsBoundedAndRejectsDuplicates(t *testing.T) {
	claim, effect, target := runtimeDriverFixture()
	store := &runtimeApprovalStoreStub{}
	set := &invocationRuntimeApprovalSet{template: invocationRuntimeApprovals{driver: &InvocationRuntimeDriver{approvalStore: store, approvalAuthorizer: &approvalAuthorizerStub{}}, target: target, effect: effect, turn: &approvalTurnStub{}}}
	for i := 1; i <= maximumPendingRuntimeApprovals; i++ {
		event := approvalStreamEvent()
		event.Sequence = uint64(i)
		event.Payload["approvalId"] = "approval-" + strconv.Itoa(i)
		if _, err := set.record(context.Background(), claim, event, false); err != nil {
			t.Fatal(err)
		}
	}
	if store.recordCalls != maximumPendingRuntimeApprovals {
		t.Fatal("routing lost an independent approval")
	}
	event := approvalStreamEvent()
	event.Sequence = maximumPendingRuntimeApprovals + 1
	event.Payload["approvalId"] = "approval-257"
	if handled, err := set.record(context.Background(), claim, event, false); !handled || !errors.Is(err, ErrInvocationApprovalUnavailable) || store.recordCalls != maximumPendingRuntimeApprovals {
		t.Fatal("unbounded approval routing reached persistence")
	}
	set.pending = set.pending[:1]
	for _, duplicate := range []dgruntime.Event{approvalStreamEvent(), {Sequence: 2, Type: "interaction.approval.requested", Payload: map[string]any{"approvalId": "approval-1", "action": "process.execute"}}} {
		if _, err := set.record(context.Background(), claim, duplicate, false); !errors.Is(err, persistence.ErrInvocationRuntimeApprovalConflict) {
			t.Fatalf("duplicate approval routing: %v", err)
		}
	}
	if store.recordCalls != maximumPendingRuntimeApprovals {
		t.Fatal("duplicate handle reached persistence")
	}
}

type unacknowledgedApprovalTurn struct {
	streamingApprovalTurn
	interruptedDeadline time.Time
}

func (turn *unacknowledgedApprovalTurn) Interrupt(ctx context.Context) error {
	turn.interruptedDeadline, _ = ctx.Deadline()
	if turn.interruptedDeadline.IsZero() {
		return errors.New("interrupt has no deadline")
	}
	<-ctx.Done()
	return ctx.Err()
}
func TestInvocationRuntimeExpiredApprovalDoesNotWaitForeverForInterruption(t *testing.T) {
	claim, effect, target := runtimeDriverFixture()
	store := &runtimeApprovalStoreStub{keepPending: true, lifetime: time.Millisecond}
	driver := &InvocationRuntimeDriver{store: &runtimeStoreStub{}, approvalStore: store, approvalAuthorizer: &approvalAuthorizerStub{}, leaseDuration: time.Minute, renewInterval: time.Second}
	turn := &unacknowledgedApprovalTurn{streamingApprovalTurn: streamingApprovalTurn{events: runtimeEvents(approvalStreamEvent()), done: make(chan struct{})}}
	output, err := newInvocationRuntimeOutput(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, err = driver.runTurn(ctx, claim, effect, turn, output, target, execution.ExecutionRef{}, nil, false, 0)
	if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || turn.interruptedDeadline.IsZero() || store.approval.State != "closed" || turn.decisionID != "" {
		t.Fatalf("unacknowledged interrupt blocked expiry cleanup: %v", err)
	}
}
