package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const AuditExportRevocationAcquisitionContract = "dataground.audit-export-revocation-acquisition/v2"

const auditExportRevocationAcquisitionLegacyContract = "dataground.audit-export-revocation-acquisition/v1"

var ErrAuditExportRevocationAcquisitionConflict = errors.New("audit export revocation acquisition conflicts with durable state")

type AuditExportRevocationAcquisition struct {
	Contract             string
	Purpose              string
	SourceID             string
	SourceRegistrySHA256 string
	SourceGeneration     int64
}

type AuditExportRevocationAcquisitionReplay struct {
	Purpose              string
	IsolationDomainID    string
	SourceID             string
	SourceRegistrySHA256 string
	ActorID              string
	ReasonDigest         []byte
	CorrelationID        string
}

func (replay AuditExportRevocationAcquisitionReplay) valid() bool {
	return (replay.Purpose == AuditExportRevocationAuthorityPurposeRecipientProof ||
		replay.Purpose == AuditExportRevocationAuthorityPurposeWorkloadIdentity) &&
		operatorAuditDomainPattern.MatchString(replay.IsolationDomainID) &&
		auditExportDeliveryRecipient.MatchString(replay.SourceID) &&
		auditExportDeliveryDigest.MatchString(replay.SourceRegistrySHA256) &&
		validOperatorAuditText(replay.ActorID, 256) && len(replay.ReasonDigest) == sha256.Size &&
		operatorAuditExportCorrelation.MatchString(replay.CorrelationID)
}

// ReplayAuditExportRevocationAcquisition returns true only when the exact
// attributed acquisition already committed. It allows recovery from a lost
// database acknowledgement without depending on the remote source again.
func (repository *Repository) ReplayAuditExportRevocationAcquisition(
	ctx context.Context,
	replay AuditExportRevocationAcquisitionReplay,
) (bool, error) {
	if repository == nil || repository.pool == nil || ctx == nil || !replay.valid() {
		return false, ErrAuditExportRevocationAcquisitionConflict
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	replay.ReasonDigest = append([]byte(nil), replay.ReasonDigest...)
	var stored AuditExportRevocationAcquisitionReplay
	var revocationSHA256 string
	var contract string
	err := repository.pool.QueryRow(ctx, `
		SELECT contract, purpose, revocation_sha256, isolation_domain_id,
		       source_id, source_registry_sha256, correlation_id
		FROM audit_export_revocation_acquisitions
		WHERE correlation_id = $1
	`, replay.CorrelationID).Scan(
		&contract, &stored.Purpose, &revocationSHA256, &stored.IsolationDomainID,
		&stored.SourceID, &stored.SourceRegistrySHA256, &stored.CorrelationID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read audit export revocation acquisition replay: %w", err)
	}
	if (contract != AuditExportRevocationAcquisitionContract &&
		contract != auditExportRevocationAcquisitionLegacyContract) ||
		stored.Purpose != replay.Purpose || stored.IsolationDomainID != replay.IsolationDomainID ||
		stored.SourceID != replay.SourceID || stored.SourceRegistrySHA256 != replay.SourceRegistrySHA256 ||
		stored.CorrelationID != replay.CorrelationID {
		return false, ErrAuditExportRevocationAcquisitionConflict
	}
	query := `
		SELECT actor_id, reason_digest
		FROM audit_export_recipient_proof_revocations
		WHERE revocation_sha256 = $1 AND isolation_domain_id = $2 AND correlation_id = $3
	`
	if replay.Purpose == AuditExportRevocationAuthorityPurposeWorkloadIdentity {
		query = `
			SELECT actor_id, reason_digest
			FROM audit_export_workload_identity_revocations
			WHERE revocation_sha256 = $1 AND isolation_domain_id = $2 AND correlation_id = $3
		`
	}
	err = repository.pool.QueryRow(
		ctx, query, revocationSHA256, replay.IsolationDomainID, replay.CorrelationID,
	).Scan(&stored.ActorID, &stored.ReasonDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrAuditExportRevocationAcquisitionConflict
	}
	if err != nil {
		return false, fmt.Errorf("read audit export revocation acquisition attribution: %w", err)
	}
	if stored.ActorID != replay.ActorID || !bytes.Equal(stored.ReasonDigest, replay.ReasonDigest) {
		return false, ErrAuditExportRevocationAcquisitionConflict
	}
	return true, nil
}

func (acquisition AuditExportRevocationAcquisition) validFor(purpose string) bool {
	return acquisition.Purpose == purpose &&
		auditExportDeliveryRecipient.MatchString(acquisition.SourceID) &&
		auditExportDeliveryDigest.MatchString(acquisition.SourceRegistrySHA256) &&
		((acquisition.Contract == AuditExportRevocationAcquisitionContract &&
			acquisition.SourceGeneration > 0) ||
			(acquisition.Contract == auditExportRevocationAcquisitionLegacyContract &&
				acquisition.SourceGeneration == 0))
}

func insertAuditExportRevocationAcquisition(
	ctx context.Context,
	tx pgx.Tx,
	acquisition *AuditExportRevocationAcquisition,
	revocationSHA256 string,
	isolationDomainID string,
	trustProfileSHA256 string,
	correlationID string,
) error {
	if acquisition == nil {
		return nil
	}
	if _, err := requireAuditExportRevocationSource(
		ctx, tx, isolationDomainID, acquisition.Purpose, acquisition.SourceID,
		acquisition.SourceRegistrySHA256, acquisition.SourceGeneration,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_export_revocation_acquisitions (
			contract, purpose, revocation_sha256, isolation_domain_id,
			source_id, source_registry_sha256, source_generation,
			trust_profile_sha256, correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, acquisition.Contract, acquisition.Purpose, revocationSHA256, isolationDomainID,
		acquisition.SourceID, acquisition.SourceRegistrySHA256, acquisition.SourceGeneration,
		trustProfileSHA256, correlationID); err != nil {
		return fmt.Errorf("record audit export revocation acquisition: %w", err)
	}
	return nil
}

func readAuditExportRevocationAcquisition(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	purpose string,
	revocationSHA256 string,
) (*AuditExportRevocationAcquisition, error) {
	var acquisition AuditExportRevocationAcquisition
	err := querier.QueryRow(ctx, `
		SELECT contract, purpose, source_id, source_registry_sha256,
		       COALESCE(source_generation, 0)
		FROM audit_export_revocation_acquisitions
		WHERE purpose = $1 AND revocation_sha256 = $2
	`, purpose, revocationSHA256).Scan(
		&acquisition.Contract, &acquisition.Purpose,
		&acquisition.SourceID, &acquisition.SourceRegistrySHA256,
		&acquisition.SourceGeneration,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audit export revocation acquisition: %w", err)
	}
	if !acquisition.validFor(purpose) {
		return nil, errors.New("stored audit export revocation acquisition is invalid")
	}
	return &acquisition, nil
}

func sameAuditExportRevocationAcquisition(
	left *AuditExportRevocationAcquisition,
	right *AuditExportRevocationAcquisition,
) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Contract == right.Contract && left.Purpose == right.Purpose &&
		left.SourceID == right.SourceID && left.SourceRegistrySHA256 == right.SourceRegistrySHA256 &&
		left.SourceGeneration == right.SourceGeneration
}

func auditExportRevocationAcquisitionMetadata(
	acquisition *AuditExportRevocationAcquisition,
) (string, string, int64) {
	if acquisition == nil {
		return "", "", 0
	}
	return acquisition.SourceID, acquisition.SourceRegistrySHA256, acquisition.SourceGeneration
}

func cloneAuditExportRevocationAcquisition(
	acquisition *AuditExportRevocationAcquisition,
) *AuditExportRevocationAcquisition {
	if acquisition == nil {
		return nil
	}
	copy := *acquisition
	return &copy
}
