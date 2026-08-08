package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	ProviderCredentialGrantContract         = "dataground.provider-credential-grant/v1"
	ProviderCredentialAuthorizationContract = "dataground.provider-credential-authorization/v1"
	ProviderCredentialPurposeAgentInference = "agent-inference"
	ProviderCredentialPhaseAdmission        = "admission"
	ProviderCredentialPhaseEffect           = "effect"
)

var (
	ErrProviderCredentialGrantInvalid  = errors.New("provider credential grant is invalid")
	ErrProviderCredentialGrantConflict = errors.New("provider credential grant conflicts with durable state")
	ErrProviderCredentialUnauthorized  = errors.New("provider credential use is not authorized")

	providerCredentialDomainPattern      = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
	providerCredentialRevisionPattern    = regexp.MustCompile(`^rev_[0-9a-z]{20,32}$`)
	providerCredentialOperationPattern   = regexp.MustCompile(`^op_[0-9a-z]{20,32}$`)
	providerCredentialCorrelationPattern = regexp.MustCompile(`^cor_[0-9a-z]{20,32}$`)
	providerCredentialProfilePattern     = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
)

type ProviderCredentialGrantChange struct {
	Contract          string
	IsolationDomainID string
	RevisionID        string
	ProviderProfile   string
	Purpose           string
	Generation        int64
	Operation         string
	ActivatedAt       time.Time
	ExpiresAt         time.Time
	ActorID           string
	ReasonDigest      []byte
	CorrelationID     string
}

func (change ProviderCredentialGrantChange) Valid() bool {
	if change.Contract != ProviderCredentialGrantContract ||
		!providerCredentialDomainPattern.MatchString(change.IsolationDomainID) ||
		!providerCredentialRevisionPattern.MatchString(change.RevisionID) ||
		!validProviderCredentialProfile(change.ProviderProfile) ||
		change.Purpose != ProviderCredentialPurposeAgentInference ||
		change.Generation < 1 || change.Generation > math.MaxInt64 ||
		(change.Operation != "activate" && change.Operation != "revoke") ||
		!validProviderCredentialText(change.ActorID, 256) ||
		len(change.ReasonDigest) != sha256.Size ||
		!providerCredentialCorrelationPattern.MatchString(change.CorrelationID) {
		return false
	}
	if change.Operation == "revoke" {
		return change.ActivatedAt.IsZero() && change.ExpiresAt.IsZero()
	}
	return canonicalProviderCredentialTime(change.ActivatedAt) &&
		canonicalProviderCredentialTime(change.ExpiresAt) &&
		change.ExpiresAt.After(change.ActivatedAt) &&
		change.ExpiresAt.Sub(change.ActivatedAt) <= 24*time.Hour
}

type ProviderCredentialUse struct {
	Contract          string
	IsolationDomainID string
	RevisionID        string
	OperationID       string
	ProviderProfile   string
	Purpose           string
	Phase             string
	ActorID           string
	CorrelationID     string
}

func (use ProviderCredentialUse) Valid() bool {
	return use.Contract == ProviderCredentialAuthorizationContract &&
		providerCredentialDomainPattern.MatchString(use.IsolationDomainID) &&
		providerCredentialRevisionPattern.MatchString(use.RevisionID) &&
		providerCredentialOperationPattern.MatchString(use.OperationID) &&
		validProviderCredentialProfile(use.ProviderProfile) &&
		use.Purpose == ProviderCredentialPurposeAgentInference &&
		(use.Phase == ProviderCredentialPhaseAdmission || use.Phase == ProviderCredentialPhaseEffect) &&
		validProviderCredentialText(use.ActorID, 256) &&
		providerCredentialCorrelationPattern.MatchString(use.CorrelationID)
}

func (repository *Repository) ChangeProviderCredentialGrant(
	ctx context.Context,
	change ProviderCredentialGrantChange,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !change.Valid() {
		return ErrProviderCredentialGrantInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	change.ReasonDigest = append([]byte(nil), change.ReasonDigest...)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin provider credential grant change: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockProviderCredentialGrant(ctx, tx, change.IsolationDomainID, change.RevisionID, change.ProviderProfile, change.Purpose); err != nil {
		return err
	}
	existing, exists, err := readProviderCredentialGrant(ctx, tx, change)
	if err != nil {
		return err
	}
	if exists {
		if !sameProviderCredentialGrant(existing, change) {
			return ErrProviderCredentialGrantConflict
		}
		return tx.Commit(ctx)
	}
	var correlation string
	err = tx.QueryRow(ctx, `
		SELECT correlation_id
		FROM provider_credential_grant_events
		WHERE correlation_id = $1
	`, change.CorrelationID).Scan(&correlation)
	if err == nil {
		return ErrProviderCredentialGrantConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("inspect provider credential grant correlation: %w", err)
	}
	var activatedAt, expiresAt *time.Time
	if change.Operation == "activate" {
		activated, expires := change.ActivatedAt, change.ExpiresAt
		activatedAt, expiresAt = &activated, &expires
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_credential_grant_events (
			contract, isolation_domain_id, revision_id, provider_profile, purpose,
			generation, operation, activated_at, expires_at, actor_id,
			reason_digest, correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, change.Contract, change.IsolationDomainID, change.RevisionID, change.ProviderProfile,
		change.Purpose, change.Generation, change.Operation, activatedAt, expiresAt,
		change.ActorID, change.ReasonDigest, change.CorrelationID); err != nil {
		return mapProviderCredentialGrantWriteError(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, $4, 'provider-credential-grant', $5,
			'accepted', $6, jsonb_build_object(
				'generation', $7::bigint,
				'providerId', $8::text,
				'reasonDigest', $9::text
			), clock_timestamp()
		)
	`, identity.New("aud"), change.IsolationDomainID, change.ActorID,
		"provider-credential-grant."+change.Operation, change.RevisionID,
		change.CorrelationID, change.Generation, change.ProviderProfile,
		"sha256:"+hex.EncodeToString(change.ReasonDigest)); err != nil {
		return fmt.Errorf("audit provider credential grant change: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit provider credential grant change: %w", err)
	}
	return nil
}

func (repository *Repository) AuthorizeProviderCredentialUse(
	ctx context.Context,
	use ProviderCredentialUse,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !use.Valid() {
		return ErrProviderCredentialUnauthorized
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin provider credential authorization: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockProviderCredentialGrant(ctx, tx, use.IsolationDomainID, use.RevisionID, use.ProviderProfile, use.Purpose); err != nil {
		return err
	}
	var generation int64
	var operation string
	var activatedAt, expiresAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT generation, operation, activated_at, expires_at
		FROM provider_credential_grant_events
		WHERE isolation_domain_id = $1
		  AND revision_id = $2
		  AND provider_profile = $3
		  AND purpose = $4
		ORDER BY generation DESC
		LIMIT 1
	`, use.IsolationDomainID, use.RevisionID, use.ProviderProfile, use.Purpose).Scan(
		&generation, &operation, &activatedAt, &expiresAt,
	)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("resolve provider credential grant: %w", err)
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return fmt.Errorf("read provider credential authorization time: %w", err)
	}
	allowed := exists && operation == "activate" && activatedAt != nil && expiresAt != nil &&
		!now.Before(*activatedAt) && now.Before(*expiresAt)
	outcome := "denied"
	var grantGeneration any
	if exists {
		grantGeneration = generation
	}
	if allowed {
		outcome = "allowed"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_credential_authorization_decisions (
			id, contract, isolation_domain_id, revision_id, operation_id,
			provider_profile, purpose, phase, grant_generation, outcome,
			actor_id, correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, identity.New("pcd"), use.Contract, use.IsolationDomainID, use.RevisionID,
		use.OperationID, use.ProviderProfile, use.Purpose, use.Phase,
		grantGeneration, outcome, use.ActorID, use.CorrelationID); err != nil {
		return fmt.Errorf("record provider credential authorization: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit provider credential authorization: %w", err)
	}
	if !allowed {
		return ErrProviderCredentialUnauthorized
	}
	return nil
}

type providerCredentialGrantQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func lockProviderCredentialGrant(
	ctx context.Context,
	querier providerCredentialGrantQuerier,
	isolationDomainID string,
	revisionID string,
	providerProfile string,
	purpose string,
) error {
	if _, err := querier.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended(
			'provider-credential-grant' || E'\\n' || $1 || E'\\n' || $2 || E'\\n' || $3 || E'\\n' || $4,
			0
		))
	`, isolationDomainID, revisionID, providerProfile, purpose); err != nil {
		return fmt.Errorf("lock provider credential grant scope: %w", err)
	}
	return nil
}

func readProviderCredentialGrant(
	ctx context.Context,
	querier providerCredentialGrantQuerier,
	expected ProviderCredentialGrantChange,
) (ProviderCredentialGrantChange, bool, error) {
	var actual ProviderCredentialGrantChange
	var activatedAt, expiresAt *time.Time
	err := querier.QueryRow(ctx, `
		SELECT contract, isolation_domain_id, revision_id, provider_profile, purpose,
		       generation, operation, activated_at, expires_at, actor_id,
		       reason_digest, correlation_id
		FROM provider_credential_grant_events
		WHERE isolation_domain_id = $1
		  AND revision_id = $2
		  AND provider_profile = $3
		  AND purpose = $4
		  AND generation = $5
	`, expected.IsolationDomainID, expected.RevisionID, expected.ProviderProfile,
		expected.Purpose, expected.Generation).Scan(
		&actual.Contract, &actual.IsolationDomainID, &actual.RevisionID,
		&actual.ProviderProfile, &actual.Purpose, &actual.Generation,
		&actual.Operation, &activatedAt, &expiresAt, &actual.ActorID,
		&actual.ReasonDigest, &actual.CorrelationID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProviderCredentialGrantChange{}, false, nil
	}
	if err != nil {
		return ProviderCredentialGrantChange{}, false, fmt.Errorf("read provider credential grant: %w", err)
	}
	if activatedAt != nil {
		actual.ActivatedAt = *activatedAt
	}
	if expiresAt != nil {
		actual.ExpiresAt = *expiresAt
	}
	return actual, true, nil
}

func sameProviderCredentialGrant(left, right ProviderCredentialGrantChange) bool {
	return left.Contract == right.Contract &&
		left.IsolationDomainID == right.IsolationDomainID &&
		left.RevisionID == right.RevisionID &&
		left.ProviderProfile == right.ProviderProfile &&
		left.Purpose == right.Purpose &&
		left.Generation == right.Generation &&
		left.Operation == right.Operation &&
		left.ActivatedAt.Equal(right.ActivatedAt) &&
		left.ExpiresAt.Equal(right.ExpiresAt) &&
		left.ActorID == right.ActorID &&
		bytes.Equal(left.ReasonDigest, right.ReasonDigest) &&
		left.CorrelationID == right.CorrelationID
}

func mapProviderCredentialGrantWriteError(err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		switch databaseError.Code {
		case "23505", "23514", "P0001":
			return ErrProviderCredentialGrantConflict
		}
	}
	return fmt.Errorf("change provider credential grant: %w", err)
}

func validProviderCredentialProfile(value string) bool {
	return len(value) <= 64 && providerCredentialProfilePattern.MatchString(value)
}

func validProviderCredentialText(value string, maximum int) bool {
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

func canonicalProviderCredentialTime(value time.Time) bool {
	return !value.IsZero() && value.Equal(value.UTC()) && value.Nanosecond()%1000 == 0
}
