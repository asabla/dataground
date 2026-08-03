-- dataground:up

DROP TRIGGER audit_export_deliveries_controlled_update ON audit_export_deliveries;

ALTER TABLE audit_export_deliveries
    DROP CONSTRAINT audit_export_deliveries_contract_check,
    ADD COLUMN acknowledgement_contract text,
    ADD COLUMN recipient_trust_profile_sha256 text,
    ADD COLUMN recipient_signing_key_id text,
    ADD COLUMN recipient_accepted_at timestamptz;

UPDATE audit_export_deliveries
SET contract = 'dataground.audit-export-delivery/v2'
WHERE status = 'prepared';

ALTER TABLE audit_export_deliveries
    ADD CONSTRAINT audit_export_deliveries_contract_check
        CHECK (contract IN ('dataground.audit-export-delivery/v1', 'dataground.audit-export-delivery/v2')),
    ADD CONSTRAINT audit_export_deliveries_verification_check
        CHECK (
            (contract = 'dataground.audit-export-delivery/v1' AND status = 'acknowledged' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v2' AND status = 'prepared' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v2' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v1' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL)
        );

CREATE OR REPLACE FUNCTION enforce_audit_export_delivery_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.status <> 'prepared' OR NEW.status <> 'acknowledged' OR
       OLD.delivery_id <> NEW.delivery_id OR
       OLD.isolation_domain_id <> NEW.isolation_domain_id OR
       OLD.contract <> NEW.contract OR
       OLD.export_kind <> NEW.export_kind OR
       OLD.export_id <> NEW.export_id OR
       OLD.envelope_digest <> NEW.envelope_digest OR
       OLD.export_sha256 <> NEW.export_sha256 OR
       OLD.trust_profile_sha256 <> NEW.trust_profile_sha256 OR
       OLD.signing_key_id <> NEW.signing_key_id OR
       OLD.recipient_id <> NEW.recipient_id OR
       OLD.destination_digest <> NEW.destination_digest OR
       OLD.prepared_at <> NEW.prepared_at OR
       NEW.acknowledgement_digest IS NULL OR
       NEW.acknowledged_at IS NULL OR
       NOT EXISTS (
           SELECT 1
           FROM audit_export_delivery_operations AS operation
           WHERE operation.delivery_id = NEW.delivery_id
             AND operation.isolation_domain_id = NEW.isolation_domain_id
             AND operation.operation = 'acknowledge'
             AND operation.evidence_digest = NEW.acknowledgement_digest
       ) THEN
        RAISE EXCEPTION 'audit export deliveries permit only operation-bound prepared-to-acknowledged transitions';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_deliveries_controlled_update
BEFORE UPDATE ON audit_export_deliveries
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transition();

-- dataground:down

DROP TRIGGER audit_export_deliveries_controlled_update ON audit_export_deliveries;

ALTER TABLE audit_export_deliveries
    DROP CONSTRAINT audit_export_deliveries_verification_check,
    DROP CONSTRAINT audit_export_deliveries_contract_check;

UPDATE audit_export_deliveries
SET contract = 'dataground.audit-export-delivery/v1';

ALTER TABLE audit_export_deliveries
    DROP COLUMN recipient_accepted_at,
    DROP COLUMN recipient_signing_key_id,
    DROP COLUMN recipient_trust_profile_sha256,
    DROP COLUMN acknowledgement_contract,
    ADD CONSTRAINT audit_export_deliveries_contract_check
        CHECK (contract = 'dataground.audit-export-delivery/v1');

CREATE TRIGGER audit_export_deliveries_controlled_update
BEFORE UPDATE ON audit_export_deliveries
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transition();
