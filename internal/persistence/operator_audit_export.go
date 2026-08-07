package persistence

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	OperatorAuditExportSchema       = "dataground.dev.operator-audit-export/v1"
	maximumOperatorAuditExportLimit = 1000
	maximumOperatorAuditMetadata    = 64 << 10
	operatorAuditCursorPrefix       = "v1."
	operatorAuditCursorBytes        = 17
)

var (
	ErrOperatorAuditExportInvalid  = errors.New("operator audit export request is invalid")
	operatorAuditIDPattern         = regexp.MustCompile(`^aud_[0-9a-z]{20,32}$`)
	operatorAuditDomainPattern     = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
	operatorAuditExportCorrelation = regexp.MustCompile(`^cor_[0-9a-z]{20,32}$`)
	operatorAuditResourceID        = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,127}$`)
	operatorAuditVocabulary        = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,127}$`)
	operatorAuditDigest            = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	operatorAuditProviderID        = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)
)

type OperatorAuditExport struct {
	SchemaVersion     string                      `json:"schemaVersion"`
	IsolationDomainID string                      `json:"isolationDomainId"`
	Cursor            string                      `json:"cursor"`
	NextCursor        string                      `json:"nextCursor"`
	Complete          bool                        `json:"complete"`
	Records           []OperatorAuditExportRecord `json:"records"`
}

type OperatorAuditExportRecord struct {
	Sequence      string          `json:"sequence"`
	sequenceValue int64           `json:"-"`
	AuditID       string          `json:"auditId"`
	RecordedAt    time.Time       `json:"recordedAt"`
	ActorID       string          `json:"actorId"`
	Action        string          `json:"action"`
	ResourceType  string          `json:"resourceType"`
	ResourceID    string          `json:"resourceId"`
	Outcome       string          `json:"outcome"`
	CorrelationID string          `json:"correlationId"`
	OperationID   string          `json:"operationId,omitempty"`
	SafeMetadata  json.RawMessage `json:"safeMetadata"`
}

type operatorAuditCursor struct {
	initialized bool
	after       int64
	through     int64
}

func (repository *Repository) ExportOperatorAuditRecords(
	ctx context.Context,
	isolationDomainID string,
	cursorValue string,
	limit int,
) (OperatorAuditExport, error) {
	if repository == nil || repository.pool == nil || ctx == nil ||
		!operatorAuditDomainPattern.MatchString(isolationDomainID) ||
		limit < 1 || limit > maximumOperatorAuditExportLimit {
		return OperatorAuditExport{}, ErrOperatorAuditExportInvalid
	}
	if err := ctx.Err(); err != nil {
		return OperatorAuditExport{}, err
	}
	cursor, err := decodeOperatorAuditCursor(cursorValue)
	if err != nil {
		return OperatorAuditExport{}, ErrOperatorAuditExportInvalid
	}
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return OperatorAuditExport{}, fmt.Errorf("begin operator audit export: %w", err)
	}
	defer tx.Rollback(ctx)

	var maximum int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(sequence), 0)
		FROM audit_records
		WHERE isolation_domain_id = $1
	`, isolationDomainID).Scan(&maximum); err != nil {
		return OperatorAuditExport{}, fmt.Errorf("capture operator audit export bound: %w", err)
	}
	if !cursor.initialized {
		cursor.initialized = true
		cursor.through = maximum
	} else if cursor.through > maximum {
		return OperatorAuditExport{}, ErrOperatorAuditExportInvalid
	}

	rows, err := tx.Query(ctx, `
		SELECT sequence, id, occurred_at, actor_id, action, resource_type,
		       resource_id, outcome, correlation_id, COALESCE(operation_id, ''), safe_metadata
		FROM audit_records
		WHERE isolation_domain_id = $1
		  AND sequence > $2
		  AND sequence <= $3
		ORDER BY sequence
		LIMIT $4
	`, isolationDomainID, cursor.after, cursor.through, limit+1)
	if err != nil {
		return OperatorAuditExport{}, fmt.Errorf("read operator audit export: %w", err)
	}
	defer rows.Close()

	records := make([]OperatorAuditExportRecord, 0, limit+1)
	for rows.Next() {
		var record OperatorAuditExportRecord
		if err := rows.Scan(
			&record.sequenceValue,
			&record.AuditID,
			&record.RecordedAt,
			&record.ActorID,
			&record.Action,
			&record.ResourceType,
			&record.ResourceID,
			&record.Outcome,
			&record.CorrelationID,
			&record.OperationID,
			&record.SafeMetadata,
		); err != nil {
			return OperatorAuditExport{}, fmt.Errorf("scan operator audit export: %w", err)
		}
		record.Sequence = strconv.FormatInt(record.sequenceValue, 10)
		if !record.valid() {
			return OperatorAuditExport{}, errors.New("stored operator audit record is invalid")
		}
		record.SafeMetadata = append(json.RawMessage(nil), record.SafeMetadata...)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return OperatorAuditExport{}, fmt.Errorf("iterate operator audit export: %w", err)
	}
	rows.Close()

	complete := len(records) <= limit
	if !complete {
		records = records[:limit]
	}
	if len(records) > 0 {
		cursor.after = records[len(records)-1].sequenceValue
	}
	nextCursor, err := encodeOperatorAuditCursor(cursor)
	if err != nil {
		return OperatorAuditExport{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return OperatorAuditExport{}, fmt.Errorf("finish operator audit export: %w", err)
	}
	return OperatorAuditExport{
		SchemaVersion:     OperatorAuditExportSchema,
		IsolationDomainID: isolationDomainID,
		Cursor:            cursorValue,
		NextCursor:        nextCursor,
		Complete:          complete,
		Records:           records,
	}, nil
}

func (record OperatorAuditExportRecord) valid() bool {
	if record.sequenceValue < 1 || record.Sequence != strconv.FormatInt(record.sequenceValue, 10) ||
		!operatorAuditIDPattern.MatchString(record.AuditID) || record.RecordedAt.IsZero() ||
		!validOperatorAuditText(record.ActorID, 256) ||
		!operatorAuditVocabulary.MatchString(record.Action) ||
		!operatorAuditVocabulary.MatchString(record.ResourceType) ||
		!operatorAuditResourceID.MatchString(record.ResourceID) ||
		!validOperatorAuditText(record.CorrelationID, 256) ||
		(record.OperationID != "" && !operatorAuditResourceID.MatchString(record.OperationID)) ||
		len(record.SafeMetadata) == 0 || len(record.SafeMetadata) > maximumOperatorAuditMetadata {
		return false
	}
	switch record.Outcome {
	case "accepted", "succeeded", "failed", "cancelled", "denied":
	default:
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(record.SafeMetadata))
	decoder.UseNumber()
	var metadata map[string]any
	if err := decoder.Decode(&metadata); err != nil || metadata == nil || len(metadata) > 32 {
		return false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return false
	}
	for key, value := range metadata {
		if !validOperatorAuditMetadataField(key, value) {
			return false
		}
	}
	return true
}

func validOperatorAuditMetadataField(key string, value any) bool {
	switch key {
	case "sensitive":
		_, ok := value.(bool)
		return ok
	case "generation", "proofingAuthorityGeneration", "recipientTrustGeneration", "revocationAuthorityGeneration",
		"revocationSourceGeneration", "workloadIdentityGeneration":
		return validOperatorAuditInteger(value, 1)
	case "recipientTrustKeyCount", "recipientEncryptionKeyCount":
		return validOperatorAuditIntegerRange(value, 0, 8)
	case "sizeBytes":
		return validOperatorAuditInteger(value, 0)
	}
	text, ok := value.(string)
	if !ok || len(text) > 512 || !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') {
		return false
	}
	switch key {
	case "acknowledgementDigest", "artifactDigest", "bindingDigest", "clientCertificateSha256", "destinationDigest",
		"encryptedPackageDigest", "envelopeDigest", "identityDigest", "planDigest", "policyDigest",
		"providerRegistrySha256", "publicationPathDigest", "reasonDigest",
		"revocationSourceRegistrySha256",
		"recipientIdentityProofSha256", "recipientProofRevocationSha256",
		"proofingAuthorityTrustProfileSha256", "recipientProofingTrustProfileSha256",
		"recipientTrustProfileSha256", "trustProfileSha256",
		"revocationAuthorityTrustProfileSha256",
		"workloadIdentityGrantSha256", "workloadIdentityRevocationSha256",
		"workloadIdentityTrustProfileSha256":
		return operatorAuditDigest.MatchString(text)
	case "principalId":
		return operatorAuditResourceID.MatchString(text)
	case "principalKind":
		return text == "human" || text == "service" || text == "platform-service" ||
			text == "sandbox-workload" || text == "distributed-compute-workload"
	case "policySetId":
		return validOperatorAuditText(text, 128)
	case "artifactKind":
		return operatorAuditVocabulary.MatchString(text)
	case "proofingAuthorityId", "providerId", "recipientProofingAuthorityId", "recipientRevocationAuthorityId",
		"revocationAuthorityId", "revocationSourceId",
		"workloadIdentityAuthorityId", "workloadIdentityRevocationAuthorityId":
		return operatorAuditProviderID.MatchString(text)
	case "recipientIdentityProofExpiresAt", "recipientProofRevocationEffectiveAt",
		"workloadIdentityExpiresAt", "workloadIdentityRevocationEffectiveAt":
		parsed, err := time.Parse(time.RFC3339Nano, text)
		return err == nil && parsed.Equal(parsed.UTC())
	case "recipientProofingSigningKeyId", "recipientEncryptionKeyId",
		"workloadIdentitySigningKeyId":
		return auditExportDeliveryKeyID.MatchString(text)
	case "recipientProofRevocationScope", "workloadIdentityRevocationScope":
		return text == "profile" || text == "key"
	case "revocationAuthorityPurpose":
		return text == AuditExportRevocationAuthorityPurposeRecipientProof ||
			text == AuditExportRevocationAuthorityPurposeWorkloadIdentity
	case "revocationSourcePurpose":
		return text == AuditExportRevocationAuthorityPurposeRecipientProof ||
			text == AuditExportRevocationAuthorityPurposeWorkloadIdentity
	case "endpoint":
		return text == "discovery" || text == "jwks"
	case "exportKind":
		return text == "authorization" || text == "operator"
	case "recipientId", "workloadId":
		return operatorAuditProviderID.MatchString(text)
	case "recipientSigningKeyId", "signingKeyId":
		return operatorAuditResourceID.MatchString(text)
	case "transportContract":
		return validAuditExportDeliveryTransportContract(text)
	default:
		return false
	}
}

func validOperatorAuditInteger(value any, minimum int64) bool {
	return validOperatorAuditIntegerRange(value, minimum, math.MaxInt64)
}

func validOperatorAuditIntegerRange(value any, minimum int64, maximum int64) bool {
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	parsed, err := strconv.ParseInt(string(number), 10, 64)
	return err == nil && parsed >= minimum && parsed <= maximum
}

func validOperatorAuditText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func decodeOperatorAuditCursor(value string) (operatorAuditCursor, error) {
	if value == "" {
		return operatorAuditCursor{}, nil
	}
	if len(value) <= len(operatorAuditCursorPrefix) || value[:len(operatorAuditCursorPrefix)] != operatorAuditCursorPrefix {
		return operatorAuditCursor{}, ErrOperatorAuditExportInvalid
	}
	encoded, err := base64.RawURLEncoding.DecodeString(value[len(operatorAuditCursorPrefix):])
	if err != nil || len(encoded) != operatorAuditCursorBytes || encoded[0] != 1 {
		return operatorAuditCursor{}, ErrOperatorAuditExportInvalid
	}
	afterValue := binary.BigEndian.Uint64(encoded[1:9])
	throughValue := binary.BigEndian.Uint64(encoded[9:17])
	if afterValue > math.MaxInt64 || throughValue > math.MaxInt64 || afterValue > throughValue {
		return operatorAuditCursor{}, ErrOperatorAuditExportInvalid
	}
	return operatorAuditCursor{initialized: true, after: int64(afterValue), through: int64(throughValue)}, nil
}

func encodeOperatorAuditCursor(cursor operatorAuditCursor) (string, error) {
	if !cursor.initialized || cursor.after < 0 || cursor.through < 0 || cursor.after > cursor.through {
		return "", ErrOperatorAuditExportInvalid
	}
	encoded := make([]byte, operatorAuditCursorBytes)
	encoded[0] = 1
	binary.BigEndian.PutUint64(encoded[1:9], uint64(cursor.after))
	binary.BigEndian.PutUint64(encoded[9:17], uint64(cursor.through))
	return operatorAuditCursorPrefix + base64.RawURLEncoding.EncodeToString(encoded), nil
}
