-- dataground:up

CREATE TABLE audit_export_recipient_proof_revocations (
    record_contract text NOT NULL,
    revocation_contract text NOT NULL,
    revocation_sha256 text PRIMARY KEY,
    isolation_domain_id text NOT NULL,
    scope text NOT NULL,
    proofing_authority_id text NOT NULL,
    proofing_trust_profile_sha256 text NOT NULL,
    proofing_signing_key_id text,
    external_reason_sha256 text NOT NULL,
    revocation_authority_id text NOT NULL,
    revocation_trust_profile_sha256 text NOT NULL,
    revocation_signing_key_id text NOT NULL,
    issued_at timestamptz NOT NULL,
    effective_at timestamptz NOT NULL,
    actor_id text NOT NULL,
    reason_digest bytea NOT NULL,
    correlation_id text NOT NULL UNIQUE,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (record_contract = 'dataground.audit-export-recipient-proof-revocation-record/v1'),
    CHECK (revocation_contract = 'dataground.audit-export-recipient-proof-revocation/ed25519/v1'),
    CHECK (revocation_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (scope IN ('profile', 'key')),
    CHECK (proofing_authority_id ~ '^[a-z][a-z0-9._-]{0,127}$'),
    CHECK (proofing_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (
        (scope = 'profile' AND proofing_signing_key_id IS NULL) OR
        (scope = 'key' AND proofing_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$')
    ),
    CHECK (external_reason_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (revocation_authority_id ~ '^[a-z][a-z0-9._-]{0,127}$'),
    CHECK (revocation_authority_id <> proofing_authority_id),
    CHECK (revocation_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (revocation_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$'),
    CHECK (issued_at > '-infinity'::timestamptz AND issued_at < 'infinity'::timestamptz),
    CHECK (effective_at > '-infinity'::timestamptz AND effective_at < 'infinity'::timestamptz),
    CHECK (date_trunc('microseconds', issued_at) = issued_at),
    CHECK (date_trunc('microseconds', effective_at) = effective_at),
    CHECK (actor_id <> '' AND length(actor_id) <= 256 AND actor_id !~ '[[:cntrl:]]'),
    CHECK (octet_length(reason_digest) = 32),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$')
);

CREATE INDEX audit_export_recipient_proof_revocations_profile_match
ON audit_export_recipient_proof_revocations (
    isolation_domain_id, proofing_authority_id, proofing_trust_profile_sha256, effective_at
)
WHERE scope = 'profile';

CREATE INDEX audit_export_recipient_proof_revocations_key_match
ON audit_export_recipient_proof_revocations (
    isolation_domain_id, proofing_authority_id, proofing_trust_profile_sha256,
    proofing_signing_key_id, effective_at
)
WHERE scope = 'key';

CREATE FUNCTION enforce_audit_export_recipient_proof_revocation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-proof-revocation' || E'\n' || NEW.isolation_domain_id,
        0
    ));
    IF NEW.issued_at > clock_timestamp() + interval '5 minutes' THEN
        RAISE EXCEPTION 'audit export recipient proof revocation is issued in the future';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_recipient_proof_revocations_controlled_insert
BEFORE INSERT ON audit_export_recipient_proof_revocations
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_recipient_proof_revocation();

CREATE FUNCTION reject_audit_export_recipient_proof_revocation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit export recipient proof revocations are append-only';
END;
$$;

CREATE TRIGGER audit_export_recipient_proof_revocations_append_only
BEFORE UPDATE OR DELETE ON audit_export_recipient_proof_revocations
FOR EACH ROW EXECUTE FUNCTION reject_audit_export_recipient_proof_revocation_mutation();

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
        RAISE EXCEPTION 'audit export deliveries permit only unrevoked identity-proven authorized acknowledgements';
    END IF;
    RETURN NEW;
END;
$$;

-- dataground:down

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM audit_export_recipient_proof_revocations) THEN
        RAISE EXCEPTION 'schema 26 contains recipient proof revocation evidence and cannot be downgraded safely';
    END IF;
END;
$$;

DROP TRIGGER audit_export_recipient_proof_revocations_append_only
    ON audit_export_recipient_proof_revocations;
DROP TRIGGER audit_export_recipient_proof_revocations_controlled_insert
    ON audit_export_recipient_proof_revocations;
DROP FUNCTION reject_audit_export_recipient_proof_revocation_mutation();
DROP FUNCTION enforce_audit_export_recipient_proof_revocation();
DROP TABLE audit_export_recipient_proof_revocations;

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
