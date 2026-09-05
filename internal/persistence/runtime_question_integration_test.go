package persistence_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/asabla/dataground/internal/reference"
	"github.com/jackc/pgx/v5/pgxpool"
)

type runtimeQuestionFixture struct {
	repository *persistence.Repository
	pool       *pgxpool.Pool
	claim      persistence.OperationClaim
	effect     persistence.EffectRecord
	target     persistence.InvocationRuntimeTarget
	sequence   uint64
}

func newRuntimeQuestionFixture(t *testing.T, ctx context.Context) *runtimeQuestionFixture {
	t.Helper()
	return newRuntimeQuestionFixtureWithReservation(t, ctx, true)
}

func newRuntimeQuestionFixtureWithReservation(t *testing.T, ctx context.Context, reserve bool) *runtimeQuestionFixture {
	t.Helper()
	pool := resetOperatorAuditDatabase(t, ctx)
	t.Cleanup(pool.Close)
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE invocation_runtime_questions`); err != nil {
			t.Error(err)
		}
	})
	repository := persistence.NewRepository(pool)
	domainID, serviceID, revisionID := identity.New("iso"), identity.New("svc"), identity.New("rev")
	if result, err := repository.CreateService(
		ctx,
		testIdempotency(domainID, "question-service"),
		persistence.CreateServiceInput{
			ID: serviceID, Name: "question-service", ActorID: "creator",
			CorrelationID: identity.New("cor"),
		},
	); err != nil || result.Status != http.StatusCreated {
		t.Fatalf("create service = (%d, %v)", result.Status, err)
	}
	if _, err := repository.CreateRevision(
		ctx,
		testIdempotency(domainID, "question-revision"),
		persistence.CreateRevisionInput{
			ID: revisionID, ServiceID: serviceID, RuntimeProfile: "reference/v1",
			RequiredCapabilities: []string{"tool"}, ActorID: "creator",
			CorrelationID: identity.New("cor"),
		},
	); err != nil {
		t.Fatal(err)
	}
	published, err := repository.AcceptPublication(
		ctx,
		testIdempotency(domainID, "question-publish"),
		persistence.AcceptPublicationInput{
			RevisionID: revisionID, ExpectedVersion: 1, ActorID: "publisher",
			CorrelationID: identity.New("cor"), Deadline: time.Now().Add(time.Minute),
		},
		reference.Capabilities(),
	)
	if err != nil {
		t.Fatal(err)
	}
	var publication domain.Operation
	if err := json.Unmarshal(published.Body, &publication); err != nil {
		t.Fatal(err)
	}
	worker := reconcile.New(repository, reconcile.NewReferenceDriver(pool), "question-setup")
	runToTerminal(t, ctx, worker, repository, domainID, publication.Metadata.ID, "published")
	if _, err := repository.AssignAlias(
		ctx,
		testIdempotency(domainID, "question-alias"),
		persistence.AssignAliasInput{
			ID: identity.New("als"), ServiceID: serviceID, Name: "stable",
			RevisionID: revisionID, ActorID: "publisher",
			CorrelationID: identity.New("cor"),
		},
	); err != nil {
		t.Fatal(err)
	}
	invocationID := identity.New("inv")
	accepted, err := repository.AcceptInvocation(
		ctx,
		testIdempotency(domainID, "question-invocation"),
		persistence.AcceptInvocationInput{
			ID: invocationID, ServiceID: serviceID, Alias: "stable",
			Input: map[string]any{"prompt": "change a file"}, ActorID: "requester",
			CorrelationID: identity.New("cor"), Deadline: time.Now().Add(time.Minute),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var invocation domain.Invocation
	if err := json.Unmarshal(accepted.Body, &invocation); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if ran, err := worker.RunOne(ctx, persistence.OperationKindInvocation); err != nil || !ran {
			t.Fatalf("advance invocation = (%t, %v)", ran, err)
		}
	}
	claim, err := repository.ClaimNext(
		ctx, persistence.OperationKindInvocation, "question-runtime", time.Minute,
	)
	if err != nil || claim == nil {
		t.Fatalf("claim runtime = (%#v, %v)", claim, err)
	}
	target, err := repository.GetClaimedInvocationRuntimeTarget(ctx, *claim)
	if err != nil {
		t.Fatal(err)
	}
	effect, err := repository.PrepareEffect(
		ctx,
		*claim,
		"run-invocation",
		sha256.Sum256([]byte(domainID+":"+invocation.OperationID+":question-runtime")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if reserve {
		if _, err := repository.BeginInvocationRuntimeAttempt(ctx, *claim, effect); err != nil {
			t.Fatal(err)
		}
	}
	return &runtimeQuestionFixture{repository: repository, pool: pool, claim: *claim, effect: effect, target: target}
}

func (fixture *runtimeQuestionFixture) request(t *testing.T, ctx context.Context, duration time.Duration) persistence.InvocationRuntimeQuestion {
	t.Helper()
	fixture.sequence++
	value, err := fixture.repository.RecordInvocationRuntimeQuestionRequest(ctx, fixture.claim, fixture.effect, fixture.target, persistence.InvocationRuntimeQuestionRequest{SourceSequence: fixture.sequence, Prompts: questionPrompts(), ExpiresAt: time.Now().Add(duration)})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func questionPrompts() []domain.QuestionPrompt {
	return []domain.QuestionPrompt{{ID: "item_1", Title: "Target", Prompt: "private prompt sentinel", Options: []domain.QuestionOption{{ID: "option_1", Label: "one", Description: "first target"}, {ID: "option_2", Label: "two", Description: "second target"}}, AllowFreeText: true}}
}
func questionAnswer(value persistence.InvocationRuntimeQuestion) persistence.InvocationRuntimeQuestionAnswer {
	text := "private answer sentinel"
	return persistence.InvocationRuntimeQuestionAnswer{IsolationDomainID: value.IsolationDomainID, InvocationID: value.InvocationID, QuestionID: value.ID, ExpectedVersion: 1, Answers: []domain.QuestionAnswer{{QuestionID: "item_1", Text: &text}}, ActorID: "answerer", CorrelationID: identity.New("cor")}
}
func allowQuestion(context.Context, persistence.InvocationRuntimeQuestion, string) error { return nil }

func TestInvocationRuntimeQuestionDurableSingleUseDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	value := fixture.request(t, ctx, 20*time.Second)
	repository := persistence.NewRepository(fixture.pool)
	request := persistence.InvocationRuntimeQuestionRequest{SourceSequence: value.SourceSequence, Prompts: questionPrompts(), ExpiresAt: value.ExpiresAt}
	replay, err := repository.RecordInvocationRuntimeQuestionRequest(ctx, fixture.claim, fixture.effect, fixture.target, request)
	if err != nil || replay.ID != value.ID || replay.Version != 1 {
		t.Fatalf("request replay: version %d, %v", replay.Version, err)
	}
	forged := fixture.target
	forged.ServiceID = identity.New("svc")
	if _, err := repository.RecordInvocationRuntimeQuestionRequest(ctx, fixture.claim, fixture.effect, forged, request); !errors.Is(err, persistence.ErrInvocationRuntimeQuestionConflict) {
		t.Fatalf("forged target: %v", err)
	}
	request.Prompts[0].Prompt = "changed"
	if _, err := repository.RecordInvocationRuntimeQuestionRequest(ctx, fixture.claim, fixture.effect, fixture.target, request); !errors.Is(err, persistence.ErrInvocationRuntimeQuestionConflict) {
		t.Fatalf("changed request: %v", err)
	}
	if _, err := repository.GetInvocationRuntimeQuestion(ctx, identity.New("iso"), value.InvocationID, value.ID); !errors.Is(err, persistence.ErrInvocationRuntimeQuestionMissing) {
		t.Fatalf("cross-domain read: %v", err)
	}
	if _, err := repository.GetInvocationRuntimeQuestion(ctx, value.IsolationDomainID, identity.New("inv"), value.ID); !errors.Is(err, persistence.ErrInvocationRuntimeQuestionMissing) {
		t.Fatalf("cross-invocation read: %v", err)
	}
	if _, err := json.Marshal(value); err == nil {
		t.Fatal("internal question was serializable")
	}
	answer := questionAnswer(value)
	denied := errors.New("policy denied")
	if _, err := repository.AnswerInvocationRuntimeQuestion(ctx, answer, nil); !errors.Is(err, persistence.ErrInvocationRuntimeQuestionInvalid) {
		t.Fatalf("missing authorization: %v", err)
	}
	if _, err := repository.AnswerInvocationRuntimeQuestion(ctx, answer, func(context.Context, persistence.InvocationRuntimeQuestion, string) error { return denied }); !errors.Is(err, denied) {
		t.Fatalf("entry denial: %v", err)
	}
	invalid := answer
	invalid.Answers = nil
	if _, err := repository.AnswerInvocationRuntimeQuestion(ctx, invalid, allowQuestion); !errors.Is(err, persistence.ErrInvocationRuntimeQuestionInvalid) {
		t.Fatalf("missing answers: %v", err)
	}
	value, err = repository.AnswerInvocationRuntimeQuestion(ctx, answer, func(_ context.Context, candidate persistence.InvocationRuntimeQuestion, phase string) error {
		if phase != persistence.InvocationQuestionEntry || candidate.AnsweredBy != answer.ActorID || *candidate.Answers[0].Text != "private answer sentinel" {
			t.Fatal("incorrect entry authorization binding")
		}
		*candidate.Answers[0].Text = "mutated callback copy"
		return nil
	})
	if err != nil || value.State != "answered" || value.Version != 2 || *value.Answers[0].Text != "private answer sentinel" {
		t.Fatalf("answer: state %s version %d, %v", value.State, value.Version, err)
	}
	extendedClaim := fixture.claim
	extendedClaim.DeadlineAt = time.Now().Add(time.Hour)
	if _, err := repository.RecordInvocationRuntimeQuestionRequest(ctx, extendedClaim, fixture.effect, fixture.target, persistence.InvocationRuntimeQuestionRequest{SourceSequence: 99, Prompts: questionPrompts(), ExpiresAt: time.Now().Add(2 * time.Minute)}); !errors.Is(err, persistence.ErrInvocationRuntimeQuestionExpired) {
		t.Fatalf("caller extended durable deadline: %v", err)
	}
	originalCorrelation := value.AnswerCorrelationID
	answer.CorrelationID = identity.New("cor")
	replay, err = repository.AnswerInvocationRuntimeQuestion(ctx, answer, allowQuestion)
	if err != nil || replay.Version != 2 || replay.AnswerCorrelationID != originalCorrelation {
		t.Fatalf("answer replay: version %d, %v", replay.Version, err)
	}
	if _, err := repository.AnswerInvocationRuntimeQuestion(ctx, answer, func(context.Context, persistence.InvocationRuntimeQuestion, string) error { return denied }); !errors.Is(err, denied) {
		t.Fatalf("replay authorization: %v", err)
	}
	otherActor := answer
	otherActor.ActorID = "other-answerer"
	if _, err := repository.AnswerInvocationRuntimeQuestion(ctx, otherActor, allowQuestion); !errors.Is(err, persistence.ErrInvocationRuntimeQuestionConflict) {
		t.Fatalf("actor substitution: %v", err)
	}
	if _, err := repository.BeginInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, value.ID, func(context.Context, persistence.InvocationRuntimeQuestion, string) error { return denied }); !errors.Is(err, denied) {
		t.Fatalf("effect denial: %v", err)
	}
	value, err = repository.BeginInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, value.ID, func(_ context.Context, candidate persistence.InvocationRuntimeQuestion, phase string) error {
		if phase != persistence.InvocationQuestionEffect || candidate.AnsweredBy != "answerer" || candidate.AnswerCorrelationID != originalCorrelation {
			t.Fatal("incorrect effect authorization binding")
		}
		return nil
	})
	if err != nil || value.State != "delivering" || value.Version != 3 {
		t.Fatalf("begin delivery: state %s version %d, %v", value.State, value.Version, err)
	}
	repository = persistence.NewRepository(fixture.pool)
	if _, err := repository.BeginInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, value.ID, allowQuestion); !errors.Is(err, persistence.ErrInvocationRuntimeQuestionDeliveryAmbiguous) {
		t.Fatalf("duplicate delivery: %v", err)
	}
	value, err = repository.CompleteInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, value.ID)
	if err != nil || value.State != "delivered" || value.Version != 4 {
		t.Fatalf("complete delivery: state %s version %d, %v", value.State, value.Version, err)
	}
	replay, err = repository.CompleteInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, value.ID)
	if err != nil || replay.Version != 4 {
		t.Fatalf("completion replay: version %d, %v", replay.Version, err)
	}
	var auditCount, outboxCount int
	var evidence string
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*),string_agg(safe_metadata::text,' ') FROM audit_records WHERE isolation_domain_id=$1 AND resource_id=$2`, value.IsolationDomainID, value.ID).Scan(&auditCount, &evidence); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND aggregate_id=$2`, value.IsolationDomainID, value.ID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 4 || outboxCount != 4 || strings.Contains(evidence, "sentinel") {
		t.Fatalf("audit/outbox evidence: %d/%d", auditCount, outboxCount)
	}
	var eventPayload string
	if err := fixture.pool.QueryRow(ctx, `SELECT payload::text FROM invocation_events WHERE isolation_domain_id=$1 AND invocation_id=$2 AND source_kind='runtime' AND source_sequence=$3`, value.IsolationDomainID, value.InvocationID, value.SourceSequence).Scan(&eventPayload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(eventPayload, "sentinel") || !strings.Contains(eventPayload, value.ID) {
		t.Fatal("unsafe question event")
	}
	for _, statement := range []string{
		`UPDATE invocation_runtime_questions SET prompts='[]' WHERE id=$1`,
		`UPDATE invocation_runtime_questions SET expires_at=expires_at+interval '1 second' WHERE id=$1`,
		`UPDATE invocation_runtime_questions SET state='answered',version=version+1 WHERE id=$1`,
		`UPDATE invocation_runtime_questions SET answers='[]',version=version+1 WHERE id=$1`,
		`DELETE FROM invocation_runtime_questions WHERE id=$1`,
	} {
		if _, err := fixture.pool.Exec(ctx, statement, value.ID); err == nil {
			t.Fatal("immutable question evidence changed")
		}
	}
	database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 49); err == nil {
		t.Fatal("retained question evidence allowed downgrade")
	}
}

func TestInvocationRuntimeQuestionClosureAndExpiry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	for _, state := range []string{"pending", "answered", "delivering"} {
		t.Run(state, func(t *testing.T) {
			value := fixture.request(t, ctx, 20*time.Second)
			if state != "pending" {
				if _, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, questionAnswer(value), allowQuestion); err != nil {
					t.Fatal(err)
				}
			}
			if state == "delivering" {
				if _, err := fixture.repository.BeginInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, value.ID, allowQuestion); err != nil {
					t.Fatal(err)
				}
			}
			closed, err := fixture.repository.CloseInvocationRuntimeQuestion(ctx, fixture.claim, fixture.effect, value.ID, "runtime-request-cleared")
			want := "closed"
			if state == "delivering" {
				want = "delivery_unknown"
			}
			if err != nil || closed.State != want || closed.CloseReason != "runtime-request-cleared" {
				t.Fatalf("closure: %s, %v", closed.State, err)
			}
			replay, err := fixture.repository.CloseInvocationRuntimeQuestion(ctx, fixture.claim, fixture.effect, value.ID, "runtime-ended")
			if err != nil || replay.Version != closed.Version || replay.CloseReason != closed.CloseReason {
				t.Fatalf("closure replay: %v", err)
			}
			if _, err := fixture.repository.BeginInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, value.ID, allowQuestion); err == nil {
				t.Fatal("closed question delivered")
			}
		})
	}
	values := make([]persistence.InvocationRuntimeQuestion, 0, 3)
	for _, state := range []string{"pending", "answered", "delivering"} {
		value := fixture.request(t, ctx, 500*time.Millisecond)
		if state != "pending" {
			if _, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, questionAnswer(value), allowQuestion); err != nil {
				t.Fatal(err)
			}
		}
		if state == "delivering" {
			if _, err := fixture.repository.BeginInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, value.ID, allowQuestion); err != nil {
				t.Fatal(err)
			}
		}
		values = append(values, value)
	}
	// Expiration must not depend on a surviving runtime lease.
	if _, err := fixture.pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp() WHERE isolation_domain_id=$1 AND id=$2`, fixture.claim.IsolationDomainID, fixture.claim.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(values[2].ExpiresAt) + 20*time.Millisecond)
	if _, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, questionAnswer(values[0]), allowQuestion); !errors.Is(err, persistence.ErrInvocationRuntimeQuestionExpired) {
		t.Fatalf("expired answer: %v", err)
	}
	if n, err := fixture.repository.ExpireInvocationRuntimeQuestions(ctx, identity.New("iso"), "worker", identity.New("cor"), 100); err != nil || n != 0 {
		t.Fatalf("cross-domain expiry: %d, %v", n, err)
	}
	for _, limit := range []int{1, 100} {
		want := 1
		if limit == 100 {
			want = 2
		}
		if n, err := fixture.repository.ExpireInvocationRuntimeQuestions(ctx, fixture.claim.IsolationDomainID, "worker", identity.New("cor"), limit); err != nil || n != want {
			t.Fatalf("expiry batch: %d, %v", n, err)
		}
	}
	if n, err := fixture.repository.ExpireInvocationRuntimeQuestions(ctx, fixture.claim.IsolationDomainID, "worker", identity.New("cor"), 100); err != nil || n != 0 {
		t.Fatalf("expiry replay: %d, %v", n, err)
	}
	for i, value := range values {
		actual, err := fixture.repository.GetInvocationRuntimeQuestion(ctx, value.IsolationDomainID, value.InvocationID, value.ID)
		want := "expired"
		if i == 2 {
			want = "delivery_unknown"
		}
		if err != nil || actual.State != want || actual.CloseReason != "expired" {
			t.Fatalf("durable expiry: %s, %v", actual.State, err)
		}
		if _, err := fixture.repository.BeginInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, value.ID, allowQuestion); err == nil {
			t.Fatal("expired delivery repeated")
		}
	}
}

func TestInvocationRuntimeQuestionRechecksAuthorizationBoundaries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	value := fixture.request(t, ctx, 150*time.Millisecond)
	_, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, questionAnswer(value), func(context.Context, persistence.InvocationRuntimeQuestion, string) error {
		time.Sleep(time.Until(value.ExpiresAt) + 20*time.Millisecond)
		return nil
	})
	if !errors.Is(err, persistence.ErrInvocationRuntimeQuestionExpired) {
		t.Fatalf("expiry during entry authorization: %v", err)
	}
	value = fixture.request(t, ctx, 150*time.Millisecond)
	if _, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, questionAnswer(value), allowQuestion); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.repository.BeginInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, value.ID, func(context.Context, persistence.InvocationRuntimeQuestion, string) error {
		time.Sleep(time.Until(value.ExpiresAt) + 20*time.Millisecond)
		return nil
	})
	if !errors.Is(err, persistence.ErrInvocationRuntimeQuestionExpired) {
		t.Fatalf("expiry during effect authorization: %v", err)
	}
	value = fixture.request(t, ctx, 20*time.Second)
	if _, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, questionAnswer(value), allowQuestion); err != nil {
		t.Fatal(err)
	}
	stale := fixture.claim
	stale.FencingToken++
	if _, err := fixture.repository.BeginInvocationRuntimeQuestionDelivery(ctx, stale, fixture.effect, value.ID, allowQuestion); err == nil {
		t.Fatal("stale claim delivered")
	}
	pending := fixture.request(t, ctx, 20*time.Second)
	if _, err := fixture.pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp()+interval '150 milliseconds' WHERE isolation_domain_id=$1 AND id=$2`, fixture.claim.IsolationDomainID, fixture.claim.ID); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.repository.AnswerInvocationRuntimeQuestion(ctx, questionAnswer(pending), func(context.Context, persistence.InvocationRuntimeQuestion, string) error {
		time.Sleep(200 * time.Millisecond)
		return nil
	})
	if !errors.Is(err, persistence.ErrInvocationRuntimeQuestionConflict) {
		t.Fatalf("lease expiry during entry authorization: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp()+interval '150 milliseconds' WHERE isolation_domain_id=$1 AND id=$2`, fixture.claim.IsolationDomainID, fixture.claim.ID); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.repository.BeginInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, value.ID, func(context.Context, persistence.InvocationRuntimeQuestion, string) error {
		time.Sleep(200 * time.Millisecond)
		return nil
	})
	if !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatalf("lease expiry during effect authorization: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=deadline_at,lease_token=lease_token+1 WHERE isolation_domain_id=$1 AND id=$2`, fixture.claim.IsolationDomainID, fixture.claim.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, questionAnswer(pending), allowQuestion); !errors.Is(err, persistence.ErrInvocationRuntimeQuestionConflict) {
		t.Fatalf("replacement worker answer: %v", err)
	}
	if _, err := fixture.repository.BeginInvocationRuntimeQuestionDelivery(ctx, stale, fixture.effect, value.ID, allowQuestion); !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatalf("replacement worker used old attempt: %v", err)
	}
}

func TestInvocationRuntimeQuestionCancellationFencesAnswers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	pending := fixture.request(t, ctx, 20*time.Second)
	answered := fixture.request(t, ctx, 20*time.Second)
	if _, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, questionAnswer(answered), allowQuestion); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.repository.AcceptCancellation(ctx, testIdempotency(fixture.claim.IsolationDomainID, "question-cancel"), persistence.AcceptCancellationInput{InvocationID: pending.InvocationID, ActorID: "requester", CorrelationID: identity.New("cor")})
	if err != nil || result.Status != http.StatusAccepted {
		t.Fatalf("cancel: %d, %v", result.Status, err)
	}
	if _, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, questionAnswer(pending), allowQuestion); !errors.Is(err, persistence.ErrInvocationRuntimeQuestionConflict) {
		t.Fatalf("answer after cancellation: %v", err)
	}
	if _, err := fixture.repository.BeginInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, answered.ID, allowQuestion); !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatalf("delivery after cancellation: %v", err)
	}
}

func TestInvocationRuntimeQuestionConcurrentAnswersAndDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	value := fixture.request(t, ctx, 20*time.Second)
	start := make(chan struct{})
	outcomes := make(chan error, 2)
	for _, text := range []string{"first answer", "second answer"} {
		go func(text string) {
			answer := questionAnswer(value)
			answer.Answers[0].Text = &text
			<-start
			_, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, answer, allowQuestion)
			outcomes <- err
		}(text)
	}
	close(start)
	success, conflict := 0, 0
	for range 2 {
		err := <-outcomes
		switch {
		case err == nil:
			success++
		case errors.Is(err, persistence.ErrInvocationRuntimeQuestionConflict):
			conflict++
		default:
			t.Fatalf("concurrent answer: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("answer ownership: %d success, %d conflict", success, conflict)
	}
	start = make(chan struct{})
	for range 2 {
		go func() {
			<-start
			_, err := fixture.repository.BeginInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, value.ID, allowQuestion)
			outcomes <- err
		}()
	}
	close(start)
	success, conflict = 0, 0
	for range 2 {
		err := <-outcomes
		switch {
		case err == nil:
			success++
		case errors.Is(err, persistence.ErrInvocationRuntimeQuestionDeliveryAmbiguous):
			conflict++
		default:
			t.Fatalf("concurrent delivery: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("delivery ownership: %d success, %d ambiguous", success, conflict)
	}
}

func TestInvocationRuntimeQuestionLockContentionAndAtomicEvidence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	value := fixture.request(t, ctx, 20*time.Second)
	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT id FROM invocation_execution_operations WHERE isolation_domain_id=$1 AND id=$2 FOR UPDATE`, fixture.claim.IsolationDomainID, fixture.claim.ID); err != nil {
		t.Fatal(err)
	}
	blockedCtx, stop := context.WithTimeout(ctx, time.Second)
	_, err = fixture.repository.AnswerInvocationRuntimeQuestion(blockedCtx, questionAnswer(value), allowQuestion)
	stop()
	if !errors.Is(err, persistence.ErrInvocationRuntimeQuestionConflict) {
		t.Fatalf("operation contention did not fail closed: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// Occupy the exact next outbox key to force a failure after the answer write
	// and audit insert. Neither state nor audit may survive without its outbox.
	duplicateID := identity.Derived("out", value.IsolationDomainID+":"+value.ID+":2")
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO outbox_events (id,isolation_domain_id,aggregate_type,aggregate_id,event_type,payload,correlation_id,available_at,created_at)
 SELECT $1,isolation_domain_id,aggregate_type,aggregate_id,event_type,payload,correlation_id,available_at,created_at FROM outbox_events WHERE isolation_domain_id=$2 AND aggregate_id=$3`, duplicateID, value.IsolationDomainID, value.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, questionAnswer(value), allowQuestion); err == nil {
		t.Fatal("answer committed without outbox")
	}
	actual, err := fixture.repository.GetInvocationRuntimeQuestion(ctx, value.IsolationDomainID, value.InvocationID, value.ID)
	if err != nil || actual.State != "pending" || actual.Version != 1 || len(actual.Answers) != 0 {
		t.Fatalf("answer rollback: state %s version %d, %v", actual.State, actual.Version, err)
	}
	var count int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND resource_id=$2`, value.IsolationDomainID, value.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit rollback: %d, %v", count, err)
	}
}
