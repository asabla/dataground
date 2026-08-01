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
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var (
	ErrAuthorizationExportConflict = errors.New("authorization audit export identifier was reused")
	ErrAuthorizationExportCorrupt  = errors.New("authorization audit export receipt is invalid")

	authorizationExportIDPattern          = regexp.MustCompile(`^aex_[0-9a-z]{20,32}$`)
	authorizationExportCorrelationPattern = regexp.MustCompile(`^cor_[0-9a-z]{20,32}$`)
)

type AuthorizationAuditExportRequest struct {
	ExportID          string
	IsolationDomainID string
	RequestedBy       string
	ReasonDigest      []byte
	CorrelationID     string
	Cursor            string
	Limit             int
}

type AuthorizationAuditExportContent struct {
	SchemaVersion     string                           `json:"schemaVersion"`
	ExportID          string                           `json:"exportId"`
	IsolationDomainID string                           `json:"isolationDomainId"`
	RequestedBy       string                           `json:"requestedBy"`
	CorrelationID     string                           `json:"correlationId"`
	Cursor            string                           `json:"cursor"`
	NextCursor        string                           `json:"nextCursor"`
	Complete          bool                             `json:"complete"`
	Records           []AuthorizationAuditExportRecord `json:"records"`
}

type AuthorizationAuditExportDocument struct {
	Content       AuthorizationAuditExportContent `json:"content"`
	ContentSHA256 string                          `json:"contentSha256"`
}

type authorizationAuditExportReceipt struct {
	requestDigest []byte
	frozenCursor  string
	contentDigest []byte
	recordCount   int
}

func (repository *Repository) ExportAuthorizationDecisionsAudited(
	ctx context.Context,
	request AuthorizationAuditExportRequest,
) ([]byte, error) {
	if !validAuthorizationAuditExportRequest(request) {
		return nil, ErrAuthorizationExportInvalid
	}
	requestDigest, err := digestAuthorizationAuditExportRequest(request)
	if err != nil {
		return nil, err
	}
	receipt, exists, err := repository.authorizationAuditExportReceipt(ctx, request.ExportID)
	if err != nil {
		return nil, err
	}
	frozenCursor := request.Cursor
	if exists {
		if !bytes.Equal(receipt.requestDigest, requestDigest[:]) {
			return nil, ErrAuthorizationExportConflict
		}
		frozenCursor = receipt.frozenCursor
	}

	page, err := repository.ExportAuthorizationDecisions(
		ctx,
		request.IsolationDomainID,
		frozenCursor,
		request.Limit,
	)
	if err != nil {
		return nil, err
	}
	if !exists && request.Cursor == "" {
		frozen, decodeErr := decodeAuthorizationExportCursor(page.NextCursor)
		if decodeErr != nil {
			return nil, ErrAuthorizationExportCorrupt
		}
		frozen.apiAfter = 0
		frozen.invocationAfter = 0
		frozenCursor, err = encodeAuthorizationExportCursor(frozen)
		if err != nil {
			return nil, ErrAuthorizationExportCorrupt
		}
	}
	content := AuthorizationAuditExportContent{
		SchemaVersion:     AuthorizationAuditExportSchema,
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
		return nil, fmt.Errorf("encode authorization audit export content: %w", err)
	}
	contentDigest := sha256.Sum256(contentBytes)
	if exists {
		if len(receipt.contentDigest) != sha256.Size ||
			!bytes.Equal(receipt.contentDigest, contentDigest[:]) ||
			receipt.recordCount != len(content.Records) {
			return nil, ErrAuthorizationExportCorrupt
		}
	} else if err := repository.recordAuthorizationAuditExport(
		ctx,
		request,
		requestDigest[:],
		frozenCursor,
		contentDigest[:],
		len(content.Records),
	); err != nil {
		return nil, err
	}
	document, err := json.Marshal(AuthorizationAuditExportDocument{
		Content:       content,
		ContentSHA256: "sha256:" + hex.EncodeToString(contentDigest[:]),
	})
	if err != nil {
		return nil, fmt.Errorf("encode authorization audit export document: %w", err)
	}
	return document, nil
}

func (repository *Repository) authorizationAuditExportReceipt(
	ctx context.Context,
	exportID string,
) (authorizationAuditExportReceipt, bool, error) {
	if repository == nil || repository.pool == nil {
		return authorizationAuditExportReceipt{}, false, ErrAuthorizationExportInvalid
	}
	var receipt authorizationAuditExportReceipt
	err := repository.pool.QueryRow(ctx, `
		SELECT request_digest, frozen_cursor, content_digest, record_count
		FROM authorization_audit_exports
		WHERE export_id = $1
	`, exportID).Scan(
		&receipt.requestDigest,
		&receipt.frozenCursor,
		&receipt.contentDigest,
		&receipt.recordCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return authorizationAuditExportReceipt{}, false, nil
	}
	if err != nil {
		return authorizationAuditExportReceipt{}, false, fmt.Errorf("read authorization audit export receipt: %w", err)
	}
	if len(receipt.requestDigest) != sha256.Size ||
		len(receipt.contentDigest) != sha256.Size ||
		receipt.recordCount < 0 ||
		receipt.recordCount > maximumAuthorizationExportLimit {
		return authorizationAuditExportReceipt{}, false, ErrAuthorizationExportCorrupt
	}
	if _, err := decodeAuthorizationExportCursor(receipt.frozenCursor); err != nil {
		return authorizationAuditExportReceipt{}, false, ErrAuthorizationExportCorrupt
	}
	return receipt, true, nil
}

func (repository *Repository) recordAuthorizationAuditExport(
	ctx context.Context,
	request AuthorizationAuditExportRequest,
	requestDigest []byte,
	frozenCursor string,
	contentDigest []byte,
	recordCount int,
) error {
	if len(requestDigest) != sha256.Size ||
		len(contentDigest) != sha256.Size ||
		recordCount < 0 ||
		recordCount > request.Limit {
		return ErrAuthorizationExportInvalid
	}
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO authorization_audit_exports (
			export_id,
			isolation_domain_id,
			schema_version,
			requested_by,
			reason_digest,
			correlation_id,
			request_cursor,
			frozen_cursor,
			limit_value,
			record_count,
			request_digest,
			content_digest
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`,
		request.ExportID,
		request.IsolationDomainID,
		AuthorizationAuditExportSchema,
		request.RequestedBy,
		request.ReasonDigest,
		request.CorrelationID,
		request.Cursor,
		frozenCursor,
		request.Limit,
		recordCount,
		requestDigest,
		contentDigest,
	); err != nil {
		return fmt.Errorf("record authorization audit export receipt: %w", err)
	}
	return nil
}

func validAuthorizationAuditExportRequest(request AuthorizationAuditExportRequest) bool {
	return request.ExportID != "" &&
		authorizationExportIDPattern.MatchString(request.ExportID) &&
		authorizationExportDomainPattern.MatchString(request.IsolationDomainID) &&
		validAuthorizationExportActor(request.RequestedBy) &&
		len(request.ReasonDigest) == sha256.Size &&
		authorizationExportCorrelationPattern.MatchString(request.CorrelationID) &&
		request.Limit >= 1 &&
		request.Limit <= maximumAuthorizationExportLimit
}

func validAuthorizationExportActor(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func digestAuthorizationAuditExportRequest(
	request AuthorizationAuditExportRequest,
) ([sha256.Size]byte, error) {
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
		SchemaVersion:     AuthorizationAuditExportSchema,
		ExportID:          request.ExportID,
		IsolationDomainID: request.IsolationDomainID,
		RequestedBy:       request.RequestedBy,
		ReasonSHA256:      hex.EncodeToString(request.ReasonDigest),
		CorrelationID:     request.CorrelationID,
		Cursor:            request.Cursor,
		Limit:             request.Limit,
	})
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("encode authorization audit export request: %w", err)
	}
	return sha256.Sum256(encoded), nil
}
