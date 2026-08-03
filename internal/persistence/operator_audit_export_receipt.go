package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrOperatorAuditExportConflict = errors.New("operator audit export identifier was reused")
	ErrOperatorAuditExportCorrupt  = errors.New("operator audit export receipt is invalid")
	operatorAuditExportIDPattern   = regexp.MustCompile(`^oax_[0-9a-z]{20,32}$`)
)

type OperatorAuditExportRequest struct {
	ExportID          string
	IsolationDomainID string
	RequestedBy       string
	ReasonDigest      []byte
	CorrelationID     string
	Cursor            string
	Limit             int
}

type OperatorAuditExportContent struct {
	SchemaVersion     string                      `json:"schemaVersion"`
	ExportID          string                      `json:"exportId"`
	IsolationDomainID string                      `json:"isolationDomainId"`
	RequestedBy       string                      `json:"requestedBy"`
	CorrelationID     string                      `json:"correlationId"`
	Cursor            string                      `json:"cursor"`
	NextCursor        string                      `json:"nextCursor"`
	Complete          bool                        `json:"complete"`
	Records           []OperatorAuditExportRecord `json:"records"`
}

type OperatorAuditExportDocument struct {
	Content       OperatorAuditExportContent `json:"content"`
	ContentSHA256 string                     `json:"contentSha256"`
}

type operatorAuditExportReceipt struct {
	requestDigest []byte
	frozenCursor  string
	contentDigest []byte
	recordCount   int
}

func (repository *Repository) ExportOperatorAuditRecordsAudited(
	ctx context.Context,
	request OperatorAuditExportRequest,
) ([]byte, error) {
	if repository == nil || repository.pool == nil || ctx == nil || !validOperatorAuditExportRequest(request) {
		return nil, ErrOperatorAuditExportInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request = cloneOperatorAuditExportRequest(request)
	requestDigest, err := digestOperatorAuditExportRequest(request)
	if err != nil {
		return nil, err
	}
	receipt, exists, err := repository.operatorAuditExportReceipt(ctx, request.ExportID)
	if err != nil {
		return nil, err
	}
	frozenCursor := request.Cursor
	if exists {
		if !bytes.Equal(receipt.requestDigest, requestDigest[:]) {
			return nil, ErrOperatorAuditExportConflict
		}
		frozenCursor = receipt.frozenCursor
	}

	page, err := repository.ExportOperatorAuditRecords(ctx, request.IsolationDomainID, frozenCursor, request.Limit)
	if err != nil {
		return nil, err
	}
	if !exists && request.Cursor == "" {
		frozen, decodeErr := decodeOperatorAuditCursor(page.NextCursor)
		if decodeErr != nil {
			return nil, ErrOperatorAuditExportCorrupt
		}
		frozen.after = 0
		frozenCursor, err = encodeOperatorAuditCursor(frozen)
		if err != nil {
			return nil, ErrOperatorAuditExportCorrupt
		}
	}
	content := OperatorAuditExportContent{
		SchemaVersion:     OperatorAuditExportSchema,
		ExportID:          request.ExportID,
		IsolationDomainID: request.IsolationDomainID,
		RequestedBy:       request.RequestedBy,
		CorrelationID:     request.CorrelationID,
		Cursor:            request.Cursor,
		NextCursor:        page.NextCursor,
		Complete:          page.Complete,
		Records:           page.Records,
	}
	contentBytes, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("encode operator audit export content: %w", err)
	}
	contentDigest := sha256.Sum256(contentBytes)
	if exists {
		if len(receipt.contentDigest) != sha256.Size ||
			!bytes.Equal(receipt.contentDigest, contentDigest[:]) ||
			receipt.recordCount != len(content.Records) {
			return nil, ErrOperatorAuditExportCorrupt
		}
	} else if inserted, err := repository.recordOperatorAuditExport(
		ctx,
		request,
		requestDigest[:],
		frozenCursor,
		contentDigest[:],
		len(content.Records),
	); err != nil {
		return nil, err
	} else if !inserted {
		return repository.ExportOperatorAuditRecordsAudited(ctx, request)
	}
	document, err := json.Marshal(OperatorAuditExportDocument{
		Content:       content,
		ContentSHA256: "sha256:" + hex.EncodeToString(contentDigest[:]),
	})
	if err != nil {
		return nil, fmt.Errorf("encode operator audit export document: %w", err)
	}
	return document, nil
}

func (repository *Repository) operatorAuditExportReceipt(
	ctx context.Context,
	exportID string,
) (operatorAuditExportReceipt, bool, error) {
	var receipt operatorAuditExportReceipt
	err := repository.pool.QueryRow(ctx, `
		SELECT request_digest, frozen_cursor, content_digest, record_count
		FROM operator_audit_exports
		WHERE export_id = $1
	`, exportID).Scan(&receipt.requestDigest, &receipt.frozenCursor, &receipt.contentDigest, &receipt.recordCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return operatorAuditExportReceipt{}, false, nil
	}
	if err != nil {
		return operatorAuditExportReceipt{}, false, fmt.Errorf("read operator audit export receipt: %w", err)
	}
	if len(receipt.requestDigest) != sha256.Size || len(receipt.contentDigest) != sha256.Size ||
		receipt.recordCount < 0 || receipt.recordCount > maximumOperatorAuditExportLimit {
		return operatorAuditExportReceipt{}, false, ErrOperatorAuditExportCorrupt
	}
	if _, err := decodeOperatorAuditCursor(receipt.frozenCursor); err != nil {
		return operatorAuditExportReceipt{}, false, ErrOperatorAuditExportCorrupt
	}
	return receipt, true, nil
}

func (repository *Repository) recordOperatorAuditExport(
	ctx context.Context,
	request OperatorAuditExportRequest,
	requestDigest []byte,
	frozenCursor string,
	contentDigest []byte,
	recordCount int,
) (bool, error) {
	if len(requestDigest) != sha256.Size || len(contentDigest) != sha256.Size ||
		recordCount < 0 || recordCount > request.Limit {
		return false, ErrOperatorAuditExportInvalid
	}
	result, err := repository.pool.Exec(ctx, `
		INSERT INTO operator_audit_exports (
			export_id, isolation_domain_id, schema_version, requested_by, reason_digest,
			correlation_id, request_cursor, frozen_cursor, limit_value, record_count,
			request_digest, content_digest
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (export_id) DO NOTHING
	`, request.ExportID, request.IsolationDomainID, OperatorAuditExportSchema, request.RequestedBy,
		request.ReasonDigest, request.CorrelationID, request.Cursor, frozenCursor, request.Limit,
		recordCount, requestDigest, contentDigest)
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == "23505" {
			return false, ErrOperatorAuditExportConflict
		}
		return false, fmt.Errorf("record operator audit export receipt: %w", err)
	}
	return result.RowsAffected() == 1, nil
}

func validOperatorAuditExportRequest(request OperatorAuditExportRequest) bool {
	return operatorAuditExportIDPattern.MatchString(request.ExportID) &&
		operatorAuditDomainPattern.MatchString(request.IsolationDomainID) &&
		validOperatorAuditText(request.RequestedBy, 256) &&
		len(request.ReasonDigest) == sha256.Size &&
		operatorAuditExportCorrelation.MatchString(request.CorrelationID) &&
		request.Limit >= 1 && request.Limit <= maximumOperatorAuditExportLimit
}

func cloneOperatorAuditExportRequest(request OperatorAuditExportRequest) OperatorAuditExportRequest {
	request.ReasonDigest = append([]byte(nil), request.ReasonDigest...)
	return request
}

func digestOperatorAuditExportRequest(request OperatorAuditExportRequest) ([sha256.Size]byte, error) {
	encoded, err := json.Marshal(struct {
		SchemaVersion     string `json:"schemaVersion"`
		ExportID          string `json:"exportId"`
		IsolationDomainID string `json:"isolationDomainId"`
		RequestedBy       string `json:"requestedBy"`
		ReasonSHA256      string `json:"reasonSha256"`
		CorrelationID     string `json:"correlationId"`
		Cursor            string `json:"cursor"`
		Limit             int    `json:"limit"`
	}{
		SchemaVersion:     OperatorAuditExportSchema,
		ExportID:          request.ExportID,
		IsolationDomainID: request.IsolationDomainID,
		RequestedBy:       request.RequestedBy,
		ReasonSHA256:      hex.EncodeToString(request.ReasonDigest),
		CorrelationID:     request.CorrelationID,
		Cursor:            request.Cursor,
		Limit:             request.Limit,
	})
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("encode operator audit export request: %w", err)
	}
	return sha256.Sum256(encoded), nil
}
