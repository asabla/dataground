npm warn Unknown env config "http-proxy". This will stop working in the next major version of npm.
package persistence

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/jackc/pgx/v5"
)

const (
	AuthorizationAuditExportSchema  = "dataground.dev.authorization-audit-export/v1"
	maximumAuthorizationExportLimit = 1000
	authorizationExportCursorPrefix = "v1."
	authorizationExportCursorBytes  = 33
)

var (
	ErrAuthorizationExportInvalid    = errors.New("authorization audit export request is invalid")
	authorizationExportDomainPattern = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
)

type AuthorizationAuditExport struct {
	SchemaVersion     string                           `json:"schemaVersion"`
	IsolationDomainID string                           `json:"isolationDomainId"`
	Cursor            string                           `json:"cursor"`
	NextCursor        string                           `json:"nextCursor"`
	Complete          bool                             `json:"complete"`
	Records           []AuthorizationAuditExportRecord `json:"records"`
}

type AuthorizationAuditExportRecord struct {
	Source        string    `json:"source"`
	Sequence      string    `json:"sequence"`
	sequenceValue int64     `json:"-"`
	RecordedAt    time.Time `json:"recordedAt"`
	PrincipalID   string    `json:"principalId,omitempty"`
	PrincipalKind string    `json:"principalKind,omitempty"`
	ActorID       string    `json:"actorId,omitempty"`
	Action        string    `json:"action"`
	ResourceType  string    `json:"resourceType,omitempty"`
	ResourceID    string    `json:"resourceId,omitempty"`
	OperationID   string    `json:"operationId,omitempty"`
	InvocationID  string    `json:"invocationId,omitempty"`
	ServiceID     string    `json:"serviceId,omitempty"`
	RevisionID    string    `json:"revisionId,omitempty"`
	Outcome       string    `json:"outcome"`
	PolicySetID   string    `json:"policySetId"`
	PolicyDigest  string    `json:"policyDigest"`
	CorrelationID string    `json:"correlationId"`
}

type authorizationExportCursor struct {
	initialized       bool
	apiAfter          int64
	apiThrough        int64
	invocationAfter   int64
	invocationThrough int64
}

func (repository *Repository) ExportAuthorizationDecisions(
	ctx context.Context,
	isolationDomainID string,
	cursorValue string,
	limit int,
) (AuthorizationAuditExport, error) {
	if repository == nil ||
		repository.pool == nil ||
		!authorizationExportDomainPattern.MatchString(isolationDomainID) ||
		limit < 1 ||
		limit > maximumAuthorizationExportLimit {
		return AuthorizationAuditExport{}, ErrAuthorizationExportInvalid
	}
	cursor, err := decodeAuthorizationExportCursor(cursorValue)
	if err != nil {
		return AuthorizationAuditExport{}, ErrAuthorizationExportInvalid
	}
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return AuthorizationAuditExport{}, fmt.Errorf("begin authorization audit export: %w", err)
	}
	defer transaction.Rollback(ctx)

	var apiMaximum, invocationMaximum int64
	if err := transaction.QueryRow(ctx, `
		SELECT
			COALESCE((
				SELECT max(sequence)
				FROM api_authorization_decisions
				WHERE isolation_domain_id = $1
			), 0),
			COALESCE((
				SELECT max(sequence)
				FROM invocation_authorization_decisions
				WHERE isolation_domain_id = $1
			), 0)
	`, isolationDomainID).Scan(&apiMaximum, &invocationMaximum); err != nil {
		return AuthorizationAuditExport{}, fmt.Errorf("capture authorization audit export bounds: %w", err)
	}
	if !cursor.initialized {
		cursor.apiThrough = apiMaximum
		cursor.invocationThrough = invocationMaximum
		cursor.initialized = true
	} else if cursor.apiThrough > apiMaximum || cursor.invocationThrough > invocationMaximum {
		return AuthorizationAuditExport{}, ErrAuthorizationExportInvalid
	}

	rows, err := transaction.Query(ctx, `
		SELECT
			source,
			sequence,
			recorded_at,
			principal_id,
			principal_kind,
			actor_id,
			action,
			resource_type,
			resource_id,
			operation_id,
			invocation_id,
			service_id,
			revision_id,
			outcome,
			policy_set_id,
			policy_digest,
			correlation_id
		FROM (
			SELECT
				'api'::text AS source,
				sequence,
				recorded_at,
				principal_id,
				principal_kind,
				''::text AS actor_id,
				action,
				resource_type,
				resource_id,
				''::text AS operation_id,
				''::text AS invocation_id,
				''::text AS service_id,
				''::text AS revision_id,
				outcome,
				policy_set_id,
				policy_digest,
				correlation_id
			FROM api_authorization_decisions
			WHERE isolation_domain_id = $1
			  AND sequence > $2
			  AND sequence <= $3
			UNION ALL
			SELECT
				'invocation'::text AS source,
				sequence,
				recorded_at,
				''::text AS principal_id,
				''::text AS principal_kind,
				actor_id,
				action,
				''::text AS resource_type,
				''::text AS resource_id,
				operation_id,
				invocation_id,
				service_id,
				revision_id,
				outcome,
				policy_set_id,
				policy_digest,
				correlation_id
			FROM invocation_authorization_decisions
			WHERE isolation_domain_id = $1
			  AND sequence > $4
			  AND sequence <= $5
		) AS decisions
		ORDER BY CASE source WHEN 'api' THEN 0 ELSE 1 END, sequence
		LIMIT $6
	`,
		isolationDomainID,
		cursor.apiAfter,
		cursor.apiThrough,
		cursor.invocationAfter,
		cursor.invocationThrough,
		limit+1,
	)
	if err != nil {
		return AuthorizationAuditExport{}, fmt.Errorf("read authorization audit export: %w", err)
	}
	defer rows.Close()

	records := make([]AuthorizationAuditExportRecord, 0, limit+1)
	for rows.Next() {
		var record AuthorizationAuditExportRecord
		if err := rows.Scan(
			&record.Source,
			&record.sequenceValue,
			&record.RecordedAt,
			&record.PrincipalID,
			&record.PrincipalKind,
			&record.ActorID,
			&record.Action,
			&record.ResourceType,
			&record.ResourceID,
			&record.OperationID,
			&record.InvocationID,
			&record.ServiceID,
			&record.RevisionID,
			&record.Outcome,
			&record.PolicySetID,
			&record.PolicyDigest,
			&record.CorrelationID,
		); err != nil {
			return AuthorizationAuditExport{}, fmt.Errorf("scan authorization audit export: %w", err)
		}
		record.Sequence = strconv.FormatInt(record.sequenceValue, 10)
		if !record.valid(isolationDomainID) {
			return AuthorizationAuditExport{}, errors.New("stored authorization audit record is invalid")
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return AuthorizationAuditExport{}, fmt.Errorf("iterate authorization audit export: %w", err)
	}
	rows.Close()

	complete := len(records) <= limit
	if !complete {
		records = records[:limit]
	}
	for _, record := range records {
		switch record.Source {
		case "api":
			cursor.apiAfter = record.sequenceValue
		case "invocation":
			cursor.invocationAfter = record.sequenceValue
		}
	}
	nextCursor, err := encodeAuthorizationExportCursor(cursor)
	if err != nil {
		return AuthorizationAuditExport{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return AuthorizationAuditExport{}, fmt.Errorf("finish authorization audit export: %w", err)
	}
	return AuthorizationAuditExport{
		SchemaVersion:     AuthorizationAuditExportSchema,
		IsolationDomainID: isolationDomainID,
		Cursor:            cursorValue,
		NextCursor:        nextCursor,
		Complete:          complete,
		Records:           records,
	}, nil
}

func (record AuthorizationAuditExportRecord) valid(isolationDomainID string) bool {
	if record.sequenceValue < 1 || record.Sequence != strconv.FormatInt(record.sequenceValue, 10) || record.RecordedAt.IsZero() {
		return false
	}
	switch record.Source {
	case "api":
		if record.ActorID != "" ||
			record.OperationID != "" ||
			record.InvocationID != "" ||
			record.ServiceID != "" ||
			record.RevisionID != "" {
			return false
		}
		return (authz.DecisionRecord{
			PrincipalID:       record.PrincipalID,
			PrincipalKind:     authn.PrincipalKind(record.PrincipalKind),
			IsolationDomainID: isolationDomainID,
			Action:            authz.Action(record.Action),
			ResourceType:      authz.ResourceType(record.ResourceType),
			ResourceID:        record.ResourceID,
			Outcome:           authz.Outcome(record.Outcome),
			PolicySetID:       record.PolicySetID,
			PolicyDigest:      record.PolicyDigest,
			CorrelationID:     record.CorrelationID,
		}).Valid()
	case "invocation":
		if record.PrincipalID != "" ||
			record.PrincipalKind != "" ||
			record.ResourceType != "" ||
			record.ResourceID != "" {
			return false
		}
		return (authz.InvocationDecisionRecord{
			ActorID:           record.ActorID,
			IsolationDomainID: isolationDomainID,
			OperationID:       record.OperationID,
			InvocationID:      record.InvocationID,
			ServiceID:         record.ServiceID,
			RevisionID:        record.RevisionID,
			Action:            authz.InvocationAction(record.Action),
			Outcome:           authz.Outcome(record.Outcome),
			PolicySetID:       record.PolicySetID,
			PolicyDigest:      record.PolicyDigest,
			CorrelationID:     record.CorrelationID,
		}).Valid()
	default:
		return false
	}
}

func decodeAuthorizationExportCursor(value string) (authorizationExportCursor, error) {
	if value == "" {
		return authorizationExportCursor{}, nil
	}
	if len(value) <= len(authorizationExportCursorPrefix) ||
		value[:len(authorizationExportCursorPrefix)] != authorizationExportCursorPrefix {
		return authorizationExportCursor{}, ErrAuthorizationExportInvalid
	}
	encoded, err := base64.RawURLEncoding.DecodeString(value[len(authorizationExportCursorPrefix):])
	if err != nil || len(encoded) != authorizationExportCursorBytes || encoded[0] != 1 {
		return authorizationExportCursor{}, ErrAuthorizationExportInvalid
	}
	values := [4]int64{}
	for index := range values {
		unsigned := binary.BigEndian.Uint64(encoded[1+index*8 : 9+index*8])
		if unsigned > math.MaxInt64 {
			return authorizationExportCursor{}, ErrAuthorizationExportInvalid
		}
		values[index] = int64(unsigned)
	}
	cursor := authorizationExportCursor{
		initialized:       true,
		apiAfter:          values[0],
		apiThrough:        values[1],
		invocationAfter:   values[2],
		invocationThrough: values[3],
	}
	if cursor.apiAfter > cursor.apiThrough ||
		cursor.invocationAfter > cursor.invocationThrough {
		return authorizationExportCursor{}, ErrAuthorizationExportInvalid
	}
	return cursor, nil
}

func encodeAuthorizationExportCursor(cursor authorizationExportCursor) (string, error) {
	if !cursor.initialized ||
		cursor.apiAfter < 0 ||
		cursor.apiThrough < 0 ||
		cursor.invocationAfter < 0 ||
		cursor.invocationThrough < 0 ||
		cursor.apiAfter > cursor.apiThrough ||
		cursor.invocationAfter > cursor.invocationThrough {
		return "", ErrAuthorizationExportInvalid
	}
	encoded := make([]byte, authorizationExportCursorBytes)
	encoded[0] = 1
	values := [...]int64{
		cursor.apiAfter,
		cursor.apiThrough,
		cursor.invocationAfter,
		cursor.invocationThrough,
	}
	for index, value := range values {
		binary.BigEndian.PutUint64(encoded[1+index*8:9+index*8], uint64(value))
	}
	return authorizationExportCursorPrefix + base64.RawURLEncoding.EncodeToString(encoded), nil
}
