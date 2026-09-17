package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
)

var ErrResourceAuditInvalid = errors.New("resource audit request is invalid")
var resourceAuditReceiptPattern = regexp.MustCompile(`^arr_[0-9a-z]{20,32}$`)
var resourceAuditDecisionPattern = regexp.MustCompile(`^ard_[0-9a-f]{32}$`)
var resourceAuditOperationPattern = regexp.MustCompile(`^op_[0-9a-z]{20,32}$`)
var resourceAuditRevisionPattern = regexp.MustCompile(`^rev_[0-9a-z]{20,32}$`)

// ReadResourceAudit commits a disclosure receipt before returning any records.
// Authorization belongs to the caller and must run again for every page.
func (repository *Repository) ReadResourceAudit(ctx context.Context, principal authn.Principal, scope, kind, id, correlation, cursor string, limit int) (domain.ResourceAuditPage, error) {
	if ctx == nil || !repository.Configured() || !principal.Valid() || !principal.AllowsIsolationDomain(scope) ||
		!questionScopePattern.MatchString(scope) || !operatorAuditExportCorrelation.MatchString(correlation) ||
		limit < 1 || limit > 100 || (cursor != "" && !resourceAuditReceiptPattern.MatchString(cursor)) ||
		!((kind == "service-revision" && resourceAuditRevisionPattern.MatchString(id)) || (kind == "invocation" && approvalInvocationPattern.MatchString(id))) {
		return domain.ResourceAuditPage{}, ErrResourceAuditInvalid
	}
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return domain.ResourceAuditPage{}, err
	}
	defer tx.Rollback(ctx)
	var operationID string
	resourceQuery := `SELECT COALESCE((SELECT id FROM service_publication_operations WHERE isolation_domain_id=$1 AND revision_id=$2),'') FROM service_revisions WHERE isolation_domain_id=$1 AND id=$2`
	if kind == "invocation" {
		resourceQuery = `SELECT operation_id FROM invocations WHERE isolation_domain_id=$1 AND id=$2`
	}
	if err := tx.QueryRow(ctx, resourceQuery, scope, id).Scan(&operationID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ResourceAuditPage{}, &DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Audit resource was not found."}
		}
		return domain.ResourceAuditPage{}, err
	}
	var snapshot string
	var afterSource int
	var afterSequence int64
	if cursor == "" {
		if err := tx.QueryRow(ctx, `SELECT pg_current_snapshot()::text`).Scan(&snapshot); err != nil {
			return domain.ResourceAuditPage{}, err
		}
	} else {
		err := tx.QueryRow(ctx, `SELECT snapshot::text,operation_id,after_source,after_sequence FROM resource_audit_read_receipts
            WHERE id=$1 AND isolation_domain_id=$2 AND resource_type=$3 AND resource_id=$4 AND principal_id=$5 AND principal_kind=$6 AND has_more`,
			cursor, scope, kind, id, principal.ID(), string(principal.Kind())).Scan(&snapshot, &operationID, &afterSource, &afterSequence)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ResourceAuditPage{}, ErrResourceAuditInvalid
		}
		if err != nil {
			return domain.ResourceAuditPage{}, err
		}
	}
	if len(snapshot) > 65536 {
		return domain.ResourceAuditPage{}, errors.New("resource audit snapshot exceeds limit")
	}
	page := domain.ResourceAuditPage{SchemaVersion: domain.ResourceAuditPageSchemaV1, IsolationDomainID: scope, ResourceType: kind, ResourceID: id, ReceiptID: identity.New("arr"), Items: []domain.ResourceAuditRecord{}}
	operationKind := OperationKindPublication
	if kind == "invocation" {
		operationKind = OperationKindInvocation
	}
	// Source order and each source's sequence form a stable total order. The
	// stored top-level transaction IDs exclude late commits, even when their
	// sequence was allocated before the last record on the previous page.
	lifecycleQuery := `SELECT sequence,id,'lifecycle',occurred_at,actor_id,action,outcome,correlation_id,COALESCE(operation_id,''),'','',''
        FROM audit_records WHERE isolation_domain_id=$1 AND ((resource_type=$2 AND resource_id=$3) OR (resource_type=$4 AND resource_id=$5 AND $5<>''))
        AND sequence>$6 AND pg_visible_in_snapshot(recorded_transaction_id,$7::pg_snapshot) ORDER BY sequence LIMIT $8`
	decisionQuery := `SELECT sequence,'ard_' || replace(public_audit_id::text,'-',''),'publication-authorization',recorded_at,actor_id,'publish',outcome,correlation_id,operation_id,policy_set_id,policy_digest,phase
        FROM publication_authorization_decisions WHERE isolation_domain_id=$1 AND revision_id=$2 AND sequence>$3
        AND pg_visible_in_snapshot(recorded_transaction_id,$4::pg_snapshot) ORDER BY sequence LIMIT $5`
	if kind == "invocation" {
		decisionQuery = `SELECT sequence,'ard_' || replace(public_audit_id::text,'-',''),'invocation-authorization',recorded_at,actor_id,action,outcome,correlation_id,operation_id,policy_set_id,policy_digest,''
        FROM invocation_authorization_decisions WHERE isolation_domain_id=$1 AND invocation_id=$2 AND sequence>$3
        AND pg_visible_in_snapshot(recorded_transaction_id,$4::pg_snapshot) ORDER BY sequence LIMIT $5`
	}
	lastSource, lastSequence := afterSource, afterSequence
	hasMore := false
	for source := afterSource; source < 2 && !hasMore; source++ {
		after := int64(0)
		if source == afterSource {
			after = afterSequence
		}
		remaining := limit + 1 - len(page.Items)
		query, args := decisionQuery, []any{scope, id, after, snapshot, remaining}
		if source == 0 {
			query, args = lifecycleQuery, []any{scope, kind, id, operationKind, operationID, after, snapshot, remaining}
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return domain.ResourceAuditPage{}, err
		}
		for rows.Next() {
			var item domain.ResourceAuditRecord
			var sequence int64
			if err := rows.Scan(&sequence, &item.ID, &item.Source, &item.RecordedAt, &item.ActorID, &item.Action, &item.Outcome, &item.CorrelationID, &item.OperationID, &item.PolicySetID, &item.PolicyDigest, &item.Phase); err != nil {
				rows.Close()
				return domain.ResourceAuditPage{}, err
			}
			if sequence < 1 {
				rows.Close()
				return domain.ResourceAuditPage{}, errors.New("invalid resource audit sequence")
			}
			item.RecordedAt = item.RecordedAt.UTC()
			if !validResourceAuditRecord(item) {
				rows.Close()
				return domain.ResourceAuditPage{}, errors.New("invalid resource audit record")
			}
			if len(page.Items) == limit {
				hasMore = true
				break
			}
			page.Items = append(page.Items, item)
			lastSource, lastSequence = source, sequence
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return domain.ResourceAuditPage{}, err
		}
	}
	if hasMore {
		page.NextCursor = page.ReceiptID
	}
	content, err := json.Marshal(page)
	if err != nil {
		return domain.ResourceAuditPage{}, err
	}
	digest := sha256.Sum256(content)
	_, err = tx.Exec(ctx, `INSERT INTO resource_audit_read_receipts
        (id,contract,isolation_domain_id,resource_type,resource_id,principal_id,principal_kind,correlation_id,request_cursor,snapshot,operation_id,after_source,after_sequence,has_more,limit_value,record_count,content_digest)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::pg_snapshot,$11,$12,$13,$14,$15,$16,$17)`,
		page.ReceiptID, page.SchemaVersion, scope, kind, id, principal.ID(), string(principal.Kind()), correlation, cursor, snapshot, operationID, lastSource, lastSequence, hasMore, limit, len(page.Items), digest[:])
	if err != nil {
		return domain.ResourceAuditPage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ResourceAuditPage{}, err
	}
	return page, nil
}

func validResourceAuditRecord(item domain.ResourceAuditRecord) bool {
	if item.RecordedAt.IsZero() || item.RecordedAt.Year() < 1 || item.RecordedAt.Year() > 9999 ||
		!validOperatorAuditText(item.ActorID, 256) || !operatorAuditVocabulary.MatchString(item.Action) ||
		!validOperatorAuditText(item.CorrelationID, 256) || (item.OperationID != "" && !resourceAuditOperationPattern.MatchString(item.OperationID)) {
		return false
	}
	if item.Source == "lifecycle" {
		if !operatorAuditIDPattern.MatchString(item.ID) {
			return false
		}
		switch item.Outcome {
		case "accepted", "succeeded", "failed", "cancelled", "denied":
			return true
		}
		return false
	}
	if !resourceAuditDecisionPattern.MatchString(item.ID) || !validOperatorAuditText(item.PolicySetID, 128) || !operatorAuditDigest.MatchString(item.PolicyDigest) {
		return false
	}
	switch item.Outcome {
	case "allowed", "denied", "unavailable":
	default:
		return false
	}
	if item.Source == "publication-authorization" {
		return item.Action == "publish" && (item.Phase == "entry" || item.Phase == "effect")
	}
	return item.Source == "invocation-authorization" && item.Phase == ""
}
