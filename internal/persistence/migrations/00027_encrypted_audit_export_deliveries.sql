-- dataground:up

DROP TRIGGER audit_export_deliveries_controlled_update ON audit_export_deliveries;
DROP TRIGGER audit_export_recipient_trust_events_key_count ON audit_export_recipient_trust_events;
DROP TRIGGER audit_export_recipient_trust_events_sequence ON audit_export_recipient_trust_events;

ALTER TABLE audit_export_recipient_trust_events
    DROP CONSTRAINT audit_export_recipient_trust_authorization_contract_check,
    DROP CONSTRAINT audit_export_recipient_trust_identity_proof_check,
    DROP CONSTRAINT audit_export_recipient_trust_events_trust_contract_check,
    ADD CONSTRAINT audit_export_recipient_trust_authorization_contract_check
        CHECK (authorization_contract IN (
            'dataground.audit-export-recipient-trust-authorization/v1',
            'dataground.audit-export-recipient-trust-authorization/v2',
            'dataground.audit-export-recipient-trust-authorization/v3'
        )),
    ADD CONSTRAINT audit_export_recipient_trust_events_trust_contract_check
        CHECK (trust_contract IN (
            'dataground.audit-export-recipient-trust/ed25519/v1',
            'dataground.audit-export-recipient-trust/ed25519-x25519/v2'
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
            (operation = 'activate' AND authorization_contract IN (
                 'dataground.audit-export-recipient-trust-authorization/v2',
                 'dataground.audit-export-recipient-trust-authorization/v3'
             ) AND
             identity_proof_contract =
                 'dataground.audit-export-recipient-identity-proof/ed25519/v1' AND
             identity_proof_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             identity_proof_evidence_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             proofing_authority_id ~ '^[a-z][a-z0-9._-]{0,127}$' AND
             proofing_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             proofing_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             identity_proof_verified_at IS NOT NULL AND identity_proof_expires_at IS NOT NULL AND
             isfinite(identity_proof_verified_at) AND isfinite(identity_proof_expires_at) AND
             identity_proof_expires_at > identity_proof_verified_at)
        );

CREATE TABLE audit_export_recipient_encryption_keys (
    isolation_domain_id text NOT NULL,
    recipient_id text NOT NULL,
    generation bigint NOT NULL,
    key_id text NOT NULL,
    PRIMARY KEY (isolation_domain_id, recipient_id, generation, key_id),
    FOREIGN KEY (isolation_domain_id, recipient_id, generation)
        REFERENCES audit_export_recipient_trust_events (isolation_domain_id, recipient_id, generation),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (recipient_id ~ '^[a-z][a-z0-9._-]{0,127}$'),
    CHECK (generation > 0),
    CHECK (key_id ~ '^[a-z][a-z0-9_-]{2,63}$')
);

CREATE FUNCTION enforce_audit_export_recipient_encryption_key_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM audit_export_recipient_trust_events AS trust_event
        WHERE trust_event.isolation_domain_id = NEW.isolation_domain_id
          AND trust_event.recipient_id = NEW.recipient_id
          AND trust_event.generation = NEW.generation
          AND trust_event.operation = 'activate'
          AND trust_event.authorization_contract =
              'dataground.audit-export-recipient-trust-authorization/v3'
          AND trust_event.trust_contract =
              'dataground.audit-export-recipient-trust/ed25519-x25519/v2'
    ) THEN
        RAISE EXCEPTION 'audit export recipient encryption keys require a v3 activation event';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_recipient_encryption_keys_binding
BEFORE INSERT ON audit_export_recipient_encryption_keys
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_recipient_encryption_key_binding();

CREATE TRIGGER audit_export_recipient_encryption_keys_append_only
BEFORE UPDATE OR DELETE ON audit_export_recipient_encryption_keys
FOR EACH ROW EXECUTE FUNCTION reject_audit_export_recipient_trust_mutation();

CREATE OR REPLACE FUNCTION enforce_audit_export_recipient_trust_key_count()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    signing_key_count bigint;
    encryption_key_count bigint;
BEGIN
    SELECT count(*) INTO signing_key_count
    FROM audit_export_recipient_trust_keys
    WHERE isolation_domain_id = NEW.isolation_domain_id
      AND recipient_id = NEW.recipient_id
      AND generation = NEW.generation;
    SELECT count(*) INTO encryption_key_count
    FROM audit_export_recipient_encryption_keys
    WHERE isolation_domain_id = NEW.isolation_domain_id
      AND recipient_id = NEW.recipient_id
      AND generation = NEW.generation;
    IF NEW.operation = 'revoke' AND
       (signing_key_count <> 0 OR encryption_key_count <> 0) THEN
        RAISE EXCEPTION 'audit export recipient trust revocation must not contain keys';
    ELSIF NEW.operation = 'activate' AND
          (signing_key_count < 1 OR signing_key_count > 8) THEN
        RAISE EXCEPTION 'audit export recipient trust activation has an invalid signing key set';
    ELSIF NEW.operation = 'activate' AND
          NEW.authorization_contract = 'dataground.audit-export-recipient-trust-authorization/v3' AND
          (encryption_key_count < 1 OR encryption_key_count > 8) THEN
        RAISE EXCEPTION 'audit export recipient trust activation has an invalid encryption key set';
    ELSIF NEW.operation = 'activate' AND
          NEW.authorization_contract <> 'dataground.audit-export-recipient-trust-authorization/v3' AND
          encryption_key_count <> 0 THEN
        RAISE EXCEPTION 'legacy audit export recipient trust cannot contain encryption keys';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER audit_export_recipient_trust_events_key_count
AFTER INSERT ON audit_export_recipient_trust_events
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_recipient_trust_key_count();

CREATE OR REPLACE FUNCTION enforce_audit_export_recipient_trust_sequence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    latest_generation bigint;
    latest_authorization_contract text;
    latest_operation text;
    latest_trust_contract text;
    latest_profile_sha256 text;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-proof-revocation' || E'\n' || NEW.isolation_domain_id,
        0
    ));
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id,
        0
    ));
    IF NEW.operation = 'activate' AND
       (NOT isfinite(NEW.identity_proof_verified_at) OR
        NOT isfinite(NEW.identity_proof_expires_at) OR
        NEW.authorization_contract <>
            'dataground.audit-export-recipient-trust-authorization/v3' OR
        NEW.trust_contract <>
            'dataground.audit-export-recipient-trust/ed25519-x25519/v2') THEN
        RAISE EXCEPTION 'new audit export recipient trust activation requires encryption-capable evidence';
    ELSIF NEW.operation = 'activate' AND
          (NEW.identity_proof_verified_at > clock_timestamp() + interval '5 minutes' OR
           NEW.identity_proof_expires_at <= clock_timestamp()) THEN
        RAISE EXCEPTION 'audit export recipient identity proof is outside its validity interval';
    ELSIF NEW.operation = 'activate' AND EXISTS (
        SELECT 1
        FROM audit_export_recipient_proof_revocations AS revocation
        WHERE revocation.isolation_domain_id = NEW.isolation_domain_id
          AND revocation.proofing_authority_id = NEW.proofing_authority_id
          AND revocation.proofing_trust_profile_sha256 = NEW.proofing_trust_profile_sha256
          AND revocation.effective_at <= clock_timestamp()
          AND (
              revocation.scope = 'profile' OR
              (revocation.scope = 'key' AND
               revocation.proofing_signing_key_id = NEW.proofing_signing_key_id)
          )
    ) THEN
        RAISE EXCEPTION 'audit export recipient identity proof has been externally revoked';
    END IF;
    SELECT generation, authorization_contract, operation, trust_contract, trust_profile_sha256
    INTO latest_generation, latest_authorization_contract, latest_operation,
         latest_trust_contract, latest_profile_sha256
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
          (latest_operation <> 'activate' OR
           NEW.authorization_contract <> latest_authorization_contract OR
           NEW.trust_contract <> latest_trust_contract OR
           NEW.trust_profile_sha256 <> latest_profile_sha256) THEN
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

ALTER TABLE audit_export_deliveries
    DROP CONSTRAINT audit_export_deliveries_verification_check,
    DROP CONSTRAINT audit_export_deliveries_contract_check,
    ADD COLUMN encrypted_package_digest bytea,
    ADD COLUMN recipient_encryption_key_id text;

ALTER TABLE audit_export_deliveries
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
        'audit-export-recipient-proof-revocation' || E'\n' || NEW.isolation_domain_id,
        0
    ));
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id,
        0
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
              SELECT max(latest.generation)
              FROM audit_export_recipient_trust_events AS latest
              WHERE latest.isolation_domain_id = NEW.isolation_domain_id
                AND latest.recipient_id = NEW.recipient_id
          )
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
        'audit-export-recipient-proof-revocation' || E'\n' || NEW.isolation_domain_id,
        0
    ));
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id,
        0
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
                 AND trust_event.trust_contract =
                     'dataground.audit-export-recipient-trust/ed25519-x25519/v2'
                 AND trust_event.trust_profile_sha256 = NEW.recipient_trust_profile_sha256
                 AND isfinite(trust_event.identity_proof_expires_at)
                 AND trust_event.identity_proof_expires_at > clock_timestamp()
                 AND signing_key.key_id = NEW.recipient_signing_key_id
                 AND encryption_key.key_id = NEW.recipient_encryption_key_id
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
        SELECT 1 FROM audit_export_recipient_trust_events
        WHERE authorization_contract = 'dataground.audit-export-recipient-trust-authorization/v3'
    ) OR EXISTS (SELECT 1 FROM audit_export_recipient_encryption_keys) OR EXISTS (
        SELECT 1 FROM audit_export_deliveries
        WHERE contract = 'dataground.audit-export-delivery/v4'
    ) THEN
        RAISE EXCEPTION 'schema 27 contains encrypted delivery evidence and cannot be downgraded safely';
    END IF;
END;
$$;

DROP TRIGGER audit_export_deliveries_controlled_update ON audit_export_deliveries;
DROP TRIGGER audit_export_deliveries_controlled_insert ON audit_export_deliveries;
DROP FUNCTION enforce_audit_export_encrypted_delivery_prepare();
DROP TRIGGER audit_export_recipient_trust_events_sequence ON audit_export_recipient_trust_events;
DROP TRIGGER audit_export_recipient_trust_events_key_count ON audit_export_recipient_trust_events;
DROP TRIGGER audit_export_recipient_encryption_keys_append_only
    ON audit_export_recipient_encryption_keys;
DROP TRIGGER audit_export_recipient_encryption_keys_binding
    ON audit_export_recipient_encryption_keys;
DROP FUNCTION enforce_audit_export_recipient_encryption_key_binding();
DROP TABLE audit_export_recipient_encryption_keys;

ALTER TABLE audit_export_deliveries
    DROP CONSTRAINT audit_export_deliveries_verification_check,
    DROP CONSTRAINT audit_export_deliveries_contract_check,
    DROP COLUMN recipient_encryption_key_id,
    DROP COLUMN encrypted_package_digest,
    ADD CONSTRAINT audit_export_deliveries_contract_check
        CHECK (contract IN (
            'dataground.audit-export-delivery/v1',
            'dataground.audit-export-delivery/v2',
            'dataground.audit-export-delivery/v3'
        )),
    ADD CONSTRAINT audit_export_deliveries_verification_check
        CHECK (
            (contract = 'dataground.audit-export-delivery/v1' AND status = 'acknowledged' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v2' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v1' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v3' AND status = 'prepared' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v3' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v2' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation > 0)
        );

ALTER TABLE audit_export_recipient_trust_events
    DROP CONSTRAINT audit_export_recipient_trust_authorization_contract_check,
    DROP CONSTRAINT audit_export_recipient_trust_identity_proof_check,
    DROP CONSTRAINT audit_export_recipient_trust_events_trust_contract_check,
    ADD CONSTRAINT audit_export_recipient_trust_authorization_contract_check
        CHECK (authorization_contract IN (
            'dataground.audit-export-recipient-trust-authorization/v1',
            'dataground.audit-export-recipient-trust-authorization/v2'
        )),
    ADD CONSTRAINT audit_export_recipient_trust_events_trust_contract_check
        CHECK (trust_contract = 'dataground.audit-export-recipient-trust/ed25519/v1'),
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
             identity_proof_contract =
                 'dataground.audit-export-recipient-identity-proof/ed25519/v1' AND
             identity_proof_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             identity_proof_evidence_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             proofing_authority_id ~ '^[a-z][a-z0-9._-]{0,127}$' AND
             proofing_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             proofing_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             identity_proof_verified_at IS NOT NULL AND identity_proof_expires_at IS NOT NULL AND
             identity_proof_expires_at > identity_proof_verified_at)
        );

CREATE OR REPLACE FUNCTION enforce_audit_export_recipient_trust_key_count()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    key_count bigint;
BEGIN
    SELECT count(*) INTO key_count
    FROM audit_export_recipient_trust_keys
    WHERE isolation_domain_id = NEW.isolation_domain_id
      AND recipient_id = NEW.recipient_id
      AND generation = NEW.generation;
    IF (NEW.operation = 'activate' AND (key_count < 1 OR key_count > 8)) OR
       (NEW.operation = 'revoke' AND key_count <> 0) THEN
        RAISE EXCEPTION 'audit export recipient trust event has an invalid key set';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER audit_export_recipient_trust_events_key_count
AFTER INSERT ON audit_export_recipient_trust_events
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_recipient_trust_key_count();

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
        'audit-export-recipient-proof-revocation' || E'\n' || NEW.isolation_domain_id,
        0
    ));
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
    ELSIF NEW.operation = 'activate' AND EXISTS (
        SELECT 1 FROM audit_export_recipient_proof_revocations AS revocation
        WHERE revocation.isolation_domain_id = NEW.isolation_domain_id
          AND revocation.proofing_authority_id = NEW.proofing_authority_id
          AND revocation.proofing_trust_profile_sha256 = NEW.proofing_trust_profile_sha256
          AND revocation.effective_at <= clock_timestamp()
          AND (revocation.scope = 'profile' OR
               (revocation.scope = 'key' AND
                revocation.proofing_signing_key_id = NEW.proofing_signing_key_id))
    ) THEN
        RAISE EXCEPTION 'audit export recipient identity proof has been externally revoked';
    END IF;
    SELECT generation, authorization_contract, operation, trust_profile_sha256
    INTO latest_generation, latest_authorization_contract, latest_operation, latest_profile_sha256
    FROM audit_export_recipient_trust_events
    WHERE isolation_domain_id = NEW.isolation_domain_id AND recipient_id = NEW.recipient_id
    ORDER BY generation DESC LIMIT 1;
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
       NEW.contract <> 'dataground.audit-export-delivery/v3' OR
       NEW.acknowledgement_digest IS NULL OR NEW.acknowledged_at IS NULL OR
       NEW.recipient_trust_generation IS NULL OR
       NOT EXISTS (
           SELECT 1 FROM audit_export_delivery_operations AS operation
           WHERE operation.delivery_id = NEW.delivery_id
             AND operation.isolation_domain_id = NEW.isolation_domain_id
             AND operation.operation = 'acknowledge'
             AND operation.evidence_digest = NEW.acknowledgement_digest
       ) OR NOT EXISTS (
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
             AND trust_event.identity_proof_expires_at > clock_timestamp()
             AND trust_key.key_id = NEW.recipient_signing_key_id
             AND trust_event.generation = (
                 SELECT max(latest.generation) FROM audit_export_recipient_trust_events AS latest
                 WHERE latest.isolation_domain_id = NEW.isolation_domain_id
                   AND latest.recipient_id = NEW.recipient_id
             ) AND NOT EXISTS (
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
        RAISE EXCEPTION 'audit export deliveries permit only unrevoked identity-proven authorized acknowledgements';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_deliveries_controlled_update
BEFORE UPDATE ON audit_export_deliveries
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transition();
