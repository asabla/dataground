package persistence

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
)

type runtimeInteractionOutcome struct {
	isolationDomainID, invocationID, serviceID, revisionID string
	id, kind, state, actor, correlation, closeReason       string
	version                                                int64
	occurredAt, closedAt                                   time.Time
}

// Callers own the invocation row before changing the interaction. The event
// commits with that change, audit and outbox, without relying on a dispatcher.
func recordRuntimeInteractionOutcome(ctx context.Context, tx pgx.Tx, value runtimeInteractionOutcome) error {
	if value.kind != "approval" && value.kind != "question" {
		return ErrInvocationRuntimeEventInvalid
	}
	action := value.state
	payload := map[string]any{value.kind + "Id": value.id, "state": value.state, "version": value.version}
	switch value.state {
	case "answered":
		if value.kind != "question" {
			return ErrInvocationRuntimeEventInvalid
		}
	case "closed", "expired", "delivery_unknown":
		if value.closedAt.IsZero() || !validApprovalCloseReason(value.closeReason) {
			return ErrInvocationRuntimeEventInvalid
		}
		payload["closedAt"] = value.closedAt.UTC().Format(time.RFC3339Nano)
		payload["closeReason"] = value.closeReason
		if value.state == "delivery_unknown" {
			action = "delivery.unknown"
		}
	default:
		return ErrInvocationRuntimeEventInvalid
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO invocation_events (
 isolation_domain_id,invocation_id,id,sequence,schema_version,event_type,occurred_at,recorded_at,correlation_id,actor_id,service_id,revision_id,payload)
 SELECT $1,$2,$3,COALESCE(max(sequence),0)+1,'dataground.event/v1',$4,$5,clock_timestamp(),$6,$7,$8,$9,$10
 FROM invocation_events WHERE isolation_domain_id=$1 AND invocation_id=$2`, value.isolationDomainID, value.invocationID,
		identity.Derived("evt", value.isolationDomainID+":"+value.invocationID+":"+value.kind+":"+value.id+":"+strconv.FormatInt(value.version, 10)),
		"interaction."+value.kind+"."+action, value.occurredAt, value.correlation, value.actor, value.serviceID, value.revisionID, encoded)
	return err
}

// Expiry owns journal rows before interaction rows, matching answer, resolution
// and native event writes. Busy invocations remain eligible for a later sweep.
func lockDueRuntimeInteractionInvocations(ctx context.Context, tx pgx.Tx, scope, kind string, limit int) ([]string, error) {
	var due string
	switch kind {
	case "approval":
		due = `SELECT invocation_id,min(expires_at) AS expires_at FROM invocation_runtime_approvals
 WHERE isolation_domain_id=$1
 AND contract='dataground.invocation-runtime-approval/v2' AND state IN ('pending','resolved','delivering') AND expires_at<=clock_timestamp() GROUP BY invocation_id`
	case "question":
		due = `SELECT invocation_id,min(expires_at) AS expires_at FROM invocation_runtime_questions
 WHERE isolation_domain_id=$1
 AND state IN ('pending','answered','delivering') AND expires_at<=clock_timestamp() GROUP BY invocation_id`
	default:
		return nil, ErrInvocationRuntimeEventInvalid
	}
	rows, err := tx.Query(ctx, `SELECT invocation.id FROM invocations invocation
 JOIN (`+due+`) pending ON pending.invocation_id=invocation.id
 WHERE invocation.isolation_domain_id=$1 ORDER BY pending.expires_at,invocation.id
 LIMIT $2 FOR UPDATE OF invocation SKIP LOCKED`, scope, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
