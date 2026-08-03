package persistence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// ValidateAuthorizationAuditExportDocument verifies the closed document contract
// independently of the database receipt that produced it.
func ValidateAuthorizationAuditExportDocument(document AuthorizationAuditExportDocument) error {
	content := document.Content
	if content.SchemaVersion != AuthorizationAuditExportSchema ||
		!authorizationExportIDPattern.MatchString(content.ExportID) ||
		!authorizationExportDomainPattern.MatchString(content.IsolationDomainID) ||
		!validAuthorizationExportActor(content.RequestedBy) ||
		!authorizationExportCorrelationPattern.MatchString(content.CorrelationID) ||
		len(content.Records) > maximumAuthorizationExportLimit {
		return ErrAuthorizationExportInvalid
	}
	cursor, err := decodeAuthorizationExportCursor(content.Cursor)
	if err != nil {
		return ErrAuthorizationExportInvalid
	}
	next, err := decodeAuthorizationExportCursor(content.NextCursor)
	if err != nil || !next.initialized {
		return ErrAuthorizationExportInvalid
	}
	if cursor.initialized &&
		(cursor.apiThrough != next.apiThrough || cursor.invocationThrough != next.invocationThrough) {
		return ErrAuthorizationExportInvalid
	}

	apiAfter, invocationAfter := int64(0), int64(0)
	if cursor.initialized {
		apiAfter = cursor.apiAfter
		invocationAfter = cursor.invocationAfter
	}
	invocationStarted := false
	for index := range content.Records {
		record := &content.Records[index]
		sequence, parseErr := strconv.ParseInt(record.Sequence, 10, 64)
		if parseErr != nil || sequence < 1 || record.RecordedAt.Location() != time.UTC {
			return ErrAuthorizationExportInvalid
		}
		record.sequenceValue = sequence
		if !record.valid(content.IsolationDomainID) {
			return ErrAuthorizationExportInvalid
		}
		switch record.Source {
		case "api":
			if invocationStarted || sequence <= apiAfter || sequence > next.apiThrough {
				return ErrAuthorizationExportInvalid
			}
			apiAfter = sequence
		case "invocation":
			invocationStarted = true
			if sequence <= invocationAfter || sequence > next.invocationThrough {
				return ErrAuthorizationExportInvalid
			}
			invocationAfter = sequence
		default:
			return ErrAuthorizationExportInvalid
		}
	}
	if apiAfter != next.apiAfter || invocationAfter != next.invocationAfter {
		return ErrAuthorizationExportInvalid
	}
	if content.Complete &&
		(next.apiAfter != next.apiThrough || next.invocationAfter != next.invocationThrough) {
		return ErrAuthorizationExportInvalid
	}
	if !content.Complete && next.apiAfter == next.apiThrough && next.invocationAfter == next.invocationThrough {
		return ErrAuthorizationExportInvalid
	}
	return validateAuditExportContentDigest(content, document.ContentSHA256)
}

// ValidateOperatorAuditExportDocument verifies the closed document contract
// independently of the database receipt that produced it.
func ValidateOperatorAuditExportDocument(document OperatorAuditExportDocument) error {
	content := document.Content
	if content.SchemaVersion != OperatorAuditExportSchema ||
		!operatorAuditExportIDPattern.MatchString(content.ExportID) ||
		!operatorAuditDomainPattern.MatchString(content.IsolationDomainID) ||
		!validOperatorAuditText(content.RequestedBy, 256) ||
		!operatorAuditExportCorrelation.MatchString(content.CorrelationID) ||
		len(content.Records) > maximumOperatorAuditExportLimit {
		return ErrOperatorAuditExportInvalid
	}
	cursor, err := decodeOperatorAuditCursor(content.Cursor)
	if err != nil {
		return ErrOperatorAuditExportInvalid
	}
	next, err := decodeOperatorAuditCursor(content.NextCursor)
	if err != nil || !next.initialized {
		return ErrOperatorAuditExportInvalid
	}
	if cursor.initialized && cursor.through != next.through {
		return ErrOperatorAuditExportInvalid
	}
	after := int64(0)
	if cursor.initialized {
		after = cursor.after
	}
	for index := range content.Records {
		record := &content.Records[index]
		sequence, parseErr := strconv.ParseInt(record.Sequence, 10, 64)
		if parseErr != nil || sequence <= after || sequence > next.through ||
			record.RecordedAt.Location() != time.UTC {
			return ErrOperatorAuditExportInvalid
		}
		record.sequenceValue = sequence
		if !record.valid() {
			return ErrOperatorAuditExportInvalid
		}
		after = sequence
	}
	if after != next.after {
		return ErrOperatorAuditExportInvalid
	}
	if content.Complete && next.after != next.through {
		return ErrOperatorAuditExportInvalid
	}
	if !content.Complete && next.after == next.through {
		return ErrOperatorAuditExportInvalid
	}
	return validateAuditExportContentDigest(content, document.ContentSHA256)
}

func validateAuditExportContentDigest(content any, value string) error {
	encoded, err := json.Marshal(content)
	if err != nil {
		return errors.New("audit export content is invalid")
	}
	digest := sha256.Sum256(encoded)
	want := "sha256:" + hex.EncodeToString(digest[:])
	if !bytes.Equal([]byte(value), []byte(want)) {
		return errors.New("audit export content digest does not match")
	}
	return nil
}
