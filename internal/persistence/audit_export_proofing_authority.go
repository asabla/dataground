package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const AuditExportProofingAuthorityAuthorizationContract = "dataground.audit-export-proofing-authority-authorization/v1"

const auditExportRecipientProofingTrustContract = "dataground.audit-export-recipient-proofing-trust/ed25519/v1"

var (
	ErrAuditExportProofingAuthorityInvalid      = errors.New("audit export proofing authority change is invalid")
	ErrAuditExportProofingAuthorityConflict     = errors.New("audit export proofing authority change conflicts with durable state")
	ErrAuditExportProofingAuthorityUnauthorized = errors.New("audit export proofing authority is not authorized")
)

type AuditExportProofingAuthorityChange struct {
	Contract           string
	Operation          string
	IsolationDomainID  string
	AuthorityID        string
	Generation         int64
	TrustContract      string
	TrustProfileSHA256 string
	KeyIDs             []string
	ActorID            string
	ReasonDigest       []byte
	CorrelationID      string
}

func (change AuditExportProofingAuthorityChange) Valid() bool {
	return change.Contract == AuditExportProofingAuthorityAuthorizationContract &&
		(change.Operation == "activate" || change.Operation == "revoke") &&
		operatorAuditDomainPattern.MatchString(change.IsolationDomainID) &&
		auditExportDeliveryRecipient.MatchString(change.AuthorityID) &&
		change.Generation > 0 && change.Generation <= math.MaxInt64 &&
		change.TrustContract == auditExportRecipientProofingTrustContract &&
		auditExportDeliveryDigest.MatchString(change.TrustProfileSHA256) &&
		validAuditExportProofingAuthorityKeys(change.Operation, change.KeyIDs) &&
		validOperatorAuditText(change.ActorID, 256) && len(change.ReasonDigest) == sha256.Size &&
		operatorAuditExportCorrelation.MatchString(change.CorrelationID)
}

func (repository *Repository) ChangeAuditExportProofingAuthority(
	ctx context.Context,
	change AuditExportProofingAuthorityChange,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !change.Valid() {
		return ErrAuditExportProofingAuthorityInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	change.KeyIDs = append([]string(nil), change.KeyIDs...)
	change.ReasonDigest = append([]byte(nil), change.ReasonDigest...)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audit export proofing authority change: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockAuditExportRecipientProofRevocations(ctx, tx, change.IsolationDomainID); err != nil {
		return err
	}
	existing, exists, err := readAuditExportProofingAuthorityGeneration(
		ctx, tx, change.IsolationDomainID, change.Generation,
	)
	if err != nil {
		return err
	}
	if exists {
		if !sameAuditExportProofingAuthorityChange(existing, change) {
			return ErrAuditExportProofingAuthorityConflict
		}
		return tx.Commit(ctx)
	}
	var correlatedDomain, correlatedAuthority string
	var correlatedGeneration int64
	err = tx.QueryRow(ctx, `
		SELECT isolation_domain_id, authority_id, generation
		FROM audit_export_proofing_authority_events
		WHERE correlation_id = $1
	`, change.CorrelationID).Scan(&correlatedDomain, &correlatedAuthority, &correlatedGeneration)
	if err == nil {
		return ErrAuditExportProofingAuthorityConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("inspect audit export proofing authority correlation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_export_proofing_authority_events (
			authorization_contract, isolation_domain_id, authority_id, generation,
			operation, trust_contract, trust_profile_sha256, actor_id,
			reason_digest, correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, change.Contract, change.IsolationDomainID, change.AuthorityID, change.Generation,
		change.Operation, change.TrustContract, change.TrustProfileSHA256,
		change.ActorID, change.ReasonDigest, change.CorrelationID); err != nil {
		return mapAuditExportProofingAuthorityWriteError(err)
	}
	for _, keyID := range change.KeyIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit_export_proofing_authority_keys (
				isolation_domain_id, generation, key_id
			) VALUES ($1, $2, $3)
		`, change.IsolationDomainID, change.Generation, keyID); err != nil {
			return mapAuditExportProofingAuthorityWriteError(err)
		}
	}
	resourceID := identity.Derived("apa", fmt.Sprintf(
		"%s\n%s\n%d", change.IsolationDomainID, change.AuthorityID, change.Generation,
	))
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, $4, 'audit-export-proofing-authority', $5,
			'accepted', $6, jsonb_build_object(
				'reasonDigest', $7::text,
				'proofingAuthorityId', $8::text,
				'proofingAuthorityGeneration', $9::bigint,
				'proofingAuthorityTrustProfileSha256', $10::text
			), clock_timestamp()
		)
	`, identity.New("aud"), change.IsolationDomainID, change.ActorID,
		"audit-export-proofing-authority."+change.Operation, resourceID,
		change.CorrelationID, digestBytes(change.ReasonDigest), change.AuthorityID,
		change.Generation, change.TrustProfileSHA256); err != nil {
		return fmt.Errorf("audit export proofing authority change: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit export proofing authority change: %w", err)
	}
	return nil
}

func requireAuditExportProofingAuthority(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	isolationDomainID string,
	authorityID string,
	trustProfileSHA256 string,
	signingKeyID string,
) error {
	var authorized bool
	if err := querier.QueryRow(ctx, `
		SELECT audit_export_proofing_authority_is_active($1, $2, $3, $4)
	`, isolationDomainID, authorityID, trustProfileSHA256, signingKeyID).Scan(&authorized); err != nil {
		return fmt.Errorf("authorize audit export proofing authority: %w", err)
	}
	if !authorized {
		return ErrAuditExportProofingAuthorityUnauthorized
	}
	return nil
}

func readAuditExportProofingAuthorityGeneration(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	isolationDomainID string,
	generation int64,
) (AuditExportProofingAuthorityChange, bool, error) {
	var change AuditExportProofingAuthorityChange
	err := querier.QueryRow(ctx, `
		SELECT authorization_contract, isolation_domain_id, authority_id, generation,
		       operation, trust_contract, trust_profile_sha256, actor_id,
		       reason_digest, correlation_id
		FROM audit_export_proofing_authority_events
		WHERE isolation_domain_id = $1 AND generation = $2
	`, isolationDomainID, generation).Scan(
		&change.Contract, &change.IsolationDomainID, &change.AuthorityID,
		&change.Generation, &change.Operation, &change.TrustContract,
		&change.TrustProfileSHA256, &change.ActorID, &change.ReasonDigest,
		&change.CorrelationID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuditExportProofingAuthorityChange{}, false, nil
	}
	if err != nil {
		return AuditExportProofingAuthorityChange{}, false,
			fmt.Errorf("read audit export proofing authority generation: %w", err)
	}
	rows, err := querier.Query(ctx, `
		SELECT key_id
		FROM audit_export_proofing_authority_keys
		WHERE isolation_domain_id = $1 AND generation = $2
		ORDER BY key_id
	`, isolationDomainID, generation)
	if err != nil {
		return AuditExportProofingAuthorityChange{}, false,
			fmt.Errorf("read audit export proofing authority keys: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var keyID string
		if err := rows.Scan(&keyID); err != nil {
			return AuditExportProofingAuthorityChange{}, false,
				fmt.Errorf("scan audit export proofing authority key: %w", err)
		}
		change.KeyIDs = append(change.KeyIDs, keyID)
	}
	if err := rows.Err(); err != nil {
		return AuditExportProofingAuthorityChange{}, false,
			fmt.Errorf("iterate audit export proofing authority keys: %w", err)
	}
	if !change.Valid() {
		return AuditExportProofingAuthorityChange{}, false, ErrAuditExportProofingAuthorityConflict
	}
	return change, true, nil
}

func sameAuditExportProofingAuthorityChange(
	left AuditExportProofingAuthorityChange,
	right AuditExportProofingAuthorityChange,
) bool {
	return left.Contract == right.Contract && left.Operation == right.Operation &&
		left.IsolationDomainID == right.IsolationDomainID && left.AuthorityID == right.AuthorityID &&
		left.Generation == right.Generation && left.TrustContract == right.TrustContract &&
		left.TrustProfileSHA256 == right.TrustProfileSHA256 &&
		slices.Equal(left.KeyIDs, right.KeyIDs) && left.ActorID == right.ActorID &&
		bytes.Equal(left.ReasonDigest, right.ReasonDigest) && left.CorrelationID == right.CorrelationID
}

func validAuditExportProofingAuthorityKeys(operation string, keyIDs []string) bool {
	if operation == "revoke" {
		return len(keyIDs) == 0
	}
	if operation != "activate" || len(keyIDs) < 1 || len(keyIDs) > 8 ||
		!sort.StringsAreSorted(keyIDs) {
		return false
	}
	for index, keyID := range keyIDs {
		if !auditExportDeliveryKeyID.MatchString(keyID) ||
			(index > 0 && keyIDs[index-1] == keyID) {
			return false
		}
	}
	return true
}

func mapAuditExportProofingAuthorityWriteError(err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) &&
		(databaseError.Code == "23505" || databaseError.Code == "P0001") {
		return ErrAuditExportProofingAuthorityConflict
	}
	return fmt.Errorf("change audit export proofing authority: %w", err)
}
