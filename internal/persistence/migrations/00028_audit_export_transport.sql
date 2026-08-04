-- dataground:up

DROP TRIGGER audit_export_deliveries_controlled_update ON audit_export_deliveries;
DROP TRIGGER audit_export_deliveries_controlled_insert ON audit_export_deliveries;
DROP FUNCTION enforce_audit_export_encrypted_delivery_prepare();

ALTER TABLE audit_export_delivery_operations
    DROP CONSTRAINT audit_export_delivery_operations_operation_check,
    ADD CONSTRAINT audit_export_delivery_operations_operation_check
        CHECK (operation IN ('prepare', 'transport', 'acknowledge'));

ALTER TABLE audit_export_deliveries
    DROP CONSTRAINT audit_export_deliveries_verification_check,
    DROP CONSTRAINT audit_export_deliveries_contract_check,
    ADD CONSTRAINT audit_export_deliveries_contract_check
        CHECK (contract IN (
            'dataground.audit-export-delivery/v1',
            'dataground.audit-export-delivery/v2',
            'dataground.audit-export-delivery/v3',
            'dataground.audit-export-delivery/v4',
            'dataground.audit-export-delivery/v5'
        )),
    ADD CONSTRAINT audit_export_deliveries_verification_check
        CHECK (
            (contract = 'dataground.audit-export-delivery/v1' AND status = 'acknowledged' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation IS NULL AND encrypted_package_digest IS NULL AND
             recipient_encryption_key_id IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v2' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v1' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation IS NULL AND
             encrypted_package_digest IS NULL AND recipient_encryption_key_id IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v3' AND status = 'prepared' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation IS NULL AND encrypted_package_digest IS NULL AND
             recipient_encryption_key_id IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v3' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v2' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation > 0 AND
             encrypted_package_digest IS NULL AND recipient_encryption_key_id IS NULL) OR
            (contract IN ('dataground.audit-export-delivery/v4', 'dataground.audit-export-delivery/v5') AND
             status = 'prepared' AND acknowledgement_contract IS NULL AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation > 0 AND octet_length(encrypted_package_digest) = 32 AND
             recipient_encryption_key_id ~ '^[a-z][a-z0-9_-]{2,63}$') OR
            (contract = 'dataground.audit-export-delivery/v4' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v3' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation > 0 AND
             octet_length(encrypted_package_digest) = 32 AND
             recipient_encryption_key_id ~ '^[a-z][a-z0-9_-]{2,63}$') OR
            (contract = 'dataground.audit-export-delivery/v5' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v4' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation > 0 AND
             octet_length(encrypted_package_digest) = 32 AND
             recipient_encryption_key_id ~ '^[a-z][a-z0-9_-]{2,63}$')
        );

CREATE TABLE audit_export_delivery_transports (
    delivery_id text PRIMARY KEY,
    isolation_domain_id text NOT NULL,
    transport_contract text NOT NULL,
    destination_digest bytea NOT NULL,
    encrypted_package_digest bytea NOT NULL,
    state text NOT NULL DEFAULT 'reserved',
    reserved_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz,
    FOREIGN KEY (delivery_id, isolation_domain_id)
        REFERENCES audit_export_deliveries(delivery_id, isolation_domain_id),
    CHECK (delivery_id ~ '^adl_[0-9a-z]{20,32}$'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (transport_contract = 'dataground.audit-export-transport/s3-immutable/v1'),
    CHECK (octet_length(destination_digest) = 32),
    CHECK (octet_length(encrypted_package_digest) = 32),
    CHECK (state IN ('reserved', 'completed')),
    CHECK (
        (state = 'reserved' AND isfinite(reserved_at) AND completed_at IS NULL) OR
        (state = 'completed' AND isfinite(reserved_at) AND isfinite(completed_at) AND
         completed_at >= reserved_at)
    )
);

CREATE FUNCTION audit_export_recipient_encryption_is_authorized(
    requested_domain text,
    requested_recipient text,
    requested_generation bigint,
    requested_profile text,
    requested_encryption_key text,
    requested_signing_key text
)
RETURNS boolean
LANGUAGE sql
VOLATILE
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM audit_export_recipient_trust_events AS trust_event
        JOIN audit_export_recipient_encryption_keys AS encryption_key
          ON encryption_key.isolation_domain_id = trust_event.isolation_domain_id
         AND encryption_key.recipient_id = trust_event.recipient_id
         AND encryption_key.generation = trust_event.generation
        WHERE trust_event.isolation_domain_id = requested_domain
          AND trust_event.recipient_id = requested_recipient
          AND trust_event.generation = requested_generation
          AND trust_event.authorization_contract =
              'dataground.audit-export-recipient-trust-authorization/v3'
          AND trust_event.operation = 'activate'
          AND trust_event.trust_contract =
              'dataground.audit-export-recipient-trust/ed25519-x25519/v2'
          AND trust_event.trust_profile_sha256 = requested_profile
          AND isfinite(trust_event.identity_proof_expires_at)
          AND trust_event.identity_proof_expires_at > clock_timestamp()
          AND encryption_key.key_id = requested_encryption_key
          AND trust_event.generation = (
              SELECT max(latest.generation)
              FROM audit_export_recipient_trust_events AS latest
              WHERE latest.isolation_domain_id = requested_domain
                AND latest.recipient_id = requested_recipient
          )
          AND (requested_signing_key IS NULL OR EXISTS (
              SELECT 1
              FROM audit_export_recipient_trust_keys AS signing_key
              WHERE signing_key.isolation_domain_id = trust_event.isolation_domain_id
                AND signing_key.recipient_id = trust_event.recipient_id
                AND signing_key.generation = trust_event.generation
                AND signing_key.key_id = requested_signing_key
          ))
          AND NOT EXISTS (
              SELECT 1
              FROM audit_export_recipient_proof_revocations AS revocation
              WHERE revocation.isolation_domain_id = trust_event.isolation_domain_id
                AND revocation.proofing_authority_id = trust_event.proofing_authority_id
                AND revocation.proofing_trust_profile_sha256 = trust_event.proofing_trust_profile_sha256
                AND revocation.effective_at <= clock_timestamp()
                AND (
                    revocation.scope = 'profile' OR
                    (revocation.scope = 'key' AND
                     revocation.proofing_signing_key_id = trust_event.proofing_signing_key_id)
                )
          )
    );
$$;

CREATE FUNCTION enforce_audit_export_transport_delivery_prepare()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.contract <> 'dataground.audit-export-delivery/v5' THEN
        RAISE EXCEPTION 'new audit export deliveries require durable transport evidence';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-proof-revocation' || E'\n' || NEW.isolation_domain_id, 0
    ));
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id, 0
    ));
    IF NOT audit_export_recipient_encryption_is_authorized(
        NEW.isolation_domain_id,
        NEW.recipient_id,
        NEW.recipient_trust_generation,
        NEW.recipient_trust_profile_sha256,
        NEW.recipient_encryption_key_id,
        NULL
    ) THEN
        RAISE EXCEPTION 'audit export delivery encryption is not authorized';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_deliveries_controlled_insert
BEFORE INSERT ON audit_export_deliveries
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_transport_delivery_prepare();

CREATE FUNCTION enforce_audit_export_delivery_transport_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    delivery audit_export_deliveries%ROWTYPE;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(NEW.delivery_id, 0));
    SELECT * INTO delivery
    FROM audit_export_deliveries
    WHERE delivery_id = NEW.delivery_id
    FOR UPDATE;
    IF FOUND THEN
        PERFORM pg_advisory_xact_lock(hashtextextended(
            'audit-export-recipient-proof-revocation' || E'\n' || delivery.isolation_domain_id, 0
        ));
        PERFORM pg_advisory_xact_lock(hashtextextended(
            'audit-export-recipient-trust' || E'\n' || delivery.isolation_domain_id || E'\n' ||
            delivery.recipient_id, 0
        ));
    END IF;
    IF NOT FOUND OR delivery.contract <> 'dataground.audit-export-delivery/v5' OR
       delivery.status <> 'prepared' OR
       delivery.isolation_domain_id <> NEW.isolation_domain_id OR
       delivery.destination_digest <> NEW.destination_digest OR
       delivery.encrypted_package_digest <> NEW.encrypted_package_digest OR
       NOT EXISTS (
           SELECT 1 FROM audit_export_delivery_operations AS operation
           WHERE operation.delivery_id = NEW.delivery_id
             AND operation.isolation_domain_id = NEW.isolation_domain_id
             AND operation.operation = 'transport'
             AND operation.evidence_digest = NEW.encrypted_package_digest
       ) OR NOT audit_export_recipient_encryption_is_authorized(
           delivery.isolation_domain_id,
           delivery.recipient_id,
           delivery.recipient_trust_generation,
           delivery.recipient_trust_profile_sha256,
           delivery.recipient_encryption_key_id,
           NULL
       ) THEN
        RAISE EXCEPTION 'audit export delivery transport reservation is invalid';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_delivery_transports_controlled_insert
BEFORE INSERT ON audit_export_delivery_transports
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transport_insert();

CREATE FUNCTION enforce_audit_export_delivery_transport_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.state <> 'reserved' OR NEW.state <> 'completed' OR
       OLD.delivery_id <> NEW.delivery_id OR
       OLD.isolation_domain_id <> NEW.isolation_domain_id OR
       OLD.transport_contract <> NEW.transport_contract OR
       OLD.destination_digest <> NEW.destination_digest OR
       OLD.encrypted_package_digest <> NEW.encrypted_package_digest OR
       OLD.reserved_at <> NEW.reserved_at OR NEW.completed_at IS NULL THEN
        RAISE EXCEPTION 'audit export delivery transport transition is invalid';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_delivery_transports_controlled_update
BEFORE UPDATE ON audit_export_delivery_transports
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transport_transition();

CREATE TRIGGER audit_export_delivery_transports_no_delete
BEFORE DELETE ON audit_export_delivery_transports
FOR EACH ROW EXECUTE FUNCTION reject_audit_export_delivery_delete();

CREATE OR REPLACE FUNCTION enforce_audit_export_delivery_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-proof-revocation' || E'\n' || NEW.isolation_domain_id, 0
    ));
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id, 0
    ));
    IF OLD.status <> 'prepared' OR NEW.status <> 'acknowledged' OR
       OLD.delivery_id <> NEW.delivery_id OR OLD.isolation_domain_id <> NEW.isolation_domain_id OR
       OLD.contract <> NEW.contract OR OLD.export_kind <> NEW.export_kind OR
       OLD.export_id <> NEW.export_id OR OLD.envelope_digest <> NEW.envelope_digest OR
       OLD.export_sha256 <> NEW.export_sha256 OR OLD.trust_profile_sha256 <> NEW.trust_profile_sha256 OR
       OLD.signing_key_id <> NEW.signing_key_id OR OLD.recipient_id <> NEW.recipient_id OR
       OLD.destination_digest <> NEW.destination_digest OR OLD.prepared_at <> NEW.prepared_at OR
       OLD.encrypted_package_digest IS DISTINCT FROM NEW.encrypted_package_digest OR
       OLD.recipient_encryption_key_id IS DISTINCT FROM NEW.recipient_encryption_key_id OR
       NEW.acknowledgement_digest IS NULL OR NEW.acknowledged_at IS NULL OR
       NOT EXISTS (
           SELECT 1 FROM audit_export_delivery_operations AS operation
           WHERE operation.delivery_id = NEW.delivery_id
             AND operation.isolation_domain_id = NEW.isolation_domain_id
             AND operation.operation = 'acknowledge'
             AND operation.evidence_digest = NEW.acknowledgement_digest
       ) THEN
        RAISE EXCEPTION 'audit export delivery transition is invalid';
    END IF;
    IF NEW.contract IN ('dataground.audit-export-delivery/v4', 'dataground.audit-export-delivery/v5') THEN
        IF OLD.recipient_trust_profile_sha256 IS DISTINCT FROM NEW.recipient_trust_profile_sha256 OR
           OLD.recipient_trust_generation IS DISTINCT FROM NEW.recipient_trust_generation OR
           (NEW.contract = 'dataground.audit-export-delivery/v4' AND
            NEW.acknowledgement_contract <> 'dataground.audit-export-delivery-receipt/ed25519/v3') OR
           (NEW.contract = 'dataground.audit-export-delivery/v5' AND
            NEW.acknowledgement_contract <> 'dataground.audit-export-delivery-receipt/ed25519/v4') OR
           (NEW.contract = 'dataground.audit-export-delivery/v5' AND NOT EXISTS (
               SELECT 1 FROM audit_export_delivery_transports AS transport
               WHERE transport.delivery_id = NEW.delivery_id
                 AND transport.isolation_domain_id = NEW.isolation_domain_id
                 AND transport.state = 'completed'
                 AND transport.destination_digest = NEW.destination_digest
                 AND transport.encrypted_package_digest = NEW.encrypted_package_digest
           )) OR NOT audit_export_recipient_encryption_is_authorized(
               NEW.isolation_domain_id,
               NEW.recipient_id,
               NEW.recipient_trust_generation,
               NEW.recipient_trust_profile_sha256,
               NEW.recipient_encryption_key_id,
               NEW.recipient_signing_key_id
           ) THEN
            RAISE EXCEPTION 'encrypted audit export acknowledgement is not authorized';
        END IF;
    ELSIF NEW.contract = 'dataground.audit-export-delivery/v3' THEN
        IF NEW.acknowledgement_contract <>
               'dataground.audit-export-delivery-receipt/ed25519/v2' OR
           NEW.recipient_trust_generation IS NULL OR
           NOT EXISTS (
               SELECT 1
               FROM audit_export_recipient_trust_events AS trust_event
               JOIN audit_export_recipient_trust_keys AS trust_key
                 ON trust_key.isolation_domain_id = trust_event.isolation_domain_id
                AND trust_key.recipient_id = trust_event.recipient_id
                AND trust_key.generation = trust_event.generation
               WHERE trust_event.isolation_domain_id = NEW.isolation_domain_id
                 AND trust_event.recipient_id = NEW.recipient_id
                 AND trust_event.generation = NEW.recipient_trust_generation
                 AND trust_event.authorization_contract =
                     'dataground.audit-export-recipient-trust-authorization/v2'
                 AND trust_event.operation = 'activate'
                 AND trust_event.trust_profile_sha256 = NEW.recipient_trust_profile_sha256
                 AND isfinite(trust_event.identity_proof_expires_at)
                 AND trust_event.identity_proof_expires_at > clock_timestamp()
                 AND trust_key.key_id = NEW.recipient_signing_key_id
                 AND trust_event.generation = (
                     SELECT max(latest.generation)
                     FROM audit_export_recipient_trust_events AS latest
                     WHERE latest.isolation_domain_id = NEW.isolation_domain_id
                       AND latest.recipient_id = NEW.recipient_id
                 )
                 AND NOT EXISTS (
                     SELECT 1 FROM audit_export_recipient_proof_revocations AS revocation
                     WHERE revocation.isolation_domain_id = trust_event.isolation_domain_id
                       AND revocation.proofing_authority_id = trust_event.proofing_authority_id
                       AND revocation.proofing_trust_profile_sha256 = trust_event.proofing_trust_profile_sha256
                       AND revocation.effective_at <= clock_timestamp()
                       AND (revocation.scope = 'profile' OR
                            (revocation.scope = 'key' AND
                             revocation.proofing_signing_key_id = trust_event.proofing_signing_key_id))
                 )
           ) THEN
            RAISE EXCEPTION 'legacy audit export acknowledgement is not authorized';
        END IF;
    ELSE
        RAISE EXCEPTION 'audit export delivery contract cannot transition';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_deliveries_controlled_update
BEFORE UPDATE ON audit_export_deliveries
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transition();

-- dataground:down

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM audit_export_deliveries
        WHERE contract = 'dataground.audit-export-delivery/v5'
    ) OR EXISTS (SELECT 1 FROM audit_export_delivery_transports) OR EXISTS (
        SELECT 1 FROM audit_export_delivery_operations WHERE operation = 'transport'
    ) THEN
        RAISE EXCEPTION 'schema 28 contains audit transport evidence and cannot be downgraded safely';
    END IF;
END;
$$;

DROP TRIGGER audit_export_deliveries_controlled_update ON audit_export_deliveries;
DROP TRIGGER audit_export_delivery_transports_no_delete ON audit_export_delivery_transports;
DROP TRIGGER audit_export_delivery_transports_controlled_update ON audit_export_delivery_transports;
DROP FUNCTION enforce_audit_export_delivery_transport_transition();
DROP TRIGGER audit_export_delivery_transports_controlled_insert ON audit_export_delivery_transports;
DROP FUNCTION enforce_audit_export_delivery_transport_insert();
DROP TRIGGER audit_export_deliveries_controlled_insert ON audit_export_deliveries;
DROP FUNCTION enforce_audit_export_transport_delivery_prepare();
DROP TABLE audit_export_delivery_transports;
DROP FUNCTION audit_export_recipient_encryption_is_authorized(text, text, bigint, text, text, text);

ALTER TABLE audit_export_delivery_operations
    DROP CONSTRAINT audit_export_delivery_operations_operation_check,
    ADD CONSTRAINT audit_export_delivery_operations_operation_check
        CHECK (operation IN ('prepare', 'acknowledge'));

ALTER TABLE audit_export_deliveries
    DROP CONSTRAINT audit_export_deliveries_verification_check,
    DROP CONSTRAINT audit_export_deliveries_contract_check,
    ADD CONSTRAINT audit_export_deliveries_contract_check
        CHECK (contract IN (
            'dataground.audit-export-delivery/v1',
            'dataground.audit-export-delivery/v2',
            'dataground.audit-export-delivery/v3',
            'dataground.audit-export-delivery/v4'
        )),
    ADD CONSTRAINT audit_export_deliveries_verification_check
        CHECK (
            (contract = 'dataground.audit-export-delivery/v1' AND status = 'acknowledged' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation IS NULL AND encrypted_package_digest IS NULL AND
             recipient_encryption_key_id IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v2' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v1' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation IS NULL AND
             encrypted_package_digest IS NULL AND recipient_encryption_key_id IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v3' AND status = 'prepared' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation IS NULL AND encrypted_package_digest IS NULL AND
             recipient_encryption_key_id IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v3' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v2' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation > 0 AND
             encrypted_package_digest IS NULL AND recipient_encryption_key_id IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v4' AND status = 'prepared' AND
             acknowledgement_contract IS NULL AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation > 0 AND octet_length(encrypted_package_digest) = 32 AND
             recipient_encryption_key_id ~ '^[a-z][a-z0-9_-]{2,63}$') OR
            (contract = 'dataground.audit-export-delivery/v4' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v3' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation > 0 AND
             octet_length(encrypted_package_digest) = 32 AND
             recipient_encryption_key_id ~ '^[a-z][a-z0-9_-]{2,63}$')
        );

CREATE FUNCTION enforce_audit_export_encrypted_delivery_prepare()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.contract <> 'dataground.audit-export-delivery/v4' THEN
        RAISE EXCEPTION 'new audit export deliveries require recipient encryption';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-proof-revocation' || E'\n' || NEW.isolation_domain_id, 0
    ));
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id, 0
    ));
    IF NOT EXISTS (
        SELECT 1
        FROM audit_export_recipient_trust_events AS trust_event
        JOIN audit_export_recipient_encryption_keys AS encryption_key
          ON encryption_key.isolation_domain_id = trust_event.isolation_domain_id
         AND encryption_key.recipient_id = trust_event.recipient_id
         AND encryption_key.generation = trust_event.generation
        WHERE trust_event.isolation_domain_id = NEW.isolation_domain_id
          AND trust_event.recipient_id = NEW.recipient_id
          AND trust_event.generation = NEW.recipient_trust_generation
          AND trust_event.authorization_contract =
              'dataground.audit-export-recipient-trust-authorization/v3'
          AND trust_event.operation = 'activate'
          AND trust_event.trust_contract =
              'dataground.audit-export-recipient-trust/ed25519-x25519/v2'
          AND trust_event.trust_profile_sha256 = NEW.recipient_trust_profile_sha256
          AND isfinite(trust_event.identity_proof_expires_at)
          AND trust_event.identity_proof_expires_at > clock_timestamp()
          AND encryption_key.key_id = NEW.recipient_encryption_key_id
          AND trust_event.generation = (
              SELECT max(latest.generation) FROM audit_export_recipient_trust_events AS latest
              WHERE latest.isolation_domain_id = NEW.isolation_domain_id
                AND latest.recipient_id = NEW.recipient_id
          )
          AND NOT EXISTS (
              SELECT 1 FROM audit_export_recipient_proof_revocations AS revocation
              WHERE revocation.isolation_domain_id = trust_event.isolation_domain_id
                AND revocation.proofing_authority_id = trust_event.proofing_authority_id
                AND revocation.proofing_trust_profile_sha256 = trust_event.proofing_trust_profile_sha256
                AND revocation.effective_at <= clock_timestamp()
                AND (revocation.scope = 'profile' OR
                     (revocation.scope = 'key' AND
                      revocation.proofing_signing_key_id = trust_event.proofing_signing_key_id))
          )
    ) THEN
        RAISE EXCEPTION 'audit export delivery encryption is not authorized';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_deliveries_controlled_insert
BEFORE INSERT ON audit_export_deliveries
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_encrypted_delivery_prepare();

CREATE OR REPLACE FUNCTION enforce_audit_export_delivery_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-proof-revocation' || E'\n' || NEW.isolation_domain_id, 0
    ));
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id, 0
    ));
    IF OLD.status <> 'prepared' OR NEW.status <> 'acknowledged' OR
       OLD.delivery_id <> NEW.delivery_id OR OLD.isolation_domain_id <> NEW.isolation_domain_id OR
       OLD.contract <> NEW.contract OR OLD.export_kind <> NEW.export_kind OR
       OLD.export_id <> NEW.export_id OR OLD.envelope_digest <> NEW.envelope_digest OR
       OLD.export_sha256 <> NEW.export_sha256 OR OLD.trust_profile_sha256 <> NEW.trust_profile_sha256 OR
       OLD.signing_key_id <> NEW.signing_key_id OR OLD.recipient_id <> NEW.recipient_id OR
       OLD.destination_digest <> NEW.destination_digest OR OLD.prepared_at <> NEW.prepared_at OR
       OLD.encrypted_package_digest IS DISTINCT FROM NEW.encrypted_package_digest OR
       OLD.recipient_encryption_key_id IS DISTINCT FROM NEW.recipient_encryption_key_id OR
       NEW.acknowledgement_digest IS NULL OR NEW.acknowledged_at IS NULL OR
       NOT EXISTS (
           SELECT 1 FROM audit_export_delivery_operations AS operation
           WHERE operation.delivery_id = NEW.delivery_id
             AND operation.isolation_domain_id = NEW.isolation_domain_id
             AND operation.operation = 'acknowledge'
             AND operation.evidence_digest = NEW.acknowledgement_digest
       ) THEN
        RAISE EXCEPTION 'audit export delivery transition is invalid';
    END IF;
    IF NEW.contract = 'dataground.audit-export-delivery/v4' THEN
        IF OLD.recipient_trust_profile_sha256 IS DISTINCT FROM NEW.recipient_trust_profile_sha256 OR
           OLD.recipient_trust_generation IS DISTINCT FROM NEW.recipient_trust_generation OR
           NEW.acknowledgement_contract <>
               'dataground.audit-export-delivery-receipt/ed25519/v3' OR
           NOT EXISTS (
               SELECT 1
               FROM audit_export_recipient_trust_events AS trust_event
               JOIN audit_export_recipient_trust_keys AS signing_key
                 ON signing_key.isolation_domain_id = trust_event.isolation_domain_id
                AND signing_key.recipient_id = trust_event.recipient_id
                AND signing_key.generation = trust_event.generation
               JOIN audit_export_recipient_encryption_keys AS encryption_key
                 ON encryption_key.isolation_domain_id = trust_event.isolation_domain_id
                AND encryption_key.recipient_id = trust_event.recipient_id
                AND encryption_key.generation = trust_event.generation
               WHERE trust_event.isolation_domain_id = NEW.isolation_domain_id
                 AND trust_event.recipient_id = NEW.recipient_id
                 AND trust_event.generation = NEW.recipient_trust_generation
                 AND trust_event.authorization_contract =
                     'dataground.audit-export-recipient-trust-authorization/v3'
                 AND trust_event.operation = 'activate'
                 AND trust_event.trust_profile_sha256 = NEW.recipient_trust_profile_sha256
                 AND isfinite(trust_event.identity_proof_expires_at)
                 AND trust_event.identity_proof_expires_at > clock_timestamp()
                 AND signing_key.key_id = NEW.recipient_signing_key_id
                 AND encryption_key.key_id = NEW.recipient_encryption_key_id
                 AND trust_event.generation = (
                     SELECT max(latest.generation) FROM audit_export_recipient_trust_events AS latest
                     WHERE latest.isolation_domain_id = NEW.isolation_domain_id
                       AND latest.recipient_id = NEW.recipient_id
                 )
                 AND NOT EXISTS (
                     SELECT 1 FROM audit_export_recipient_proof_revocations AS revocation
                     WHERE revocation.isolation_domain_id = trust_event.isolation_domain_id
                       AND revocation.proofing_authority_id = trust_event.proofing_authority_id
                       AND revocation.proofing_trust_profile_sha256 = trust_event.proofing_trust_profile_sha256
                       AND revocation.effective_at <= clock_timestamp()
                       AND (revocation.scope = 'profile' OR
                            (revocation.scope = 'key' AND
                             revocation.proofing_signing_key_id = trust_event.proofing_signing_key_id))
                 )
           ) THEN
            RAISE EXCEPTION 'encrypted audit export acknowledgement is not authorized';
        END IF;
    ELSIF NEW.contract = 'dataground.audit-export-delivery/v3' THEN
        IF NEW.acknowledgement_contract <>
               'dataground.audit-export-delivery-receipt/ed25519/v2' OR
           NEW.recipient_trust_generation IS NULL OR
           NOT EXISTS (
               SELECT 1
               FROM audit_export_recipient_trust_events AS trust_event
               JOIN audit_export_recipient_trust_keys AS trust_key
                 ON trust_key.isolation_domain_id = trust_event.isolation_domain_id
                AND trust_key.recipient_id = trust_event.recipient_id
                AND trust_key.generation = trust_event.generation
               WHERE trust_event.isolation_domain_id = NEW.isolation_domain_id
                 AND trust_event.recipient_id = NEW.recipient_id
                 AND trust_event.generation = NEW.recipient_trust_generation
                 AND trust_event.authorization_contract =
                     'dataground.audit-export-recipient-trust-authorization/v2'
                 AND trust_event.operation = 'activate'
                 AND trust_event.trust_profile_sha256 = NEW.recipient_trust_profile_sha256
                 AND isfinite(trust_event.identity_proof_expires_at)
                 AND trust_event.identity_proof_expires_at > clock_timestamp()
                 AND trust_key.key_id = NEW.recipient_signing_key_id
                 AND trust_event.generation = (
                     SELECT max(latest.generation)
                     FROM audit_export_recipient_trust_events AS latest
                     WHERE latest.isolation_domain_id = NEW.isolation_domain_id
                       AND latest.recipient_id = NEW.recipient_id
                 )
                 AND NOT EXISTS (
                     SELECT 1 FROM audit_export_recipient_proof_revocations AS revocation
                     WHERE revocation.isolation_domain_id = trust_event.isolation_domain_id
                       AND revocation.proofing_authority_id = trust_event.proofing_authority_id
                       AND revocation.proofing_trust_profile_sha256 = trust_event.proofing_trust_profile_sha256
                       AND revocation.effective_at <= clock_timestamp()
                       AND (revocation.scope = 'profile' OR
                            (revocation.scope = 'key' AND
                             revocation.proofing_signing_key_id = trust_event.proofing_signing_key_id))
                 )
           ) THEN
            RAISE EXCEPTION 'legacy audit export acknowledgement is not authorized';
        END IF;
    ELSE
        RAISE EXCEPTION 'audit export delivery contract cannot transition';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_deliveries_controlled_update
BEFORE UPDATE ON audit_export_deliveries
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transition();
