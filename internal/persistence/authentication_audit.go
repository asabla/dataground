package persistence

import (
	"context"
	"errors"
	"fmt"

	"github.com/asabla/dataground/internal/authn"
)

var ErrAuthenticationAttemptInvalid = errors.New("authentication attempt record is invalid")

func (repository *Repository) RecordAuthenticationAttempt(
	ctx context.Context,
	record authn.AuthenticationAttemptRecord,
) error {
	if repository == nil || repository.pool == nil || !record.Valid() {
		return ErrAuthenticationAttemptInvalid
	}
	var principalID, principalKind any
	if record.PrincipalID != "" {
		principalID = record.PrincipalID
		principalKind = string(record.PrincipalKind)
	}
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO authentication_attempts (
			isolation_domain_id,
			principal_id,
			principal_kind,
			method,
			outcome,
			correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6)
	`,
		record.IsolationDomainID,
		principalID,
		principalKind,
		string(record.Method),
		string(record.Outcome),
		record.CorrelationID,
	); err != nil {
		return fmt.Errorf("record authentication attempt: %w", err)
	}
	return nil
}

var _ authn.AuthenticationAttemptRecorder = (*Repository)(nil)
