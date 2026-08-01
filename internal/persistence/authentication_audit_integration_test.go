package persistence_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestAuthenticationAttemptsAreMinimizedScopedAndAppendOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL := testDatabaseURL(t)
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateDownTo(ctx, database, 0); err != nil {
		database.Close()
		t.Fatalf("reset schema: %v", err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		database.Close()
		t.Fatalf("migrate schema: %v", err)
	}
	database.Close()
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repository := persistence.NewRepository(pool)

	domainID := identity.New("iso")
	principalID := identity.New("usr")
	authenticatedCorrelation := identity.New("cor")
	if err := repository.RecordAuthenticationAttempt(ctx, authn.AuthenticationAttemptRecord{
		IsolationDomainID: domainID,
		PrincipalID:       principalID,
		PrincipalKind:     authn.PrincipalHuman,
		Method:            authn.AuthenticationMethodOIDC,
		Outcome:           authn.AuthenticationOutcomeAuthenticated,
		CorrelationID:     authenticatedCorrelation,
	}); err != nil {
		t.Fatalf("record authenticated attempt: %v", err)
	}
	rejectedCorrelation := identity.New("cor")
	if err := repository.RecordAuthenticationAttempt(ctx, authn.AuthenticationAttemptRecord{
		IsolationDomainID: domainID,
		Method:            authn.AuthenticationMethodOIDC,
		Outcome:           authn.AuthenticationOutcomeRejected,
		CorrelationID:     rejectedCorrelation,
	}); err != nil {
		t.Fatalf("record rejected attempt: %v", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT principal_id, principal_kind, method, outcome, correlation_id
		FROM authentication_attempts
		WHERE isolation_domain_id = $1
		ORDER BY sequence
	`, domainID)
	if err != nil {
		t.Fatalf("read authentication attempts: %v", err)
	}
	defer rows.Close()
	type observedAttempt struct {
		principalID   *string
		principalKind *string
		method        string
		outcome       string
		correlationID string
	}
	var attempts []observedAttempt
	for rows.Next() {
		var attempt observedAttempt
		if err := rows.Scan(
			&attempt.principalID,
			&attempt.principalKind,
			&attempt.method,
			&attempt.outcome,
			&attempt.correlationID,
		); err != nil {
			t.Fatalf("scan authentication attempt: %v", err)
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate authentication attempts: %v", err)
	}
	rows.Close()
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d, want 2", len(attempts))
	}
	if attempts[0].principalID == nil || *attempts[0].principalID != principalID ||
		attempts[0].principalKind == nil || *attempts[0].principalKind != string(authn.PrincipalHuman) ||
		attempts[0].method != string(authn.AuthenticationMethodOIDC) ||
		attempts[0].outcome != string(authn.AuthenticationOutcomeAuthenticated) ||
		attempts[0].correlationID != authenticatedCorrelation {
		t.Fatalf("authenticated attempt = %#v", attempts[0])
	}
	if attempts[1].principalID != nil || attempts[1].principalKind != nil ||
		attempts[1].outcome != string(authn.AuthenticationOutcomeRejected) ||
		attempts[1].correlationID != rejectedCorrelation {
		t.Fatalf("rejected attempt = %#v", attempts[1])
	}

	if _, err := pool.Exec(ctx, `
		UPDATE authentication_attempts
		SET outcome = 'rejected'
		WHERE isolation_domain_id = $1
	`, domainID); err == nil {
		t.Fatal("authentication attempt update was accepted")
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM authentication_attempts
		WHERE isolation_domain_id = $1
	`, domainID); err == nil {
		t.Fatal("authentication attempt deletion was accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO authentication_attempts (
			isolation_domain_id, principal_id, principal_kind, method, outcome, correlation_id
		) VALUES ($1, $2, NULL, 'oidc', 'authenticated', $3)
	`, domainID, principalID, identity.New("cor")); err == nil {
		t.Fatal("partially attributed authentication attempt was accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO authentication_attempts (
			isolation_domain_id, principal_id, principal_kind, method, outcome, correlation_id
		) VALUES ($1, $2, 'service', 'development-bearer', 'authenticated', $3)
	`, domainID, principalID, identity.New("cor")); err == nil {
		t.Fatal("impossible development principal kind was accepted")
	}
	if err := repository.RecordAuthenticationAttempt(ctx, authn.AuthenticationAttemptRecord{
		IsolationDomainID: domainID,
		Method:            authn.AuthenticationMethodOIDC,
		Outcome:           authn.AuthenticationOutcomeRejected,
		CorrelationID:     rejectedCorrelation,
	}); err == nil {
		t.Fatal("duplicate domain correlation was accepted")
	}
	if err := repository.RecordAuthenticationAttempt(ctx, authn.AuthenticationAttemptRecord{
		IsolationDomainID: domainID,
		PrincipalID:       principalID,
		PrincipalKind:     authn.PrincipalHuman,
		Method:            authn.AuthenticationMethodOIDC,
		Outcome:           authn.AuthenticationOutcomeRejected,
		CorrelationID:     identity.New("cor"),
	}); !errors.Is(err, persistence.ErrAuthenticationAttemptInvalid) {
		t.Fatalf("invalid record error = %v", err)
	}
}
