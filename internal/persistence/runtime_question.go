package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
)

const InvocationRuntimeQuestionContract = "dataground.invocation-runtime-question/v1"

var (
	ErrInvocationRuntimeQuestionInvalid           = errors.New("invocation runtime question is invalid")
	ErrInvocationRuntimeQuestionMissing           = errors.New("invocation runtime question is missing")
	ErrInvocationRuntimeQuestionConflict          = errors.New("invocation runtime question conflicts with durable state")
	ErrInvocationRuntimeQuestionExpired           = errors.New("invocation runtime question is expired")
	ErrInvocationRuntimeQuestionDeliveryAmbiguous = errors.New("invocation runtime question delivery is ambiguous")
	questionIDPattern                             = regexp.MustCompile(`^qst_[0-9a-z]{20,32}$`)
	questionScopePattern                          = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
)

type InvocationRuntimeQuestion struct {
	Contract            string
	IsolationDomainID   string
	ID                  string
	OperationID         string
	InvocationID        string
	ServiceID           string
	RevisionID          string
	EffectID            string
	SourceSequence      uint64
	CorrelationID       string
	RequestedBy         string
	Prompts             []domain.QuestionPrompt
	ExpiresAt           time.Time
	State               string
	Version             int64
	Answers             []domain.QuestionAnswer
	AnsweredBy          string
	AnswerCorrelationID string
	AnsweredAt          time.Time
	DeliveryStartedAt   time.Time
	DeliveredAt         time.Time
	ClosedAt            time.Time
	CloseReason         string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Question answers and prompts require an explicit authorized projection.
func (InvocationRuntimeQuestion) MarshalJSON() ([]byte, error) {
	return nil, errors.New("internal runtime question records cannot be serialized")
}

type InvocationRuntimeQuestionRequest struct {
	SourceSequence uint64
	Prompts        []domain.QuestionPrompt
	ExpiresAt      time.Time
}
type InvocationRuntimeQuestionAnswer struct {
	IsolationDomainID string
	InvocationID      string
	QuestionID        string
	ExpectedVersion   int64
	Answers           []domain.QuestionAnswer
	ActorID           string
	CorrelationID     string
}

// Authorization is mandatory at answer entry and again at delivery reservation.
// Implementations own policy resolution and its durable decision audit.
type InvocationRuntimeQuestionAuthorizer func(context.Context, InvocationRuntimeQuestion, string) error

const (
	InvocationQuestionEntry  = "entry"
	InvocationQuestionEffect = "effect"
)

func (repository *Repository) RecordInvocationRuntimeQuestionRequest(ctx context.Context, claim OperationClaim, effect EffectRecord, target InvocationRuntimeTarget, request InvocationRuntimeQuestionRequest) (InvocationRuntimeQuestion, error) {
	if repository == nil || repository.pool == nil || ctx == nil || !validInvocationRuntimeClaim(claim) || !validInvocationRuntimeAttempt(claim, effect) || !invocationRuntimeTargetMatchesApproval(target, claim) || request.SourceSequence == 0 || domain.ValidateQuestionPrompts(request.Prompts) != nil {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionInvalid
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockRuntimeInteractionAttempt(ctx, tx, claim, effect); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	actual, err := getInvocationRuntimeTarget(ctx, tx, target.IsolationDomainID, target.OperationID)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if actual.InvocationID != target.InvocationID || actual.ServiceID != target.ServiceID || actual.RevisionID != target.RevisionID || actual.ActorID != target.ActorID || actual.CorrelationID != target.CorrelationID {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionConflict
	}
	id := identity.Derived("qst", target.IsolationDomainID+":"+target.OperationID+":"+strconv.FormatUint(request.SourceSequence, 10))
	existing, found, err := getInvocationRuntimeQuestion(ctx, tx, target.IsolationDomainID, id, false)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if found {
		if existing.EffectID != effect.EffectID || !sameQuestionJSON(existing.Prompts, request.Prompts) || !existing.ExpiresAt.Equal(request.ExpiresAt.Truncate(time.Microsecond)) {
			return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionConflict
		}
		return existing, nil
	}
	var now, deadline time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp(),deadline_at FROM invocation_execution_operations WHERE isolation_domain_id=$1 AND id=$2`, claim.IsolationDomainID, claim.ID).Scan(&now, &deadline); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	expiry := request.ExpiresAt.UTC().Truncate(time.Microsecond)
	if !expiry.After(now) || expiry.After(now.Add(15*time.Minute)) || expiry.After(deadline) {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionExpired
	}
	prompts, _ := json.Marshal(request.Prompts)
	_, err = tx.Exec(ctx, `INSERT INTO invocation_runtime_questions
  (contract,isolation_domain_id,id,operation_id,invocation_id,service_id,revision_id,effect_id,source_sequence,correlation_id,requested_by,prompts,expires_at,state,version,created_at,updated_at)
  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'pending',1,$14,$14)`, InvocationRuntimeQuestionContract, target.IsolationDomainID, id, target.OperationID, target.InvocationID, target.ServiceID, target.RevisionID, effect.EffectID, request.SourceSequence, target.CorrelationID, target.ActorID, prompts, expiry, now)
	if err != nil {
		return InvocationRuntimeQuestion{}, fmt.Errorf("persist runtime question: %w", err)
	}
	value, _, err := getInvocationRuntimeQuestion(ctx, tx, target.IsolationDomainID, id, false)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if err := repository.recordRuntimeQuestionEvent(ctx, tx, claim, target, value); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if err := recordRuntimeQuestionChange(ctx, tx, value, "requested", value.RequestedBy, value.CorrelationID, now); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	return value, nil
}

func (repository *Repository) GetInvocationRuntimeQuestion(ctx context.Context, scope, invocationID, id string) (InvocationRuntimeQuestion, error) {
	if repository == nil || repository.pool == nil || ctx == nil || !questionScopePattern.MatchString(scope) || !approvalInvocationPattern.MatchString(invocationID) || !questionIDPattern.MatchString(id) {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionInvalid
	}
	value, found, err := getInvocationRuntimeQuestion(ctx, repository.pool, scope, id, false)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if !found || value.InvocationID != invocationID {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionMissing
	}
	return value, nil
}

func sameQuestionJSON(left, right any) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

type runtimeQuestionQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getInvocationRuntimeQuestion(ctx context.Context, query runtimeQuestionQuerier, scope, id string, locked bool) (InvocationRuntimeQuestion, bool, error) {
	suffix := ""
	if locked {
		suffix = " FOR UPDATE"
	}
	var value InvocationRuntimeQuestion
	var prompts, answers []byte
	var answeredAt, startedAt, deliveredAt, closedAt *time.Time
	err := query.QueryRow(ctx, `SELECT contract,isolation_domain_id,id,operation_id,invocation_id,service_id,revision_id,effect_id,source_sequence,
 correlation_id,requested_by,prompts,expires_at,state,version,answers,COALESCE(answered_by,''),COALESCE(answer_correlation_id,''),answered_at,delivery_started_at,delivered_at,closed_at,COALESCE(close_reason,''),created_at,updated_at
 FROM invocation_runtime_questions WHERE isolation_domain_id=$1 AND id=$2`+suffix, scope, id).Scan(&value.Contract, &value.IsolationDomainID, &value.ID, &value.OperationID, &value.InvocationID, &value.ServiceID, &value.RevisionID, &value.EffectID, &value.SourceSequence, &value.CorrelationID, &value.RequestedBy, &prompts, &value.ExpiresAt, &value.State, &value.Version, &answers, &value.AnsweredBy, &value.AnswerCorrelationID, &answeredAt, &startedAt, &deliveredAt, &closedAt, &value.CloseReason, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, false, nil
	}
	if err != nil {
		return value, false, err
	}
	if json.Unmarshal(prompts, &value.Prompts) != nil || domain.ValidateQuestionPrompts(value.Prompts) != nil {
		return value, false, ErrInvocationRuntimeQuestionInvalid
	}
	if len(answers) > 0 && (json.Unmarshal(answers, &value.Answers) != nil || domain.ValidateQuestionAnswers(value.Prompts, value.Answers) != nil) {
		return value, false, ErrInvocationRuntimeQuestionInvalid
	}
	if answeredAt != nil {
		value.AnsweredAt = answeredAt.UTC()
	}
	if startedAt != nil {
		value.DeliveryStartedAt = startedAt.UTC()
	}
	if deliveredAt != nil {
		value.DeliveredAt = deliveredAt.UTC()
	}
	if closedAt != nil {
		value.ClosedAt = closedAt.UTC()
	}
	return value, true, nil
}

func recordRuntimeQuestionChange(ctx context.Context, tx pgx.Tx, value InvocationRuntimeQuestion, action, actor, correlation string, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_records (id,isolation_domain_id,actor_id,action,resource_type,resource_id,outcome,correlation_id,operation_id,safe_metadata,occurred_at)
 VALUES ($1,$2,$3,$4,'invocation-question',$5,'accepted',$6,$7,jsonb_build_object('state',$8::text,'version',$9::bigint),$10)`, identity.New("aud"), value.IsolationDomainID, actor, "invocation-question."+action, value.ID, correlation, value.OperationID, value.State, value.Version, now)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events (id,isolation_domain_id,aggregate_type,aggregate_id,event_type,payload,correlation_id,available_at,created_at)
 VALUES ($1,$2,'invocation-question',$3,$4,jsonb_build_object('questionId',$3::text,'state',$5::text,'version',$6::bigint),$7,$8,$8)`, identity.Derived("out", value.IsolationDomainID+":"+value.ID+":"+strconv.FormatInt(value.Version, 10)), value.IsolationDomainID, value.ID, "invocation-question."+action, value.State, value.Version, correlation, now)
	if err != nil {
		return err
	}
	switch action {
	case "answered", "closed", "expired", "delivery_unknown":
		return recordRuntimeInteractionOutcome(ctx, tx, runtimeInteractionOutcome{
			isolationDomainID: value.IsolationDomainID, invocationID: value.InvocationID, serviceID: value.ServiceID, revisionID: value.RevisionID,
			id: value.ID, kind: "question", state: value.State, version: value.Version, actor: actor, correlation: correlation,
			occurredAt: now, closedAt: value.ClosedAt, closeReason: value.CloseReason,
		})
	}
	return nil
}

func (repository *Repository) recordRuntimeQuestionEvent(ctx context.Context, tx pgx.Tx, claim OperationClaim, target InvocationRuntimeTarget, question InvocationRuntimeQuestion) error {
	return repository.recordInvocationRuntimeInteractionEvent(ctx, tx, claim, target, question.SourceSequence, "interaction.question.requested", map[string]any{
		"questionId": question.ID, "version": int64(1), "expiresAt": question.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
}
