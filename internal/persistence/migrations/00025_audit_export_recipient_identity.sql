-- dataground:up

DROP TRIGGER audit_export_deliveries_controlled_update ON audit_export_deliveries;
DROP TRIGGER audit_export_recipient_trust_events_sequence ON audit_export_recipient_trust_events;

ALTER TABLE audit_export_recipient_trust_events
    ADD COLUMN authorization_contract text NOT NULL
        DEFAULT 'dataground.audit-export-recipient-trust-authorization/v1',
    ADD COLUMN identity_proof_contract text,
    ADD COLUMN identity_proof_sha256 text,
    ADD COLUMN identity_proof_evidence_sha256 text,
    ADD COLUMN proofing_authority_id text,
    ADD COLUMN proofing_trust_profile_sha256 text,
    ADD COLUMN proofing_signing_key_id text,
    ADD COLUMN identity_proof_verified_at timestamptz,
    ADD COLUMN identity_proof_expires_at timestamptz;

ALTER TABLE audit_export_recipient_trust_events
    ALTER COLUMN authorization_contract DROP DEFAULT,
    ADD CONSTRAINT audit_export_recipient_trust_authorization_contract_check
        CHECK (authorization_contract IN (
            'dataground.audit-export-recipient-trust-authorization/v1',
            'dataground.audit-export-recipient-trust-authorization/v2'
        )),
    ADD CONSTRAINT audit_export_recipient_trust_identity_proof_check
        CHECK (
            (operation = 'revoke' AND
             identity_proof_contract IS NULL AND identity_proof_sha256 IS NULL AND
             identity_proof_evidence_sha256 IS NULL AND proofing_authority_id IS NULL AND
             proofing_trust_profile_sha256 IS NULL AND proofing_signing_key_id IS NULL AND
             identity_proof_verified_at IS NULL AND identity_proof_expires_at IS NULL) OR
            (operation = 'activate' AND
             authorization_contract = 'dataground.audit-export-recipient-trust-authorization/v1' AND
             identity_proof_contract IS NULL AND identity_proof_sha256 IS NULL AND
             identity_proof_evidence_sha256 IS NULL AND proofing_authority_id IS NULL AND
             proofing_trust_profile_sha256 IS NULL AND proofing_signing_key_id IS NULL AND
             identity_proof_verified_at IS NULL AND identity_proof_expires_at IS NULL) OR
            (operation = 'activate' AND
             authorization_contract = 'dataground.audit-export-recipient-trust-authorization/v2' AND
             identity_proof_contract = 'dataground.audit-export-recipient-identity-proof/ed25519/v1' AND
             identity_proof_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             identity_proof_evidence_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             proofing_authority_id ~ '^[a-z][a-z0-9._-]{0,127}$' AND
             proofing_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             proofing_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             identity_proof_verified_at IS NOT NULL AND identity_proof_expires_at IS NOT NULL AND
             identity_proof_expires_at > identity_proof_verified_at)
        );

CREATE OR REPLACE FUNCTION enforce_audit_export_recipient_trust_sequence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    latest_generation bigint;
    latest_authorization_contract text;
    latest_operation text;
    latest_profile_sha256 text;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id,
        0
    ));
    IF NEW.authorization_contract <> 'dataground.audit-export-recipient-trust-authorization/v2' THEN
        RAISE EXCEPTION 'new audit export recipient trust evidence requires recipient identity proof';
    ELSIF NEW.operation = 'activate' AND
          (NEW.identity_proof_verified_at > clock_timestamp() + interval '5 minutes' OR
           NEW.identity_proof_expires_at <= clock_timestamp()) THEN
        RAISE EXCEPTION 'audit export recipient identity proof is outside its validity interval';
    END IF;
    SELECT generation, authorization_contract, operation, trust_profile_sha256
    INTO latest_generation, latest_authorization_contract, latest_operation, latest_profile_sha256
    FROM audit_export_recipient_trust_events
    WHERE isolation_domain_id = NEW.isolation_domain_id
      AND recipient_id = NEW.recipient_id
    ORDER BY generation DESC
    LIMIT 1;

    IF NOT FOUND THEN
        IF NEW.generation <> 1 OR NEW.operation <> 'activate' THEN
            RAISE EXCEPTION 'audit export recipient trust must begin with generation 1 activation';
        END IF;
    ELSIF latest_generation = 9223372036854775807 OR NEW.generation <> latest_generation + 1 THEN
        RAISE EXCEPTION 'audit export recipient trust generations must be sequential';
    ELSIF NEW.operation = 'revoke' AND
          (latest_operation <> 'activate' OR NEW.trust_profile_sha256 <> latest_profile_sha256) THEN
        RAISE EXCEPTION 'audit export recipient trust revocation must match the active profile';
    ELSIF NEW.operation = 'activate' AND latest_operation = 'activate' AND
          NEW.trust_profile_sha256 = latest_profile_sha256 AND
          latest_authorization_contract <> 'dataground.audit-export-recipient-trust-authorization/v1' THEN
        RAISE EXCEPTION 'audit export recipient trust activation must change the active profile';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_recipient_trust_events_sequence
BEFORE INSERT ON audit_export_recipient_trust_events
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_recipient_trust_sequence();

CREATE OR REPLACE FUNCTION enforce_audit_export_delivery_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id,
        0
    ));
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
       NEW.contract <> 'dataground.audit-export-delivery/v3' OR
       NEW.acknowledgement_digest IS NULL OR
       NEW.acknowledged_at IS NULL OR
       NEW.recipient_trust_generation IS NULL OR
       NOT EXISTS (
           SELECT 1
           FROM audit_export_delivery_operations AS operation
           WHERE operation.delivery_id = NEW.delivery_id
             AND operation.isolation_domain_id = NEW.isolation_domain_id
             AND operation.operation = 'acknowledge'
             AND operation.evidence_digest = NEW.acknowledgement_digest
       ) OR
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
             AND trust_event.authorization_contract = 'dataground.audit-export-recipient-trust-authorization/v2'
             AND trust_event.operation = 'activate'
             AND trust_event.trust_profile_sha256 = NEW.recipient_trust_profile_sha256
             AND trust_event.identity_proof_expires_at > clock_timestamp()
             AND trust_key.key_id = NEW.recipient_signing_key_id
             AND trust_event.generation = (
                 SELECT max(latest.generation)
                 FROM audit_export_recipient_trust_events AS latest
                 WHERE latest.isolation_domain_id = NEW.isolation_domain_id
                   AND latest.recipient_id = NEW.recipient_id
             )
       ) THEN
        RAISE EXCEPTION 'audit export deliveries permit only identity-proven authorized acknowledgements';
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
        SELECT 1
        FROM audit_export_recipient_trust_events
        WHERE authorization_contract = 'dataground.audit-export-recipient-trust-authorization/v2'
    ) THEN
        RAISE EXCEPTION 'schema 25 contains recipient identity proof evidence and cannot be downgraded safely';
    END IF;
END;
$$;

DROP TRIGGER audit_export_deliveries_controlled_update ON audit_export_deliveries;
DROP TRIGGER audit_export_recipient_trust_events_sequence ON audit_export_recipient_trust_events;

ALTER TABLE audit_export_recipient_trust_events
    DROP CONSTRAINT audit_export_recipient_trust_identity_proof_check,
    DROP CONSTRAINT audit_export_recipient_trust_authorization_contract_check,
    DROP COLUMN identity_proof_expires_at,
    DROP COLUMN identity_proof_verified_at,
    DROP COLUMN proofing_signing_key_id,
    DROP COLUMN proofing_trust_profile_sha256,
    DROP COLUMN proofing_authority_id,
    DROP COLUMN identity_proof_evidence_sha256,
    DROP COLUMN identity_proof_sha256,
    DROP COLUMN identity_proof_contract,
    DROP COLUMN authorization_contract;

CREATE OR REPLACE FUNCTION enforce_audit_export_recipient_trust_sequence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    latest_generation bigint;
    latest_operation text;
    latest_profile_sha256 text;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id,
        0
    ));
    SELECT generation, operation, trust_profile_sha256
    INTO latest_generation, latest_operation, latest_profile_sha256
    FROM audit_export_recipient_trust_events
    WHERE isolation_domain_id = NEW.isolation_domain_id
      AND recipient_id = NEW.recipient_id
    ORDER BY generation DESC
    LIMIT 1;

    IF NOT FOUND THEN
        IF NEW.generation <> 1 OR NEW.operation <> 'activate' THEN
            RAISE EXCEPTION 'audit export recipient trust must begin with generation 1 activation';
        END IF;
    ELSIF latest_generation = 9223372036854775807 OR NEW.generation <> latest_generation + 1 THEN
        RAISE EXCEPTION 'audit export recipient trust generations must be sequential';
    ELSIF NEW.operation = 'revoke' AND
          (latest_operation <> 'activate' OR NEW.trust_profile_sha256 <> latest_profile_sha256) THEN
        RAISE EXCEPTION 'audit export recipient trust revocation must match the active profile';
    ELSIF NEW.operation = 'activate' AND latest_operation = 'activate' AND
          NEW.trust_profile_sha256 = latest_profile_sha256 THEN
        RAISE EXCEPTION 'audit export recipient trust activation must change the active profile';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_recipient_trust_events_sequence
BEFORE INSERT ON audit_export_recipient_trust_events
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_recipient_trust_sequence();

CREATE OR REPLACE FUNCTION enforce_audit_export_delivery_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id,
        0
    ));
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
       NEW.contract <> 'dataground.audit-export-delivery/v3' OR
       NEW.acknowledgement_digest IS NULL OR
       NEW.acknowledged_at IS NULL OR
       NEW.recipient_trust_generation IS NULL OR
       NOT EXISTS (
           SELECT 1
           FROM audit_export_delivery_operations AS operation
           WHERE operation.delivery_id = NEW.delivery_id
             AND operation.isolation_domain_id = NEW.isolation_domain_id
             AND operation.operation = 'acknowledge'
             AND operation.evidence_digest = NEW.acknowledgement_digest
       ) OR
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
             AND trust_event.operation = 'activate'
             AND trust_event.trust_profile_sha256 = NEW.recipient_trust_profile_sha256
             AND trust_key.key_id = NEW.recipient_signing_key_id
             AND trust_event.generation = (
                 SELECT max(latest.generation)
                 FROM audit_export_recipient_trust_events AS latest
                 WHERE latest.isolation_domain_id = NEW.isolation_domain_id
                   AND latest.recipient_id = NEW.recipient_id
             )
       ) THEN
        RAISE EXCEPTION 'audit export deliveries permit only authorized operation-bound acknowledgements';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_deliveries_controlled_update
BEFORE UPDATE ON audit_export_deliveries
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transition();
