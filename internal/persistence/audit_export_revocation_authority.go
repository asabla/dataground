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

const (
	AuditExportRevocationAuthorityAuthorizationContract   = "dataground.audit-export-revocation-authority-authorization/v1"
	AuditExportRevocationAuthorityPurposeRecipientProof   = "recipient-proof"
	AuditExportRevocationAuthorityPurposeWorkloadIdentity = "workload-identity"

	auditExportRecipientRevocationTrustContract = "dataground.audit-export-recipient-revocation-trust/ed25519/v1"
	auditExportWorkloadRevocationTrustContract  = "dataground.audit-export-workload-identity-revocation-trust/ed25519/v1"
)

var (
	ErrAuditExportRevocationAuthorityInvalid      = errors.New("audit export revocation authority change is invalid")
	ErrAuditExportRevocationAuthorityConflict     = errors.New("audit export revocation authority change conflicts with durable state")
	ErrAuditExportRevocationAuthorityUnauthorized = errors.New("audit export revocation authority is not authorized")
)

type AuditExportRevocationAuthorityChange struct {
	Contract           string
	Operation          string
	IsolationDomainID  string
	Purpose            string
	AuthorityID        string
	Generation         int64
	TrustContract      string
	TrustProfileSHA256 string
	KeyIDs             []string
	ActorID            string
	ReasonDigest       []byte
	CorrelationID      string
}

func (change AuditExportRevocationAuthorityChange) Valid() bool {
	return change.Contract == AuditExportRevocationAuthorityAuthorizationContract &&
		(change.Operation == "activate" || change.Operation == "revoke") &&
		operatorAuditDomainPattern.MatchString(change.IsolationDomainID) &&
		validAuditExportRevocationAuthorityPurpose(change.Purpose, change.TrustContract) &&
		auditExportDeliveryRecipient.MatchString(change.AuthorityID) &&
		change.Generation > 0 && change.Generation <= math.MaxInt64 &&
		auditExportDeliveryDigest.MatchString(change.TrustProfileSHA256) &&
		validAuditExportRevocationAuthorityKeys(change.Operation, change.KeyIDs) &&
		validOperatorAuditText(change.ActorID, 256) && len(change.ReasonDigest) == sha256.Size &&
		operatorAuditExportCorrelation.MatchString(change.CorrelationID)
}

func (repository *Repository) ChangeAuditExportRevocationAuthority(
	ctx context.Context,
	change AuditExportRevocationAuthorityChange,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !change.Valid() {
		return ErrAuditExportRevocationAuthorityInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	change.KeyIDs = append([]string(nil), change.KeyIDs...)
	change.ReasonDigest = append([]byte(nil), change.ReasonDigest...)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audit export revocation authority change: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockAuditExportRevocationAuthorityPurpose(
		ctx, tx, change.Purpose, change.IsolationDomainID,
	); err != nil {
		return err
	}
	existing, exists, err := readAuditExportRevocationAuthorityGeneration(
		ctx, tx, change.IsolationDomainID, change.Purpose, change.Generation,
	)
	if err != nil {
		return err
	}
	if exists {
		if !sameAuditExportRevocationAuthorityChange(existing, change) {
			return ErrAuditExportRevocationAuthorityConflict
		}
		return tx.Commit(ctx)
	}
	var correlatedDomain, correlatedPurpose, correlatedAuthority string
	var correlatedGeneration int64
	err = tx.QueryRow(ctx, `
		SELECT isolation_domain_id, purpose, authority_id, generation
		FROM audit_export_revocation_authority_events
		WHERE correlation_id = $1
	`, change.CorrelationID).Scan(
		&correlatedDomain, &correlatedPurpose, &correlatedAuthority, &correlatedGeneration,
	)
	if err == nil {
		return ErrAuditExportRevocationAuthorityConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("inspect audit export revocation authority correlation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_export_revocation_authority_events (
			authorization_contract, isolation_domain_id, purpose, authority_id,
			generation, operation, trust_contract, trust_profile_sha256,
			actor_id, reason_digest, correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, change.Contract, change.IsolationDomainID, change.Purpose, change.AuthorityID,
		change.Generation, change.Operation, change.TrustContract, change.TrustProfileSHA256,
		change.ActorID, change.ReasonDigest, change.CorrelationID); err != nil {
		return mapAuditExportRevocationAuthorityWriteError(err)
	}
	for _, keyID := range change.KeyIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit_export_revocation_authority_keys (
				isolation_domain_id, purpose, generation, key_id
			) VALUES ($1, $2, $3, $4)
		`, change.IsolationDomainID, change.Purpose, change.Generation, keyID); err != nil {
			return mapAuditExportRevocationAuthorityWriteError(err)
		}
	}
	resourceID := identity.Derived("ara", fmt.Sprintf(
		"%s\n%s\n%s\n%d", change.IsolationDomainID, change.Purpose,
		change.AuthorityID, change.Generation,
	))
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, $4, 'audit-export-revocation-authority', $5,
			'accepted', $6, jsonb_build_object(
				'reasonDigest', $7::text,
				'revocationAuthorityPurpose', $8::text,
				'revocationAuthorityId', $9::text,
				'revocationAuthorityGeneration', $10::bigint,
				'revocationAuthorityTrustProfileSha256', $11::text
			), clock_timestamp()
		)
	`, identity.New("aud"), change.IsolationDomainID, change.ActorID,
		"audit-export-revocation-authority."+change.Operation, resourceID,
		change.CorrelationID, digestBytes(change.ReasonDigest), change.Purpose,
		change.AuthorityID, change.Generation, change.TrustProfileSHA256); err != nil {
		return fmt.Errorf("audit export revocation authority change: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit export revocation authority change: %w", err)
	}
	return nil
}

func requireAuditExportRevocationAuthority(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	isolationDomainID string,
	purpose string,
	authorityID string,
	trustProfileSHA256 string,
	signingKeyID string,
) error {
	var authorized bool
	if err := querier.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM audit_export_revocation_authority_events AS authority_event
			JOIN audit_export_revocation_authority_keys AS authority_key
			  ON authority_key.isolation_domain_id = authority_event.isolation_domain_id
			 AND authority_key.purpose = authority_event.purpose
			 AND authority_key.generation = authority_event.generation
			WHERE authority_event.isolation_domain_id = $1
			  AND authority_event.purpose = $2
			  AND authority_event.authority_id = $3
			  AND authority_event.operation = 'activate'
			  AND authority_event.trust_profile_sha256 = $4
			  AND authority_key.key_id = $5
			  AND authority_event.generation = (
				SELECT max(latest.generation)
				FROM audit_export_revocation_authority_events AS latest
				WHERE latest.isolation_domain_id = $1
				  AND latest.purpose = $2
			  )
		)
	`, isolationDomainID, purpose, authorityID, trustProfileSHA256,
		signingKeyID).Scan(&authorized); err != nil {
		return fmt.Errorf("authorize audit export revocation authority: %w", err)
	}
	if !authorized {
		return ErrAuditExportRevocationAuthorityUnauthorized
	}
	return nil
}

func readAuditExportRevocationAuthorityGeneration(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	isolationDomainID string,
	purpose string,
	generation int64,
) (AuditExportRevocationAuthorityChange, bool, error) {
	var change AuditExportRevocationAuthorityChange
	err := querier.QueryRow(ctx, `
		SELECT authorization_contract, isolation_domain_id, purpose, authority_id,
		       generation, operation, trust_contract, trust_profile_sha256,
		       actor_id, reason_digest, correlation_id
		FROM audit_export_revocation_authority_events
		WHERE isolation_domain_id = $1 AND purpose = $2
		  AND generation = $3
	`, isolationDomainID, purpose, generation).Scan(
		&change.Contract, &change.IsolationDomainID, &change.Purpose, &change.AuthorityID,
		&change.Generation, &change.Operation, &change.TrustContract,
		&change.TrustProfileSHA256, &change.ActorID, &change.ReasonDigest,
		&change.CorrelationID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuditExportRevocationAuthorityChange{}, false, nil
	}
	if err != nil {
		return AuditExportRevocationAuthorityChange{}, false,
			fmt.Errorf("read audit export revocation authority generation: %w", err)
	}
	rows, err := querier.Query(ctx, `
		SELECT key_id
		FROM audit_export_revocation_authority_keys
		WHERE isolation_domain_id = $1 AND purpose = $2
		  AND generation = $3
		ORDER BY key_id
	`, isolationDomainID, purpose, generation)
	if err != nil {
		return AuditExportRevocationAuthorityChange{}, false,
			fmt.Errorf("read audit export revocation authority keys: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var keyID string
		if err := rows.Scan(&keyID); err != nil {
			return AuditExportRevocationAuthorityChange{}, false,
				fmt.Errorf("scan audit export revocation authority key: %w", err)
		}
		change.KeyIDs = append(change.KeyIDs, keyID)
	}
	if err := rows.Err(); err != nil {
		return AuditExportRevocationAuthorityChange{}, false,
			fmt.Errorf("iterate audit export revocation authority keys: %w", err)
	}
	if !change.Valid() {
		return AuditExportRevocationAuthorityChange{}, false,
			ErrAuditExportRevocationAuthorityConflict
	}
	return change, true, nil
}

func lockAuditExportRevocationAuthorityPurpose(
	ctx context.Context,
	tx pgx.Tx,
	purpose string,
	isolationDomainID string,
) error {
	switch purpose {
	case AuditExportRevocationAuthorityPurposeRecipientProof:
		return lockAuditExportRecipientProofRevocations(ctx, tx, isolationDomainID)
	case AuditExportRevocationAuthorityPurposeWorkloadIdentity:
		return lockAuditExportWorkloadIdentityRevocations(ctx, tx, isolationDomainID)
	default:
		return ErrAuditExportRevocationAuthorityInvalid
	}
}

func sameAuditExportRevocationAuthorityChange(
	left AuditExportRevocationAuthorityChange,
	right AuditExportRevocationAuthorityChange,
) bool {
	return left.Contract == right.Contract && left.Operation == right.Operation &&
		left.IsolationDomainID == right.IsolationDomainID && left.Purpose == right.Purpose &&
		left.AuthorityID == right.AuthorityID && left.Generation == right.Generation &&
		left.TrustContract == right.TrustContract &&
		left.TrustProfileSHA256 == right.TrustProfileSHA256 &&
		slices.Equal(left.KeyIDs, right.KeyIDs) && left.ActorID == right.ActorID &&
		bytes.Equal(left.ReasonDigest, right.ReasonDigest) &&
		left.CorrelationID == right.CorrelationID
}

func validAuditExportRevocationAuthorityPurpose(purpose string, trustContract string) bool {
	return (purpose == AuditExportRevocationAuthorityPurposeRecipientProof &&
		trustContract == auditExportRecipientRevocationTrustContract) ||
		(purpose == AuditExportRevocationAuthorityPurposeWorkloadIdentity &&
			trustContract == auditExportWorkloadRevocationTrustContract)
}

func validAuditExportRevocationAuthorityKeys(operation string, keyIDs []string) bool {
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

func mapAuditExportRevocationAuthorityWriteError(err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) &&
		(databaseError.Code == "23505" || databaseError.Code == "P0001") {
		return ErrAuditExportRevocationAuthorityConflict
	}
	return fmt.Errorf("change audit export revocation authority: %w", err)
}
