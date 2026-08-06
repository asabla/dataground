package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	AuditExportDeliveryContract                   = "dataground.audit-export-delivery/v3"
	AuditExportEncryptedDeliveryContract          = "dataground.audit-export-delivery/v4"
	AuditExportTransportedDeliveryContract        = "dataground.audit-export-delivery/v5"
	AuditExportWorkloadDeliveryContract           = "dataground.audit-export-delivery/v6"
	AuditExportDeliveryTransportContract          = "dataground.audit-export-transport/s3-immutable/v1"
	AuditExportDeliveryMTLSTransportContract      = "dataground.audit-export-transport/s3-immutable-mtls/v2"
	AuditExportDeliveryWorkloadTransportContract  = "dataground.audit-export-transport/s3-immutable-mtls-workload/v3"
	auditExportDeliveryLegacyContract             = "dataground.audit-export-delivery/v1"
	AuditExportDeliveryReceiptVerifiedContract    = "dataground.audit-export-delivery/v2"
	auditExportDeliveryReceiptContract            = "dataground.audit-export-delivery-receipt/ed25519/v2"
	auditExportEncryptedDeliveryReceiptContract   = "dataground.audit-export-delivery-receipt/ed25519/v3"
	auditExportTransportedDeliveryReceiptContract = "dataground.audit-export-delivery-receipt/ed25519/v4"
	auditExportWorkloadDeliveryReceiptContract    = "dataground.audit-export-delivery-receipt/ed25519/v5"
	auditExportDeliveryLegacyReceiptContract      = "dataground.audit-export-delivery-receipt/ed25519/v1"
)

var (
	ErrAuditExportDeliveryInvalid  = errors.New("audit export delivery is invalid")
	ErrAuditExportDeliveryConflict = errors.New("audit export delivery conflicts with durable state")
	auditExportDeliveryIDPattern   = regexp.MustCompile(`^adl_[0-9a-z]{20,32}$`)
	auditExportDeliveryDigest      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	auditExportDeliveryKeyID       = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,63}$`)
	auditExportDeliveryRecipient   = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)
)

// AuditExportDelivery binds one externally executed delivery to the exact
// signed export envelope and opaque destination selected by an operator.
type AuditExportDelivery struct {
	Contract                    string
	DeliveryID                  string
	IsolationDomainID           string
	ExportKind                  string
	ExportID                    string
	EnvelopeDigest              []byte
	ExportSHA256                string
	TrustProfileSHA256          string
	SigningKeyID                string
	RecipientID                 string
	DestinationDigest           []byte
	EncryptedPackageDigest      []byte
	RecipientTrustProfileSHA256 string
	RecipientEncryptionKeyID    string
	RecipientTrustGeneration    int64
}

type AuditExportDeliveryAttribution struct {
	ActorID       string
	ReasonDigest  []byte
	CorrelationID string
}

type AuditExportDeliveryAcknowledgement struct {
	AcknowledgementDigest       []byte
	DeliveryContract            string
	ReceiptContract             string
	RecipientTrustProfileSHA256 string
	RecipientSigningKeyID       string
	AcceptedAt                  time.Time
	RecipientTrustGeneration    int64
	Attribution                 AuditExportDeliveryAttribution
}

func (delivery AuditExportDelivery) Valid() bool {
	if (delivery.Contract != AuditExportDeliveryContract &&
		!encryptedAuditExportDeliveryContract(delivery.Contract)) ||
		!auditExportDeliveryIDPattern.MatchString(delivery.DeliveryID) ||
		!operatorAuditDomainPattern.MatchString(delivery.IsolationDomainID) ||
		len(delivery.EnvelopeDigest) != sha256.Size ||
		!auditExportDeliveryDigest.MatchString(delivery.ExportSHA256) ||
		!auditExportDeliveryDigest.MatchString(delivery.TrustProfileSHA256) ||
		!auditExportDeliveryKeyID.MatchString(delivery.SigningKeyID) ||
		!auditExportDeliveryRecipient.MatchString(delivery.RecipientID) ||
		len(delivery.DestinationDigest) != sha256.Size {
		return false
	}
	if encryptedAuditExportDeliveryContract(delivery.Contract) {
		if len(delivery.EncryptedPackageDigest) != sha256.Size ||
			!auditExportDeliveryDigest.MatchString(delivery.RecipientTrustProfileSHA256) ||
			!auditExportDeliveryKeyID.MatchString(delivery.RecipientEncryptionKeyID) ||
			delivery.RecipientTrustGeneration < 0 {
			return false
		}
	} else if len(delivery.EncryptedPackageDigest) != 0 ||
		delivery.RecipientTrustProfileSHA256 != "" || delivery.RecipientEncryptionKeyID != "" ||
		delivery.RecipientTrustGeneration != 0 {
		return false
	}
	switch delivery.ExportKind {
	case "authorization":
		return authorizationExportIDPattern.MatchString(delivery.ExportID)
	case "operator":
		return operatorAuditExportIDPattern.MatchString(delivery.ExportID)
	default:
		return false
	}
}

func (attribution AuditExportDeliveryAttribution) Valid() bool {
	return validOperatorAuditText(attribution.ActorID, 256) &&
		len(attribution.ReasonDigest) == sha256.Size &&
		operatorAuditExportCorrelation.MatchString(attribution.CorrelationID)
}

func (acknowledgement AuditExportDeliveryAcknowledgement) Valid() bool {
	_, offset := acknowledgement.AcceptedAt.Zone()
	return len(acknowledgement.AcknowledgementDigest) == sha256.Size &&
		(acknowledgement.DeliveryContract == AuditExportDeliveryContract ||
			acknowledgement.DeliveryContract == AuditExportEncryptedDeliveryContract ||
			acknowledgement.DeliveryContract == AuditExportTransportedDeliveryContract ||
			acknowledgement.DeliveryContract == AuditExportWorkloadDeliveryContract ||
			acknowledgement.DeliveryContract == AuditExportDeliveryReceiptVerifiedContract) &&
		((acknowledgement.DeliveryContract == AuditExportDeliveryContract &&
			acknowledgement.ReceiptContract == auditExportDeliveryReceiptContract &&
			acknowledgement.RecipientTrustGeneration == 0) ||
			(acknowledgement.DeliveryContract == AuditExportEncryptedDeliveryContract &&
				acknowledgement.ReceiptContract == auditExportEncryptedDeliveryReceiptContract &&
				acknowledgement.RecipientTrustGeneration > 0) ||
			(acknowledgement.DeliveryContract == AuditExportTransportedDeliveryContract &&
				acknowledgement.ReceiptContract == auditExportTransportedDeliveryReceiptContract &&
				acknowledgement.RecipientTrustGeneration > 0) ||
			(acknowledgement.DeliveryContract == AuditExportWorkloadDeliveryContract &&
				acknowledgement.ReceiptContract == auditExportWorkloadDeliveryReceiptContract &&
				acknowledgement.RecipientTrustGeneration > 0) ||
			(acknowledgement.DeliveryContract == AuditExportDeliveryReceiptVerifiedContract &&
				acknowledgement.ReceiptContract == auditExportDeliveryLegacyReceiptContract &&
				acknowledgement.RecipientTrustGeneration == 0)) &&
		auditExportDeliveryDigest.MatchString(acknowledgement.RecipientTrustProfileSHA256) &&
		auditExportDeliveryKeyID.MatchString(acknowledgement.RecipientSigningKeyID) &&
		!acknowledgement.AcceptedAt.IsZero() && offset == 0 &&
		credentialTimestampExact(acknowledgement.AcceptedAt) &&
		acknowledgement.Attribution.Valid()
}

func (repository *Repository) PrepareAuditExportDelivery(
	ctx context.Context,
	delivery AuditExportDelivery,
	attribution AuditExportDeliveryAttribution,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !delivery.Valid() || !attribution.Valid() {
		return ErrAuditExportDeliveryInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	delivery = cloneAuditExportDelivery(delivery)
	attribution = cloneAuditExportDeliveryAttribution(attribution)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audit export delivery preparation: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockAuditExportDelivery(ctx, tx, delivery.DeliveryID); err != nil {
		return err
	}
	existing, existingAttribution, status, exists, err := readAuditExportDelivery(ctx, tx, delivery.DeliveryID)
	if err != nil {
		return err
	}
	if exists {
		if !sameAuditExportDeliveryPreparation(existing, delivery) ||
			!sameAuditExportDeliveryAttribution(existingAttribution, attribution) ||
			(status != "prepared" && status != "acknowledged") {
			return ErrAuditExportDeliveryConflict
		}
		if status == "acknowledged" {
			if _, err := readAuditExportDeliveryAcknowledgement(ctx, tx, delivery.DeliveryID); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}
	if encryptedAuditExportDeliveryContract(delivery.Contract) {
		recipientTrustGeneration, err := authorizeAuditExportRecipientEncryption(ctx, tx, delivery)
		if err != nil {
			return err
		}
		delivery.RecipientTrustGeneration = recipientTrustGeneration
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_export_deliveries (
			delivery_id, isolation_domain_id, contract, export_kind, export_id,
			envelope_digest, export_sha256, trust_profile_sha256, signing_key_id,
			recipient_id, destination_digest, encrypted_package_digest,
			recipient_trust_profile_sha256, recipient_encryption_key_id, recipient_trust_generation
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`, delivery.DeliveryID, delivery.IsolationDomainID, delivery.Contract, delivery.ExportKind,
		delivery.ExportID, delivery.EnvelopeDigest, delivery.ExportSHA256, delivery.TrustProfileSHA256,
		delivery.SigningKeyID, delivery.RecipientID, delivery.DestinationDigest,
		nullAuditExportDeliveryBytes(delivery.EncryptedPackageDigest),
		nullAuditExportDeliveryText(delivery.RecipientTrustProfileSHA256),
		nullAuditExportDeliveryText(delivery.RecipientEncryptionKeyID),
		nullAuditExportDeliveryGeneration(delivery.RecipientTrustGeneration)); err != nil {
		return mapAuditExportDeliveryWriteError("prepare audit export delivery", err)
	}
	preparationEvidence := delivery.EnvelopeDigest
	if encryptedAuditExportDeliveryContract(delivery.Contract) {
		preparationEvidence = delivery.EncryptedPackageDigest
	}
	if err := insertAuditExportDeliveryOperation(ctx, tx, delivery, "prepare", attribution, preparationEvidence); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, 'audit-export-delivery.prepare', 'audit-export-delivery', $4,
			'accepted', $5,
			jsonb_strip_nulls(jsonb_build_object(
				'destinationDigest', $6::text,
				'envelopeDigest', $7::text,
				'exportKind', $8::text,
				'reasonDigest', $9::text,
				'recipientId', $10::text,
				'signingKeyId', $11::text,
				'trustProfileSha256', $12::text,
				'encryptedPackageDigest', NULLIF($13::text, ''),
				'recipientEncryptionKeyId', NULLIF($14::text, ''),
				'recipientTrustGeneration', NULLIF($15::bigint, 0),
				'recipientTrustProfileSha256', NULLIF($16::text, '')
			)),
			clock_timestamp()
		)
	`, identity.New("aud"), delivery.IsolationDomainID, attribution.ActorID, delivery.DeliveryID,
		attribution.CorrelationID, digestBytes(delivery.DestinationDigest), digestBytes(delivery.EnvelopeDigest),
		delivery.ExportKind, digestBytes(attribution.ReasonDigest), delivery.RecipientID,
		delivery.SigningKeyID, delivery.TrustProfileSHA256,
		digestOptionalBytes(delivery.EncryptedPackageDigest), delivery.RecipientEncryptionKeyID,
		delivery.RecipientTrustGeneration, delivery.RecipientTrustProfileSHA256); err != nil {
		return fmt.Errorf("audit export delivery preparation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit export delivery preparation: %w", err)
	}
	return nil
}

func (repository *Repository) ReserveAuditExportDeliveryTransport(
	ctx context.Context,
	delivery AuditExportDelivery,
	transportContract string,
	attribution AuditExportDeliveryAttribution,
) error {
	return repository.ReserveAuditExportDeliveryTransportWithWorkloadIdentity(
		ctx, delivery, transportContract, AuditExportWorkloadIdentityAuthorization{}, attribution,
	)
}

func (repository *Repository) ReserveAuditExportDeliveryTransportWithWorkloadIdentity(
	ctx context.Context,
	delivery AuditExportDelivery,
	transportContract string,
	workloadIdentity AuditExportWorkloadIdentityAuthorization,
	attribution AuditExportDeliveryAttribution,
) error {
	if repository == nil || repository.pool == nil || ctx == nil ||
		(delivery.Contract != AuditExportTransportedDeliveryContract &&
			delivery.Contract != AuditExportWorkloadDeliveryContract) ||
		!delivery.Valid() || !validAuditExportDeliveryTransportContract(transportContract) ||
		!validAuditExportDeliveryTransportAuthorization(delivery, transportContract, workloadIdentity) ||
		!attribution.Valid() {
		return ErrAuditExportDeliveryInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	delivery = cloneAuditExportDelivery(delivery)
	attribution = cloneAuditExportDeliveryAttribution(attribution)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audit export delivery transport reservation: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockAuditExportDelivery(ctx, tx, delivery.DeliveryID); err != nil {
		return err
	}
	existing, _, status, exists, err := readAuditExportDelivery(ctx, tx, delivery.DeliveryID)
	if err != nil {
		return err
	}
	if !exists || status != "prepared" || !sameAuditExportDeliveryPreparation(existing, delivery) {
		return ErrAuditExportDeliveryConflict
	}
	transportAttribution, storedTransportContract, storedWorkloadIdentity, state, found, err := readAuditExportDeliveryTransport(
		ctx, tx, delivery.DeliveryID,
	)
	if err != nil {
		return err
	}
	if found {
		if storedTransportContract != transportContract ||
			(state != "reserved" && state != "completed") ||
			!sameAuditExportWorkloadIdentityAuthorization(storedWorkloadIdentity, workloadIdentity) ||
			!sameAuditExportDeliveryAttribution(transportAttribution, attribution) {
			return ErrAuditExportDeliveryConflict
		}
		if state == "reserved" {
			generation, err := authorizeAuditExportRecipientEncryption(ctx, tx, existing)
			if err != nil {
				return err
			}
			if generation != existing.RecipientTrustGeneration {
				return ErrAuditExportDeliveryConflict
			}
			if existing.Contract == AuditExportWorkloadDeliveryContract {
				if err := authorizeAuditExportWorkloadIdentity(
					ctx, tx, existing.IsolationDomainID, workloadIdentity,
				); err != nil {
					return err
				}
			}
		}
		return tx.Commit(ctx)
	}
	generation, err := authorizeAuditExportRecipientEncryption(ctx, tx, existing)
	if err != nil {
		return err
	}
	if generation != existing.RecipientTrustGeneration {
		return ErrAuditExportDeliveryConflict
	}
	if delivery.Contract == AuditExportWorkloadDeliveryContract {
		if err := authorizeAuditExportWorkloadIdentity(
			ctx, tx, delivery.IsolationDomainID, workloadIdentity,
		); err != nil {
			return err
		}
	}
	if err := insertAuditExportDeliveryOperation(
		ctx, tx, existing, "transport", attribution, existing.EncryptedPackageDigest,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_export_delivery_transports (
			delivery_id, isolation_domain_id, transport_contract,
			destination_digest, encrypted_package_digest, workload_id,
			workload_identity_grant_sha256, workload_identity_generation,
			client_certificate_sha256
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, existing.DeliveryID, existing.IsolationDomainID, transportContract,
		existing.DestinationDigest, existing.EncryptedPackageDigest,
		nullAuditExportRecipientTrustText(workloadIdentity.WorkloadID),
		nullAuditExportRecipientTrustText(workloadIdentity.GrantSHA256),
		nullAuditExportDeliveryGeneration(workloadIdentity.Generation),
		nullAuditExportRecipientTrustText(workloadIdentity.ClientCertificateSHA256)); err != nil {
		return mapAuditExportDeliveryWriteError("reserve audit export delivery transport", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, 'audit-export-delivery.transport', 'audit-export-delivery', $4,
			'accepted', $5,
			jsonb_build_object(
				'destinationDigest', $6::text,
				'encryptedPackageDigest', $7::text,
				'reasonDigest', $8::text,
				'transportContract', $9::text,
				'workloadId', NULLIF($10::text, ''),
				'workloadIdentityGeneration', NULLIF($11::bigint, 0),
				'workloadIdentityGrantSha256', NULLIF($12::text, '')
			),
			clock_timestamp()
		)
	`, identity.New("aud"), existing.IsolationDomainID, attribution.ActorID,
		existing.DeliveryID, attribution.CorrelationID, digestBytes(existing.DestinationDigest),
		digestBytes(existing.EncryptedPackageDigest), digestBytes(attribution.ReasonDigest),
		transportContract, workloadIdentity.WorkloadID, workloadIdentity.Generation,
		workloadIdentity.GrantSHA256); err != nil {
		return fmt.Errorf("audit export delivery transport reservation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit export delivery transport reservation: %w", err)
	}
	return nil
}

func (repository *Repository) CompleteAuditExportDeliveryTransport(
	ctx context.Context,
	delivery AuditExportDelivery,
	transportContract string,
	attribution AuditExportDeliveryAttribution,
) error {
	return repository.CompleteAuditExportDeliveryTransportWithWorkloadIdentity(
		ctx, delivery, transportContract, AuditExportWorkloadIdentityAuthorization{}, attribution,
	)
}

func (repository *Repository) CompleteAuditExportDeliveryTransportWithWorkloadIdentity(
	ctx context.Context,
	delivery AuditExportDelivery,
	transportContract string,
	workloadIdentity AuditExportWorkloadIdentityAuthorization,
	attribution AuditExportDeliveryAttribution,
) error {
	if repository == nil || repository.pool == nil || ctx == nil ||
		(delivery.Contract != AuditExportTransportedDeliveryContract &&
			delivery.Contract != AuditExportWorkloadDeliveryContract) ||
		!delivery.Valid() || !validAuditExportDeliveryTransportContract(transportContract) ||
		!validAuditExportDeliveryTransportAuthorization(delivery, transportContract, workloadIdentity) ||
		!attribution.Valid() {
		return ErrAuditExportDeliveryInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	delivery = cloneAuditExportDelivery(delivery)
	attribution = cloneAuditExportDeliveryAttribution(attribution)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audit export delivery transport completion: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockAuditExportDelivery(ctx, tx, delivery.DeliveryID); err != nil {
		return err
	}
	existing, _, status, exists, err := readAuditExportDelivery(ctx, tx, delivery.DeliveryID)
	if err != nil {
		return err
	}
	if !exists || status != "prepared" || !sameAuditExportDeliveryPreparation(existing, delivery) {
		return ErrAuditExportDeliveryConflict
	}
	transportAttribution, storedTransportContract, storedWorkloadIdentity, state, found, err := readAuditExportDeliveryTransport(
		ctx, tx, delivery.DeliveryID,
	)
	if err != nil {
		return err
	}
	if !found || storedTransportContract != transportContract ||
		!sameAuditExportWorkloadIdentityAuthorization(storedWorkloadIdentity, workloadIdentity) ||
		!sameAuditExportDeliveryAttribution(transportAttribution, attribution) {
		return ErrAuditExportDeliveryConflict
	}
	if state == "completed" {
		return tx.Commit(ctx)
	}
	if state != "reserved" {
		return ErrAuditExportDeliveryConflict
	}
	result, err := tx.Exec(ctx, `
		UPDATE audit_export_delivery_transports
		SET state = 'completed', completed_at = clock_timestamp()
		WHERE delivery_id = $1 AND state = 'reserved'
	`, existing.DeliveryID)
	if err != nil {
		return fmt.Errorf("complete audit export delivery transport: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrAuditExportDeliveryConflict
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, 'audit-export-delivery.transport-complete', 'audit-export-delivery', $4,
			'succeeded', $5,
			jsonb_build_object(
				'destinationDigest', $6::text,
				'encryptedPackageDigest', $7::text,
				'reasonDigest', $8::text,
				'transportContract', $9::text,
				'workloadId', NULLIF($10::text, ''),
				'workloadIdentityGeneration', NULLIF($11::bigint, 0),
				'workloadIdentityGrantSha256', NULLIF($12::text, '')
			),
			clock_timestamp()
		)
	`, identity.New("aud"), existing.IsolationDomainID, attribution.ActorID,
		existing.DeliveryID, attribution.CorrelationID, digestBytes(existing.DestinationDigest),
		digestBytes(existing.EncryptedPackageDigest), digestBytes(attribution.ReasonDigest),
		transportContract, workloadIdentity.WorkloadID, workloadIdentity.Generation,
		workloadIdentity.GrantSHA256); err != nil {
		return fmt.Errorf("audit export delivery transport completion: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit export delivery transport completion: %w", err)
	}
	return nil
}

func (repository *Repository) AcknowledgeAuditExportDelivery(
	ctx context.Context,
	delivery AuditExportDelivery,
	acknowledgement AuditExportDeliveryAcknowledgement,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !delivery.Valid() || !acknowledgement.Valid() {
		return ErrAuditExportDeliveryInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	delivery = cloneAuditExportDelivery(delivery)
	acknowledgement = cloneAuditExportDeliveryAcknowledgement(acknowledgement)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audit export delivery acknowledgement: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockAuditExportDelivery(ctx, tx, delivery.DeliveryID); err != nil {
		return err
	}
	existing, _, status, exists, err := readAuditExportDelivery(ctx, tx, delivery.DeliveryID)
	if err != nil {
		return err
	}
	if !exists || !sameAuditExportDeliveryPreparation(existing, delivery) {
		return ErrAuditExportDeliveryConflict
	}
	if encryptedAuditExportDeliveryContract(existing.Contract) &&
		acknowledgement.RecipientTrustGeneration != existing.RecipientTrustGeneration {
		return ErrAuditExportDeliveryConflict
	}
	if status == "acknowledged" {
		existingAcknowledgement, err := readAuditExportDeliveryAcknowledgement(ctx, tx, delivery.DeliveryID)
		if err != nil {
			return err
		}
		if !sameAuditExportDeliveryAcknowledgement(existingAcknowledgement, acknowledgement) {
			return ErrAuditExportDeliveryConflict
		}
		return tx.Commit(ctx)
	}
	if status != "prepared" {
		return ErrAuditExportDeliveryConflict
	}
	if acknowledgement.DeliveryContract != delivery.Contract {
		return ErrAuditExportDeliveryConflict
	}
	if existing.Contract == AuditExportTransportedDeliveryContract ||
		existing.Contract == AuditExportWorkloadDeliveryContract {
		_, transportContract, workloadIdentity, transportState, found, err := readAuditExportDeliveryTransport(
			ctx, tx, delivery.DeliveryID,
		)
		if err != nil {
			return err
		}
		if !found || !validAuditExportDeliveryTransportContract(transportContract) ||
			!validAuditExportDeliveryTransportAuthorization(existing, transportContract, workloadIdentity) ||
			transportState != "completed" {
			return ErrAuditExportDeliveryConflict
		}
	}
	recipientTrustGeneration, err := authorizeAuditExportRecipientTrust(ctx, tx, existing, acknowledgement)
	if err != nil {
		return err
	}
	if err := insertAuditExportDeliveryOperation(
		ctx, tx, delivery, "acknowledge", acknowledgement.Attribution, acknowledgement.AcknowledgementDigest,
	); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `
		UPDATE audit_export_deliveries
		SET status = 'acknowledged', acknowledgement_digest = $2,
		    acknowledgement_contract = $3, recipient_trust_profile_sha256 = $4,
		    recipient_signing_key_id = $5, recipient_accepted_at = $6,
		    recipient_trust_generation = $7, acknowledged_at = clock_timestamp()
		WHERE delivery_id = $1 AND status = 'prepared'
	`, delivery.DeliveryID, acknowledgement.AcknowledgementDigest, acknowledgement.ReceiptContract,
		acknowledgement.RecipientTrustProfileSHA256, acknowledgement.RecipientSigningKeyID,
		acknowledgement.AcceptedAt.UTC(), recipientTrustGeneration)
	if err != nil {
		return fmt.Errorf("acknowledge audit export delivery: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrAuditExportDeliveryConflict
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, 'audit-export-delivery.acknowledge', 'audit-export-delivery', $4,
			'succeeded', $5,
			jsonb_build_object(
				'acknowledgementDigest', $6::text,
				'envelopeDigest', $7::text,
				'reasonDigest', $8::text,
				'recipientSigningKeyId', $9::text,
				'recipientTrustGeneration', $10::bigint,
				'recipientTrustProfileSha256', $11::text
			),
			clock_timestamp()
		)
	`, identity.New("aud"), delivery.IsolationDomainID, acknowledgement.Attribution.ActorID,
		delivery.DeliveryID, acknowledgement.Attribution.CorrelationID,
		digestBytes(acknowledgement.AcknowledgementDigest), digestBytes(delivery.EnvelopeDigest),
		digestBytes(acknowledgement.Attribution.ReasonDigest), acknowledgement.RecipientSigningKeyID,
		recipientTrustGeneration, acknowledgement.RecipientTrustProfileSHA256); err != nil {
		return fmt.Errorf("audit export delivery acknowledgement: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit export delivery acknowledgement: %w", err)
	}
	return nil
}

func validAuditExportDeliveryTransportContract(contract string) bool {
	return contract == AuditExportDeliveryTransportContract ||
		contract == AuditExportDeliveryMTLSTransportContract ||
		contract == AuditExportDeliveryWorkloadTransportContract
}

func validAuditExportDeliveryTransportAuthorization(
	delivery AuditExportDelivery,
	transportContract string,
	authorization AuditExportWorkloadIdentityAuthorization,
) bool {
	if delivery.Contract == AuditExportWorkloadDeliveryContract {
		return transportContract == AuditExportDeliveryWorkloadTransportContract && authorization.Valid()
	}
	return delivery.Contract == AuditExportTransportedDeliveryContract &&
		transportContract != AuditExportDeliveryWorkloadTransportContract &&
		authorization == (AuditExportWorkloadIdentityAuthorization{})
}

func lockAuditExportDelivery(ctx context.Context, tx pgx.Tx, deliveryID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, deliveryID); err != nil {
		return fmt.Errorf("lock audit export delivery: %w", err)
	}
	return nil
}

func insertAuditExportDeliveryOperation(
	ctx context.Context,
	tx pgx.Tx,
	delivery AuditExportDelivery,
	operation string,
	attribution AuditExportDeliveryAttribution,
	evidenceDigest []byte,
) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_export_delivery_operations (
			delivery_id, operation, isolation_domain_id, actor_id, correlation_id,
			reason_digest, evidence_digest
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, delivery.DeliveryID, operation, delivery.IsolationDomainID, attribution.ActorID,
		attribution.CorrelationID, attribution.ReasonDigest, evidenceDigest); err != nil {
		return mapAuditExportDeliveryWriteError("record audit export delivery operation", err)
	}
	return nil
}

func readAuditExportDeliveryTransport(
	ctx context.Context,
	tx pgx.Tx,
	deliveryID string,
) (
	AuditExportDeliveryAttribution,
	string,
	AuditExportWorkloadIdentityAuthorization,
	string,
	bool,
	error,
) {

	var attribution AuditExportDeliveryAttribution
	var workloadIdentity AuditExportWorkloadIdentityAuthorization
	var transportContract, state string
	var operationEvidence, deliveryEvidence, transportEvidence []byte
	var deliveryDestination, transportDestination []byte
	err := tx.QueryRow(ctx, `
		SELECT operation.actor_id, operation.reason_digest, operation.correlation_id,
		       operation.evidence_digest, delivery.encrypted_package_digest,
		       transport.encrypted_package_digest,
		       delivery.destination_digest, transport.destination_digest,
		       transport.transport_contract,
		       COALESCE(transport.workload_id, ''),
		       COALESCE(transport.workload_identity_grant_sha256, ''),
		       COALESCE(transport.workload_identity_generation, 0),
		       COALESCE(transport.client_certificate_sha256, ''),
		       transport.state
		FROM audit_export_delivery_transports AS transport
		JOIN audit_export_deliveries AS delivery ON delivery.delivery_id = transport.delivery_id
		JOIN audit_export_delivery_operations AS operation
		  ON operation.delivery_id = transport.delivery_id AND operation.operation = 'transport'
		WHERE transport.delivery_id = $1
	`, deliveryID).Scan(
		&attribution.ActorID, &attribution.ReasonDigest, &attribution.CorrelationID,
		&operationEvidence, &deliveryEvidence, &transportEvidence,
		&deliveryDestination, &transportDestination,
		&transportContract, &workloadIdentity.WorkloadID, &workloadIdentity.GrantSHA256,
		&workloadIdentity.Generation, &workloadIdentity.ClientCertificateSHA256, &state,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuditExportDeliveryAttribution{}, "", AuditExportWorkloadIdentityAuthorization{}, "", false, nil
	}
	if err != nil {
		return AuditExportDeliveryAttribution{}, "", AuditExportWorkloadIdentityAuthorization{}, "", false,
			fmt.Errorf("read audit export delivery transport: %w", err)
	}
	if !attribution.Valid() || subtle.ConstantTimeCompare(operationEvidence, deliveryEvidence) != 1 ||
		subtle.ConstantTimeCompare(operationEvidence, transportEvidence) != 1 ||
		subtle.ConstantTimeCompare(deliveryDestination, transportDestination) != 1 ||
		((transportContract == AuditExportDeliveryWorkloadTransportContract) != workloadIdentity.Valid()) {
		return AuditExportDeliveryAttribution{}, "", AuditExportWorkloadIdentityAuthorization{}, "", false,
			ErrAuditExportDeliveryConflict
	}
	return attribution, transportContract, workloadIdentity, state, true, nil
}

func readAuditExportDelivery(
	ctx context.Context,
	tx pgx.Tx,
	deliveryID string,
) (AuditExportDelivery, AuditExportDeliveryAttribution, string, bool, error) {
	var delivery AuditExportDelivery
	var attribution AuditExportDeliveryAttribution
	var status string
	var preparationEvidence []byte
	var acknowledgementDigest []byte
	var hasAcknowledgedAt bool
	var acknowledgementContract, storedRecipientTrustProfileSHA256, recipientSigningKeyID *string
	var recipientEncryptionKeyID *string
	var encryptedPackageDigest []byte
	var recipientAcceptedAt *time.Time
	var recipientTrustGeneration *int64
	err := tx.QueryRow(ctx, `
		SELECT delivery.contract, delivery.delivery_id, delivery.isolation_domain_id,
		       delivery.export_kind, delivery.export_id, delivery.envelope_digest,
		       delivery.export_sha256, delivery.trust_profile_sha256, delivery.signing_key_id,
		       delivery.recipient_id, delivery.destination_digest, delivery.status,
		       delivery.acknowledgement_digest, delivery.acknowledged_at IS NOT NULL,
		       delivery.acknowledgement_contract, delivery.recipient_trust_profile_sha256,
		       delivery.recipient_signing_key_id, delivery.recipient_accepted_at,
		       delivery.recipient_trust_generation, delivery.encrypted_package_digest,
		       delivery.recipient_encryption_key_id,
		       operation.actor_id, operation.reason_digest, operation.correlation_id,
		       operation.evidence_digest
		FROM audit_export_deliveries AS delivery
		JOIN audit_export_delivery_operations AS operation
		  ON operation.delivery_id = delivery.delivery_id AND operation.operation = 'prepare'
		WHERE delivery.delivery_id = $1
		FOR UPDATE OF delivery
	`, deliveryID).Scan(
		&delivery.Contract, &delivery.DeliveryID, &delivery.IsolationDomainID,
		&delivery.ExportKind, &delivery.ExportID, &delivery.EnvelopeDigest,
		&delivery.ExportSHA256, &delivery.TrustProfileSHA256, &delivery.SigningKeyID,
		&delivery.RecipientID, &delivery.DestinationDigest, &status,
		&acknowledgementDigest, &hasAcknowledgedAt,
		&acknowledgementContract, &storedRecipientTrustProfileSHA256,
		&recipientSigningKeyID, &recipientAcceptedAt, &recipientTrustGeneration,
		&encryptedPackageDigest, &recipientEncryptionKeyID,
		&attribution.ActorID, &attribution.ReasonDigest, &attribution.CorrelationID,
		&preparationEvidence,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuditExportDelivery{}, AuditExportDeliveryAttribution{}, "", false, nil
	}
	if err != nil {
		return AuditExportDelivery{}, AuditExportDeliveryAttribution{}, "", false,
			fmt.Errorf("read audit export delivery: %w", err)
	}
	validPrepared := status == "prepared" && delivery.Contract == AuditExportDeliveryContract &&
		len(acknowledgementDigest) == 0 && !hasAcknowledgedAt && acknowledgementContract == nil &&
		storedRecipientTrustProfileSHA256 == nil && recipientSigningKeyID == nil && recipientAcceptedAt == nil &&
		recipientTrustGeneration == nil && len(encryptedPackageDigest) == 0 && recipientEncryptionKeyID == nil
	validEncryptedPrepared := status == "prepared" &&
		encryptedAuditExportDeliveryContract(delivery.Contract) && len(acknowledgementDigest) == 0 &&
		!hasAcknowledgedAt && acknowledgementContract == nil && recipientSigningKeyID == nil &&
		recipientAcceptedAt == nil && len(encryptedPackageDigest) == sha256.Size &&
		storedRecipientTrustProfileSHA256 != nil &&
		auditExportDeliveryDigest.MatchString(*storedRecipientTrustProfileSHA256) &&
		recipientEncryptionKeyID != nil && auditExportDeliveryKeyID.MatchString(*recipientEncryptionKeyID) &&
		recipientTrustGeneration != nil && *recipientTrustGeneration > 0
	validLegacyAcknowledged := status == "acknowledged" && delivery.Contract == auditExportDeliveryLegacyContract &&
		len(acknowledgementDigest) == sha256.Size && hasAcknowledgedAt && acknowledgementContract == nil &&
		storedRecipientTrustProfileSHA256 == nil && recipientSigningKeyID == nil && recipientAcceptedAt == nil &&
		recipientTrustGeneration == nil && len(encryptedPackageDigest) == 0 && recipientEncryptionKeyID == nil
	validReceiptVerifiedAcknowledged := status == "acknowledged" &&
		delivery.Contract == AuditExportDeliveryReceiptVerifiedContract &&
		len(acknowledgementDigest) == sha256.Size && hasAcknowledgedAt && acknowledgementContract != nil &&
		*acknowledgementContract == auditExportDeliveryLegacyReceiptContract &&
		storedRecipientTrustProfileSHA256 != nil && auditExportDeliveryDigest.MatchString(*storedRecipientTrustProfileSHA256) &&
		recipientSigningKeyID != nil && auditExportDeliveryKeyID.MatchString(*recipientSigningKeyID) &&
		recipientAcceptedAt != nil && !recipientAcceptedAt.IsZero() && credentialTimestampExact(*recipientAcceptedAt) &&
		recipientTrustGeneration == nil && len(encryptedPackageDigest) == 0 && recipientEncryptionKeyID == nil
	validAuthorizedAcknowledged := status == "acknowledged" && delivery.Contract == AuditExportDeliveryContract &&
		len(acknowledgementDigest) == sha256.Size && hasAcknowledgedAt && acknowledgementContract != nil &&
		*acknowledgementContract == auditExportDeliveryReceiptContract &&
		storedRecipientTrustProfileSHA256 != nil && auditExportDeliveryDigest.MatchString(*storedRecipientTrustProfileSHA256) &&
		recipientSigningKeyID != nil && auditExportDeliveryKeyID.MatchString(*recipientSigningKeyID) &&
		recipientAcceptedAt != nil && !recipientAcceptedAt.IsZero() && credentialTimestampExact(*recipientAcceptedAt) &&
		recipientTrustGeneration != nil && *recipientTrustGeneration > 0 &&
		len(encryptedPackageDigest) == 0 && recipientEncryptionKeyID == nil
	validEncryptedAcknowledged := status == "acknowledged" &&
		encryptedAuditExportDeliveryContract(delivery.Contract) &&
		len(acknowledgementDigest) == sha256.Size && hasAcknowledgedAt && acknowledgementContract != nil &&
		((delivery.Contract == AuditExportEncryptedDeliveryContract &&
			*acknowledgementContract == auditExportEncryptedDeliveryReceiptContract) ||
			(delivery.Contract == AuditExportTransportedDeliveryContract &&
				*acknowledgementContract == auditExportTransportedDeliveryReceiptContract) ||
			(delivery.Contract == AuditExportWorkloadDeliveryContract &&
				*acknowledgementContract == auditExportWorkloadDeliveryReceiptContract)) &&
		storedRecipientTrustProfileSHA256 != nil &&
		auditExportDeliveryDigest.MatchString(*storedRecipientTrustProfileSHA256) &&
		recipientSigningKeyID != nil && auditExportDeliveryKeyID.MatchString(*recipientSigningKeyID) &&
		recipientAcceptedAt != nil && !recipientAcceptedAt.IsZero() && credentialTimestampExact(*recipientAcceptedAt) &&
		recipientTrustGeneration != nil && *recipientTrustGeneration > 0 &&
		len(encryptedPackageDigest) == sha256.Size && recipientEncryptionKeyID != nil &&
		auditExportDeliveryKeyID.MatchString(*recipientEncryptionKeyID)
	if encryptedAuditExportDeliveryContract(delivery.Contract) &&
		storedRecipientTrustProfileSHA256 != nil && recipientEncryptionKeyID != nil &&
		recipientTrustGeneration != nil {
		delivery.EncryptedPackageDigest = append([]byte(nil), encryptedPackageDigest...)
		delivery.RecipientTrustProfileSHA256 = *storedRecipientTrustProfileSHA256
		delivery.RecipientEncryptionKeyID = *recipientEncryptionKeyID
		delivery.RecipientTrustGeneration = *recipientTrustGeneration
	}
	wantPreparationEvidence := delivery.EnvelopeDigest
	if encryptedAuditExportDeliveryContract(delivery.Contract) {
		wantPreparationEvidence = delivery.EncryptedPackageDigest
	}
	if !validStoredAuditExportDelivery(delivery) || !attribution.Valid() ||
		subtle.ConstantTimeCompare(preparationEvidence, wantPreparationEvidence) != 1 ||
		(!validPrepared && !validLegacyAcknowledged && !validReceiptVerifiedAcknowledged &&
			!validAuthorizedAcknowledged && !validEncryptedPrepared && !validEncryptedAcknowledged) {
		return AuditExportDelivery{}, AuditExportDeliveryAttribution{}, "", false,
			ErrAuditExportDeliveryConflict
	}
	return delivery, attribution, status, true, nil
}

func readAuditExportDeliveryAcknowledgement(
	ctx context.Context,
	tx pgx.Tx,
	deliveryID string,
) (AuditExportDeliveryAcknowledgement, error) {
	var acknowledgement AuditExportDeliveryAcknowledgement
	var operationEvidence, recordedEvidence []byte
	var deliveryContract string
	var receiptContract, recipientTrustProfileSHA256, recipientSigningKeyID *string
	var acceptedAt *time.Time
	var recipientTrustGeneration *int64
	err := tx.QueryRow(ctx, `
		SELECT delivery.contract, operation.actor_id, operation.reason_digest, operation.correlation_id,
		       operation.evidence_digest, delivery.acknowledgement_digest,
		       delivery.acknowledgement_contract, delivery.recipient_trust_profile_sha256,
		       delivery.recipient_signing_key_id, delivery.recipient_accepted_at,
		       delivery.recipient_trust_generation
		FROM audit_export_delivery_operations AS operation
		JOIN audit_export_deliveries AS delivery ON delivery.delivery_id = operation.delivery_id
		WHERE operation.delivery_id = $1 AND operation.operation = 'acknowledge'
	`, deliveryID).Scan(
		&deliveryContract, &acknowledgement.Attribution.ActorID, &acknowledgement.Attribution.ReasonDigest,
		&acknowledgement.Attribution.CorrelationID, &operationEvidence, &recordedEvidence,
		&receiptContract, &recipientTrustProfileSHA256, &recipientSigningKeyID, &acceptedAt,
		&recipientTrustGeneration,
	)
	if err != nil {
		return AuditExportDeliveryAcknowledgement{}, fmt.Errorf("read audit export delivery acknowledgement: %w", err)
	}
	acknowledgement.AcknowledgementDigest = operationEvidence
	if subtle.ConstantTimeCompare(operationEvidence, recordedEvidence) != 1 ||
		!acknowledgement.Attribution.Valid() {
		return AuditExportDeliveryAcknowledgement{}, ErrAuditExportDeliveryConflict
	}
	if deliveryContract == auditExportDeliveryLegacyContract {
		if receiptContract != nil || recipientTrustProfileSHA256 != nil || recipientSigningKeyID != nil ||
			acceptedAt != nil || recipientTrustGeneration != nil {
			return AuditExportDeliveryAcknowledgement{}, ErrAuditExportDeliveryConflict
		}
		return acknowledgement, nil
	}
	if receiptContract == nil || recipientTrustProfileSHA256 == nil || recipientSigningKeyID == nil || acceptedAt == nil {
		return AuditExportDeliveryAcknowledgement{}, ErrAuditExportDeliveryConflict
	}
	if deliveryContract == AuditExportDeliveryReceiptVerifiedContract && recipientTrustGeneration != nil {
		return AuditExportDeliveryAcknowledgement{}, ErrAuditExportDeliveryConflict
	}
	if (deliveryContract == AuditExportDeliveryContract ||
		encryptedAuditExportDeliveryContract(deliveryContract)) &&
		(recipientTrustGeneration == nil || *recipientTrustGeneration < 1) {
		return AuditExportDeliveryAcknowledgement{}, ErrAuditExportDeliveryConflict
	}
	acknowledgement.ReceiptContract = *receiptContract
	acknowledgement.DeliveryContract = deliveryContract
	acknowledgement.RecipientTrustProfileSHA256 = *recipientTrustProfileSHA256
	acknowledgement.RecipientSigningKeyID = *recipientSigningKeyID
	acknowledgement.AcceptedAt = acceptedAt.UTC()
	if encryptedAuditExportDeliveryContract(deliveryContract) {
		acknowledgement.RecipientTrustGeneration = *recipientTrustGeneration
	}
	if !acknowledgement.Valid() {
		return AuditExportDeliveryAcknowledgement{}, ErrAuditExportDeliveryConflict
	}
	return acknowledgement, nil
}

func mapAuditExportDeliveryWriteError(action string, err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "23505" {
		return ErrAuditExportDeliveryConflict
	}
	return fmt.Errorf("%s: %w", action, err)
}

func sameAuditExportDelivery(left, right AuditExportDelivery) bool {
	return left.Contract == right.Contract && left.DeliveryID == right.DeliveryID &&
		left.IsolationDomainID == right.IsolationDomainID && left.ExportKind == right.ExportKind &&
		left.ExportID == right.ExportID && subtle.ConstantTimeCompare(left.EnvelopeDigest, right.EnvelopeDigest) == 1 &&
		left.ExportSHA256 == right.ExportSHA256 && left.TrustProfileSHA256 == right.TrustProfileSHA256 &&
		left.SigningKeyID == right.SigningKeyID && left.RecipientID == right.RecipientID &&
		bytes.Equal(left.DestinationDigest, right.DestinationDigest) &&
		bytes.Equal(left.EncryptedPackageDigest, right.EncryptedPackageDigest) &&
		left.RecipientTrustProfileSHA256 == right.RecipientTrustProfileSHA256 &&
		left.RecipientEncryptionKeyID == right.RecipientEncryptionKeyID
}

func sameAuditExportDeliveryPreparation(left, right AuditExportDelivery) bool {
	if sameAuditExportDelivery(left, right) {
		return true
	}
	if (left.Contract != auditExportDeliveryLegacyContract &&
		left.Contract != AuditExportDeliveryReceiptVerifiedContract) ||
		right.Contract != AuditExportDeliveryContract {
		return false
	}
	left.Contract = AuditExportDeliveryContract
	return sameAuditExportDelivery(left, right)
}

func sameAuditExportDeliveryAttribution(left, right AuditExportDeliveryAttribution) bool {
	return left.ActorID == right.ActorID && left.CorrelationID == right.CorrelationID &&
		bytes.Equal(left.ReasonDigest, right.ReasonDigest)
}

func sameAuditExportDeliveryAcknowledgement(
	left AuditExportDeliveryAcknowledgement,
	right AuditExportDeliveryAcknowledgement,
) bool {
	return subtle.ConstantTimeCompare(left.AcknowledgementDigest, right.AcknowledgementDigest) == 1 &&
		left.DeliveryContract == right.DeliveryContract &&
		left.ReceiptContract == right.ReceiptContract &&
		left.RecipientTrustProfileSHA256 == right.RecipientTrustProfileSHA256 &&
		left.RecipientSigningKeyID == right.RecipientSigningKeyID &&
		left.RecipientTrustGeneration == right.RecipientTrustGeneration &&
		left.AcceptedAt.Equal(right.AcceptedAt) &&
		sameAuditExportDeliveryAttribution(left.Attribution, right.Attribution)
}

func cloneAuditExportDelivery(delivery AuditExportDelivery) AuditExportDelivery {
	delivery.EnvelopeDigest = append([]byte(nil), delivery.EnvelopeDigest...)
	delivery.DestinationDigest = append([]byte(nil), delivery.DestinationDigest...)
	delivery.EncryptedPackageDigest = append([]byte(nil), delivery.EncryptedPackageDigest...)
	return delivery
}

func cloneAuditExportDeliveryAttribution(
	attribution AuditExportDeliveryAttribution,
) AuditExportDeliveryAttribution {
	attribution.ReasonDigest = append([]byte(nil), attribution.ReasonDigest...)
	return attribution
}

func cloneAuditExportDeliveryAcknowledgement(
	acknowledgement AuditExportDeliveryAcknowledgement,
) AuditExportDeliveryAcknowledgement {
	acknowledgement.AcknowledgementDigest = append([]byte(nil), acknowledgement.AcknowledgementDigest...)
	acknowledgement.AcceptedAt = acknowledgement.AcceptedAt.UTC()
	acknowledgement.Attribution = cloneAuditExportDeliveryAttribution(acknowledgement.Attribution)
	return acknowledgement
}

func validStoredAuditExportDelivery(delivery AuditExportDelivery) bool {
	if delivery.Contract == AuditExportDeliveryContract ||
		encryptedAuditExportDeliveryContract(delivery.Contract) {
		return delivery.Valid()
	}
	if delivery.Contract != auditExportDeliveryLegacyContract &&
		delivery.Contract != AuditExportDeliveryReceiptVerifiedContract {
		return false
	}
	delivery.Contract = AuditExportDeliveryContract
	return delivery.Valid()
}

func encryptedAuditExportDeliveryContract(contract string) bool {
	return contract == AuditExportEncryptedDeliveryContract ||
		contract == AuditExportTransportedDeliveryContract ||
		contract == AuditExportWorkloadDeliveryContract
}

func sameAuditExportWorkloadIdentityAuthorization(
	left AuditExportWorkloadIdentityAuthorization,
	right AuditExportWorkloadIdentityAuthorization,
) bool {
	return left.WorkloadID == right.WorkloadID && left.GrantSHA256 == right.GrantSHA256 &&
		left.ClientCertificateSHA256 == right.ClientCertificateSHA256 &&
		left.Generation == right.Generation
}

func nullAuditExportDeliveryBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func nullAuditExportDeliveryText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullAuditExportDeliveryGeneration(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func digestOptionalBytes(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	return digestBytes(value)
}

func digestBytes(value []byte) string {
	return "sha256:" + hex.EncodeToString(value)
}
