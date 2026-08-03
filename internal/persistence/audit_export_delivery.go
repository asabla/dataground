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

	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const AuditExportDeliveryContract = "dataground.audit-export-delivery/v1"

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
	Contract           string
	DeliveryID         string
	IsolationDomainID  string
	ExportKind         string
	ExportID           string
	EnvelopeDigest     []byte
	ExportSHA256       string
	TrustProfileSHA256 string
	SigningKeyID       string
	RecipientID        string
	DestinationDigest  []byte
}

type AuditExportDeliveryAttribution struct {
	ActorID       string
	ReasonDigest  []byte
	CorrelationID string
}

type AuditExportDeliveryAcknowledgement struct {
	AcknowledgementDigest []byte
	Attribution           AuditExportDeliveryAttribution
}

func (delivery AuditExportDelivery) Valid() bool {
	if delivery.Contract != AuditExportDeliveryContract ||
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
	return len(acknowledgement.AcknowledgementDigest) == sha256.Size && acknowledgement.Attribution.Valid()
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
		if !sameAuditExportDelivery(existing, delivery) ||
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
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_export_deliveries (
			delivery_id, isolation_domain_id, contract, export_kind, export_id,
			envelope_digest, export_sha256, trust_profile_sha256, signing_key_id,
			recipient_id, destination_digest
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, delivery.DeliveryID, delivery.IsolationDomainID, delivery.Contract, delivery.ExportKind,
		delivery.ExportID, delivery.EnvelopeDigest, delivery.ExportSHA256, delivery.TrustProfileSHA256,
		delivery.SigningKeyID, delivery.RecipientID, delivery.DestinationDigest); err != nil {
		return mapAuditExportDeliveryWriteError("prepare audit export delivery", err)
	}
	if err := insertAuditExportDeliveryOperation(ctx, tx, delivery, "prepare", attribution, delivery.EnvelopeDigest); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, 'audit-export-delivery.prepare', 'audit-export-delivery', $4,
			'accepted', $5,
			jsonb_build_object(
				'destinationDigest', $6::text,
				'envelopeDigest', $7::text,
				'exportKind', $8::text,
				'reasonDigest', $9::text,
				'recipientId', $10::text,
				'signingKeyId', $11::text,
				'trustProfileSha256', $12::text
			),
			clock_timestamp()
		)
	`, identity.New("aud"), delivery.IsolationDomainID, attribution.ActorID, delivery.DeliveryID,
		attribution.CorrelationID, digestBytes(delivery.DestinationDigest), digestBytes(delivery.EnvelopeDigest),
		delivery.ExportKind, digestBytes(attribution.ReasonDigest), delivery.RecipientID,
		delivery.SigningKeyID, delivery.TrustProfileSHA256); err != nil {
		return fmt.Errorf("audit export delivery preparation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit export delivery preparation: %w", err)
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
	if !exists || !sameAuditExportDelivery(existing, delivery) {
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
	if err := insertAuditExportDeliveryOperation(
		ctx, tx, delivery, "acknowledge", acknowledgement.Attribution, acknowledgement.AcknowledgementDigest,
	); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `
		UPDATE audit_export_deliveries
		SET status = 'acknowledged', acknowledgement_digest = $2, acknowledged_at = clock_timestamp()
		WHERE delivery_id = $1 AND status = 'prepared'
	`, delivery.DeliveryID, acknowledgement.AcknowledgementDigest)
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
				'reasonDigest', $8::text
			),
			clock_timestamp()
		)
	`, identity.New("aud"), delivery.IsolationDomainID, acknowledgement.Attribution.ActorID,
		delivery.DeliveryID, acknowledgement.Attribution.CorrelationID,
		digestBytes(acknowledgement.AcknowledgementDigest), digestBytes(delivery.EnvelopeDigest),
		digestBytes(acknowledgement.Attribution.ReasonDigest)); err != nil {
		return fmt.Errorf("audit export delivery acknowledgement: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit export delivery acknowledgement: %w", err)
	}
	return nil
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
	err := tx.QueryRow(ctx, `
		SELECT delivery.contract, delivery.delivery_id, delivery.isolation_domain_id,
		       delivery.export_kind, delivery.export_id, delivery.envelope_digest,
		       delivery.export_sha256, delivery.trust_profile_sha256, delivery.signing_key_id,
		       delivery.recipient_id, delivery.destination_digest, delivery.status,
		       delivery.acknowledgement_digest, delivery.acknowledged_at IS NOT NULL,
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
	validState := status == "prepared" && len(acknowledgementDigest) == 0 && !hasAcknowledgedAt ||
		status == "acknowledged" && len(acknowledgementDigest) == sha256.Size && hasAcknowledgedAt
	if !delivery.Valid() || !attribution.Valid() ||
		subtle.ConstantTimeCompare(preparationEvidence, delivery.EnvelopeDigest) != 1 ||
		!validState {
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
	err := tx.QueryRow(ctx, `
		SELECT operation.actor_id, operation.reason_digest, operation.correlation_id,
		       operation.evidence_digest, delivery.acknowledgement_digest
		FROM audit_export_delivery_operations AS operation
		JOIN audit_export_deliveries AS delivery ON delivery.delivery_id = operation.delivery_id
		WHERE operation.delivery_id = $1 AND operation.operation = 'acknowledge'
	`, deliveryID).Scan(
		&acknowledgement.Attribution.ActorID, &acknowledgement.Attribution.ReasonDigest,
		&acknowledgement.Attribution.CorrelationID, &operationEvidence, &recordedEvidence,
	)
	if err != nil {
		return AuditExportDeliveryAcknowledgement{}, fmt.Errorf("read audit export delivery acknowledgement: %w", err)
	}
	acknowledgement.AcknowledgementDigest = operationEvidence
	if !acknowledgement.Valid() || subtle.ConstantTimeCompare(operationEvidence, recordedEvidence) != 1 {
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
		bytes.Equal(left.DestinationDigest, right.DestinationDigest)
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
		sameAuditExportDeliveryAttribution(left.Attribution, right.Attribution)
}

func cloneAuditExportDelivery(delivery AuditExportDelivery) AuditExportDelivery {
	delivery.EnvelopeDigest = append([]byte(nil), delivery.EnvelopeDigest...)
	delivery.DestinationDigest = append([]byte(nil), delivery.DestinationDigest...)
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
	acknowledgement.Attribution = cloneAuditExportDeliveryAttribution(acknowledgement.Attribution)
	return acknowledgement
}

func digestBytes(value []byte) string {
	return "sha256:" + hex.EncodeToString(value)
}
