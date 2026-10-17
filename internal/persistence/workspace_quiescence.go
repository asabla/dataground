package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/workspace"
	"github.com/jackc/pgx/v5"
)

const WorkspaceQuiescenceContract = "dataground.invocation-workspace-quiescence/v1"

var ErrWorkspaceQuiescence = errors.New("workspace cleanup is unconfirmed or conflicts with its original runtime")
var workspaceOwnerBindingPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// InvocationWorkspaceQuiescence is an internal retained observation, not export
// or teardown authority. The database-authorized controller must confirm the
// original owner's terminal exchange before recording completion.
type InvocationWorkspaceQuiescence struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Source        workspace.SourceRecord `json:"source"`
	InvocationID  string                 `json:"invocationId"`
	OperationID   string                 `json:"operationId"`
	EffectID      string                 `json:"effectId"`
	ExecutionID   string                 `json:"executionId"`
	PlanDigest    string                 `json:"planDigest"`
	OwnerBinding  string                 `json:"ownerBinding"`
	RuntimeOwner  string                 `json:"runtimeOwner"`
	RuntimeToken  int64                  `json:"runtimeToken"`
	ActorID       string                 `json:"actorId"`
	CorrelationID string                 `json:"correlationId"`
}

type InvocationWorkspaceQuiescenceEntry struct {
	Record    InvocationWorkspaceQuiescence
	Confirmed bool
	ExpiresAt time.Time
}

// BeginInvocationWorkspaceQuiescence commits immutable intent before cleanup.
// Only the original runtime claim can begin or complete it. Replacement workers
// can inspect retained completion, but cannot manufacture a new owner's proof.
func (repository *Repository) BeginInvocationWorkspaceQuiescence(ctx context.Context, claim OperationClaim, effect EffectRecord, expected execution.ExecutionRef, ownerBinding string) (InvocationWorkspaceQuiescenceEntry, error) {
	if !repository.Configured() || ctx == nil || !validInvocationRuntimeAttempt(claim, effect) || expected.IsolationDomainID != claim.IsolationDomainID || expected.ID == "" || !workspaceOwnerBindingPattern.MatchString(ownerBinding) {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	defer tx.Rollback(ctx)
	var record InvocationWorkspaceQuiescence
	var source []byte
	err = tx.QueryRow(ctx, `SELECT preparation.source_record,preparation.plan_digest,preparation.execution_id,attempt.lease_owner,attempt.fencing_token
 FROM invocation_runtime_attempts attempt JOIN invocation_workspace_preparations preparation
 ON preparation.isolation_domain_id=attempt.isolation_domain_id AND preparation.operation_id=attempt.operation_id
 AND preparation.fencing_token=attempt.fencing_token AND preparation.lease_owner=attempt.lease_owner AND preparation.effect_id=attempt.effect_id
 WHERE attempt.isolation_domain_id=$1 AND attempt.operation_id=$2 AND attempt.effect_id=$3`, claim.IsolationDomainID, claim.ID, effect.EffectID).Scan(&source, &record.PlanDigest, &record.ExecutionID, &record.RuntimeOwner, &record.RuntimeToken)
	if err != nil || json.Unmarshal(source, &record.Source) != nil || !workspace.ValidSourceRecord(record.Source) || record.ExecutionID != expected.ID || record.Source.IsolationDomainID != claim.IsolationDomainID || record.RuntimeOwner != claim.LeaseOwner || record.RuntimeToken != claim.FencingToken {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	record.SchemaVersion, record.InvocationID, record.OperationID, record.EffectID, record.OwnerBinding = WorkspaceQuiescenceContract, claim.ResourceID, claim.ID, effect.EffectID, ownerBinding
	record.ActorID, record.CorrelationID = claim.ActorID, claim.CorrelationID
	entry, found, err := readWorkspaceQuiescence(ctx, tx, claim.IsolationDomainID, claim.ID)
	if err != nil || (found && entry.Record != record) {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	if found && entry.Confirmed {
		return entry, nil
	}
	if verifyInvocationRuntimeEffect(ctx, tx, effect) != nil {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	encoded, expires, err := lockWorkspaceQuiescence(ctx, tx, claim, record)
	if err != nil {
		return InvocationWorkspaceQuiescenceEntry{}, err
	}
	result, err := tx.Exec(ctx, `INSERT INTO invocation_workspace_quiescence_intents(isolation_domain_id,operation_id,record,runtime_token,actor_id,correlation_id)
 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, claim.IsolationDomainID, claim.ID, encoded, record.RuntimeToken, claim.ActorID, claim.CorrelationID)
	if err != nil {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	entry, found, err = readWorkspaceQuiescence(ctx, tx, claim.IsolationDomainID, claim.ID)
	if err != nil || !found || entry.Record != record {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	if result.RowsAffected() == 1 {
		if err := writeOutboxAndAudit(ctx, tx, claim.IsolationDomainID, "workspace-quiescence", claim.ID, "workspace.cleanup.prepared", claim.ActorID, claim.CorrelationID, "accepted", claim.ID, repository.now()); err != nil {
			return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
		}
	}
	if tx.Commit(ctx) != nil {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	entry.ExpiresAt = expires
	return entry, nil
}

func (repository *Repository) CompleteInvocationWorkspaceQuiescence(ctx context.Context, claim OperationClaim, effect EffectRecord, record InvocationWorkspaceQuiescence) (InvocationWorkspaceQuiescenceEntry, error) {
	if !repository.Configured() || ctx == nil || !validInvocationRuntimeAttempt(claim, effect) || record.Source.IsolationDomainID != claim.IsolationDomainID || record.OperationID != claim.ID || record.InvocationID != claim.ResourceID || record.EffectID != effect.EffectID || record.RuntimeOwner != claim.LeaseOwner || record.RuntimeToken != claim.FencingToken || record.ActorID != claim.ActorID || record.CorrelationID != claim.CorrelationID {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	defer tx.Rollback(ctx)
	entry, found, err := readWorkspaceQuiescence(ctx, tx, claim.IsolationDomainID, claim.ID)
	if err != nil || !found || entry.Record != record {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	if entry.Confirmed {
		return entry, nil
	}
	if verifyInvocationRuntimeEffect(ctx, tx, effect) != nil {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	if _, _, err := lockWorkspaceQuiescence(ctx, tx, claim, record); err != nil {
		return InvocationWorkspaceQuiescenceEntry{}, err
	}
	// Reobserve under the operation lock so concurrent exact completion is one
	// append-only record and one audit event.
	entry, _, err = readWorkspaceQuiescence(ctx, tx, claim.IsolationDomainID, claim.ID)
	if err != nil {
		return InvocationWorkspaceQuiescenceEntry{}, err
	}
	if entry.Confirmed {
		return entry, nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO invocation_workspace_quiescence_completions(isolation_domain_id,operation_id,actor_id,correlation_id) VALUES($1,$2,$3,$4)`, claim.IsolationDomainID, claim.ID, claim.ActorID, claim.CorrelationID); err != nil {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	if err := writeOutboxAndAudit(ctx, tx, claim.IsolationDomainID, "workspace-quiescence", claim.ID, "workspace.cleanup.confirmed", claim.ActorID, claim.CorrelationID, "succeeded", claim.ID, repository.now()); err != nil {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	if tx.Commit(ctx) != nil {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	return InvocationWorkspaceQuiescenceEntry{Record: record, Confirmed: true}, nil
}

// GetInvocationWorkspaceQuiescence is read-only historical observation. An
// absent or pending record must retain the workspace. It grants no new lease.
func (repository *Repository) GetInvocationWorkspaceQuiescence(ctx context.Context, scope, operation string) (InvocationWorkspaceQuiescenceEntry, error) {
	if !repository.Configured() || ctx == nil {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	entry, found, err := readWorkspaceQuiescence(ctx, repository.pool, scope, operation)
	if err != nil || !found {
		return InvocationWorkspaceQuiescenceEntry{}, ErrWorkspaceQuiescence
	}
	return entry, nil
}

func lockWorkspaceQuiescence(ctx context.Context, tx pgx.Tx, claim OperationClaim, record InvocationWorkspaceQuiescence) ([]byte, time.Time, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, time.Time{}, ErrWorkspaceQuiescence
	}
	var expires time.Time
	err = tx.QueryRow(ctx, `SELECT lock_workspace_quiescence_claim($1,$2,$3,$4,$5,$6,$7)`, claim.IsolationDomainID, claim.ID, claim.LeaseOwner, claim.FencingToken, claim.ActorID, claim.CorrelationID, encoded).Scan(&expires)
	if err != nil {
		return nil, time.Time{}, ErrWorkspaceQuiescence
	}
	return encoded, expires, nil
}

func readWorkspaceQuiescence(ctx context.Context, querier operationQuerier, scope, operation string) (InvocationWorkspaceQuiescenceEntry, bool, error) {
	var entry InvocationWorkspaceQuiescenceEntry
	var encoded []byte
	err := querier.QueryRow(ctx, `SELECT intent.record,completion.operation_id IS NOT NULL FROM invocation_workspace_quiescence_intents intent
 LEFT JOIN invocation_workspace_quiescence_completions completion USING(isolation_domain_id,operation_id) WHERE intent.isolation_domain_id=$1 AND intent.operation_id=$2`, scope, operation).Scan(&encoded, &entry.Confirmed)
	if errors.Is(err, pgx.ErrNoRows) {
		return entry, false, nil
	}
	if err != nil || len(encoded) > 16<<10 {
		return entry, false, ErrWorkspaceQuiescence
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&entry.Record) != nil || entry.Record.SchemaVersion != WorkspaceQuiescenceContract || !workspace.ValidSourceRecord(entry.Record.Source) || entry.Record.Source.IsolationDomainID != scope || entry.Record.OperationID != operation || !workspaceOwnerBindingPattern.MatchString(entry.Record.OwnerBinding) {
		return InvocationWorkspaceQuiescenceEntry{}, false, ErrWorkspaceQuiescence
	}
	return entry, true, nil
}
