-- dataground:up

ALTER TABLE audit_export_revocation_authority_events
    ADD COLUMN creation_transaction_id bigint NOT NULL DEFAULT txid_current();

ALTER TABLE audit_export_revocation_authority_keys
    ADD COLUMN creation_transaction_id bigint NOT NULL DEFAULT txid_current();

CREATE OR REPLACE FUNCTION enforce_audit_export_revocation_authority_sequence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    latest_generation bigint;
    latest_operation text;
    latest_authority_id text;
    latest_profile_sha256 text;
    lock_namespace text;
BEGIN
    IF NEW.creation_transaction_id <> txid_current() THEN
        RAISE EXCEPTION 'audit export revocation authority transaction binding is invalid';
    ELSIF NEW.purpose = 'recipient-proof' THEN
        lock_namespace := 'audit-export-recipient-proof-revocation';
    ELSIF NEW.purpose = 'workload-identity' THEN
        lock_namespace := 'audit-export-workload-identity-revocation';
    ELSE
        RAISE EXCEPTION 'audit export revocation authority purpose is invalid';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(
        lock_namespace || E'\n' || NEW.isolation_domain_id, 0
    ));
    SELECT generation, operation, authority_id, trust_profile_sha256
    INTO latest_generation, latest_operation, latest_authority_id, latest_profile_sha256
    FROM audit_export_revocation_authority_events
    WHERE isolation_domain_id = NEW.isolation_domain_id
      AND purpose = NEW.purpose
    ORDER BY generation DESC
    LIMIT 1;
    IF NOT FOUND THEN
        IF NEW.generation <> 1 OR NEW.operation <> 'activate' THEN
            RAISE EXCEPTION 'audit export revocation authority must begin with generation 1 activation';
        END IF;
    ELSIF latest_generation = 9223372036854775807 OR
          NEW.generation <> latest_generation + 1 THEN
        RAISE EXCEPTION 'audit export revocation authority generations must be sequential';
    ELSIF NEW.operation = 'revoke' AND
          (latest_operation <> 'activate' OR NEW.authority_id <> latest_authority_id OR
           NEW.trust_profile_sha256 <> latest_profile_sha256) THEN
        RAISE EXCEPTION 'audit export revocation authority withdrawal must match the active profile';
    ELSIF NEW.operation = 'activate' AND latest_operation = 'activate' AND
          NEW.authority_id = latest_authority_id AND
          NEW.trust_profile_sha256 = latest_profile_sha256 THEN
        RAISE EXCEPTION 'audit export revocation authority rotation must change the active profile';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION enforce_audit_export_revocation_authority_key_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.creation_transaction_id <> txid_current() OR NOT EXISTS (
        SELECT 1
        FROM audit_export_revocation_authority_events AS authority_event
        WHERE authority_event.isolation_domain_id = NEW.isolation_domain_id
          AND authority_event.purpose = NEW.purpose
          AND authority_event.generation = NEW.generation
          AND authority_event.operation = 'activate'
          AND authority_event.creation_transaction_id = txid_current()
    ) THEN
        RAISE EXCEPTION 'audit export revocation authority keys require their activation transaction';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TABLE audit_export_proofing_authority_events (
    authorization_contract text NOT NULL,
    isolation_domain_id text NOT NULL,
    authority_id text NOT NULL,
    generation bigint NOT NULL,
    operation text NOT NULL,
    trust_contract text NOT NULL,
    trust_profile_sha256 text NOT NULL,
    actor_id text NOT NULL,
    reason_digest bytea NOT NULL,
    correlation_id text NOT NULL UNIQUE,
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
    PRIMARY KEY (isolation_domain_id, generation),
    CHECK (authorization_contract = 'dataground.audit-export-proofing-authority-authorization/v1'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (authority_id ~ '^[a-z][a-z0-9._-]{0,127}$'),
    CHECK (generation > 0),
    CHECK (operation IN ('activate', 'revoke')),
    CHECK (trust_contract = 'dataground.audit-export-recipient-proofing-trust/ed25519/v1'),
    CHECK (trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (actor_id <> '' AND length(actor_id) <= 256 AND actor_id !~ '[[:cntrl:]]'),
    CHECK (octet_length(reason_digest) = 32),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (isfinite(occurred_at))
);

CREATE TABLE audit_export_proofing_authority_keys (
    isolation_domain_id text NOT NULL,
    generation bigint NOT NULL,
    key_id text NOT NULL,
    creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
    PRIMARY KEY (isolation_domain_id, generation, key_id),
    FOREIGN KEY (isolation_domain_id, generation)
        REFERENCES audit_export_proofing_authority_events (isolation_domain_id, generation),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (generation > 0),
    CHECK (key_id ~ '^[a-z][a-z0-9_-]{2,63}$')
);

CREATE FUNCTION enforce_audit_export_proofing_authority_sequence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    latest_generation bigint;
    latest_operation text;
    latest_authority_id text;
    latest_profile_sha256 text;
BEGIN
    IF NEW.creation_transaction_id <> txid_current() THEN
        RAISE EXCEPTION 'audit export proofing authority transaction binding is invalid';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-proof-revocation' || E'\n' || NEW.isolation_domain_id,
        0
    ));
    SELECT generation, operation, authority_id, trust_profile_sha256
    INTO latest_generation, latest_operation, latest_authority_id, latest_profile_sha256
    FROM audit_export_proofing_authority_events
    WHERE isolation_domain_id = NEW.isolation_domain_id
    ORDER BY generation DESC
    LIMIT 1;
    IF NOT FOUND THEN
        IF NEW.generation <> 1 OR NEW.operation <> 'activate' THEN
            RAISE EXCEPTION 'audit export proofing authority must begin with generation 1 activation';
        END IF;
    ELSIF latest_generation = 9223372036854775807 OR
          NEW.generation <> latest_generation + 1 THEN
        RAISE EXCEPTION 'audit export proofing authority generations must be sequential';
    ELSIF NEW.operation = 'revoke' AND
          (latest_operation <> 'activate' OR NEW.authority_id <> latest_authority_id OR
           NEW.trust_profile_sha256 <> latest_profile_sha256) THEN
        RAISE EXCEPTION 'audit export proofing authority withdrawal must match the active profile';
    ELSIF NEW.operation = 'activate' AND latest_operation = 'activate' AND
          NEW.authority_id = latest_authority_id AND
          NEW.trust_profile_sha256 = latest_profile_sha256 THEN
        RAISE EXCEPTION 'audit export proofing authority rotation must change the active profile';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_proofing_authority_events_sequence
BEFORE INSERT ON audit_export_proofing_authority_events
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_proofing_authority_sequence();

CREATE FUNCTION enforce_audit_export_proofing_authority_key_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.creation_transaction_id <> txid_current() OR NOT EXISTS (
        SELECT 1
        FROM audit_export_proofing_authority_events AS authority_event
        WHERE authority_event.isolation_domain_id = NEW.isolation_domain_id
          AND authority_event.generation = NEW.generation
          AND authority_event.operation = 'activate'
          AND authority_event.creation_transaction_id = txid_current()
    ) THEN
        RAISE EXCEPTION 'audit export proofing authority keys require their activation transaction';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_proofing_authority_keys_binding
BEFORE INSERT ON audit_export_proofing_authority_keys
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_proofing_authority_key_binding();

CREATE FUNCTION enforce_audit_export_proofing_authority_key_count()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    key_count bigint;
BEGIN
    SELECT count(*)
    INTO key_count
    FROM audit_export_proofing_authority_keys
    WHERE isolation_domain_id = NEW.isolation_domain_id
      AND generation = NEW.generation;
    IF (NEW.operation = 'activate' AND (key_count < 1 OR key_count > 8)) OR
       (NEW.operation = 'revoke' AND key_count <> 0) THEN
        RAISE EXCEPTION 'audit export proofing authority event has an invalid key set';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER audit_export_proofing_authority_events_key_count
AFTER INSERT ON audit_export_proofing_authority_events
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_proofing_authority_key_count();

CREATE FUNCTION reject_audit_export_proofing_authority_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit export proofing authority evidence is append-only';
END;
$$;

CREATE TRIGGER audit_export_proofing_authority_events_append_only
BEFORE UPDATE OR DELETE ON audit_export_proofing_authority_events
FOR EACH ROW EXECUTE FUNCTION reject_audit_export_proofing_authority_mutation();

CREATE TRIGGER audit_export_proofing_authority_keys_append_only
BEFORE UPDATE OR DELETE ON audit_export_proofing_authority_keys
FOR EACH ROW EXECUTE FUNCTION reject_audit_export_proofing_authority_mutation();

CREATE FUNCTION audit_export_proofing_authority_is_active(
    requested_domain text,
    requested_authority text,
    requested_profile text,
    requested_key text
)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM audit_export_proofing_authority_events AS authority_event
        JOIN audit_export_proofing_authority_keys AS authority_key
          ON authority_key.isolation_domain_id = authority_event.isolation_domain_id
         AND authority_key.generation = authority_event.generation
        WHERE authority_event.isolation_domain_id = requested_domain
          AND authority_event.authority_id = requested_authority
          AND authority_event.operation = 'activate'
          AND authority_event.trust_profile_sha256 = requested_profile
          AND authority_key.key_id = requested_key
          AND authority_event.generation = (
              SELECT max(latest.generation)
              FROM audit_export_proofing_authority_events AS latest
              WHERE latest.isolation_domain_id = requested_domain
          )
    );
$$;

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
    ELSIF NEW.operation = 'activate' AND NOT audit_export_proofing_authority_is_active(
        NEW.isolation_domain_id, NEW.proofing_authority_id,
        NEW.proofing_trust_profile_sha256, NEW.proofing_signing_key_id
    ) THEN
        RAISE EXCEPTION 'audit export recipient proofing authority is not active';
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

-- dataground:down

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM audit_export_proofing_authority_events) THEN
        RAISE EXCEPTION 'schema 33 contains proofing authority evidence and cannot be downgraded safely';
    END IF;
END;
$$;

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

DROP FUNCTION audit_export_proofing_authority_is_active(text, text, text, text);
DROP TRIGGER audit_export_proofing_authority_keys_append_only
    ON audit_export_proofing_authority_keys;
DROP TRIGGER audit_export_proofing_authority_events_append_only
    ON audit_export_proofing_authority_events;
DROP FUNCTION reject_audit_export_proofing_authority_mutation();
DROP TRIGGER audit_export_proofing_authority_events_key_count
    ON audit_export_proofing_authority_events;
DROP FUNCTION enforce_audit_export_proofing_authority_key_count();
DROP TRIGGER audit_export_proofing_authority_keys_binding
    ON audit_export_proofing_authority_keys;
DROP FUNCTION enforce_audit_export_proofing_authority_key_binding();
DROP TRIGGER audit_export_proofing_authority_events_sequence
    ON audit_export_proofing_authority_events;
DROP FUNCTION enforce_audit_export_proofing_authority_sequence();
DROP TABLE audit_export_proofing_authority_keys;
DROP TABLE audit_export_proofing_authority_events;

CREATE OR REPLACE FUNCTION enforce_audit_export_revocation_authority_sequence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    latest_generation bigint;
    latest_operation text;
    latest_authority_id text;
    latest_profile_sha256 text;
    lock_namespace text;
BEGIN
    IF NEW.purpose = 'recipient-proof' THEN
        lock_namespace := 'audit-export-recipient-proof-revocation';
    ELSIF NEW.purpose = 'workload-identity' THEN
        lock_namespace := 'audit-export-workload-identity-revocation';
    ELSE
        RAISE EXCEPTION 'audit export revocation authority purpose is invalid';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(
        lock_namespace || E'\n' || NEW.isolation_domain_id, 0
    ));
    SELECT generation, operation, authority_id, trust_profile_sha256
    INTO latest_generation, latest_operation, latest_authority_id, latest_profile_sha256
    FROM audit_export_revocation_authority_events
    WHERE isolation_domain_id = NEW.isolation_domain_id
      AND purpose = NEW.purpose
    ORDER BY generation DESC
    LIMIT 1;
    IF NOT FOUND THEN
        IF NEW.generation <> 1 OR NEW.operation <> 'activate' THEN
            RAISE EXCEPTION 'audit export revocation authority must begin with generation 1 activation';
        END IF;
    ELSIF latest_generation = 9223372036854775807 OR
          NEW.generation <> latest_generation + 1 THEN
        RAISE EXCEPTION 'audit export revocation authority generations must be sequential';
    ELSIF NEW.operation = 'revoke' AND
          (latest_operation <> 'activate' OR NEW.authority_id <> latest_authority_id OR
           NEW.trust_profile_sha256 <> latest_profile_sha256) THEN
        RAISE EXCEPTION 'audit export revocation authority withdrawal must match the active profile';
    ELSIF NEW.operation = 'activate' AND latest_operation = 'activate' AND
          NEW.authority_id = latest_authority_id AND
          NEW.trust_profile_sha256 = latest_profile_sha256 THEN
        RAISE EXCEPTION 'audit export revocation authority rotation must change the active profile';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION enforce_audit_export_revocation_authority_key_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM audit_export_revocation_authority_events AS authority_event
        WHERE authority_event.isolation_domain_id = NEW.isolation_domain_id
          AND authority_event.purpose = NEW.purpose
          AND authority_event.generation = NEW.generation
          AND authority_event.operation = 'activate'
    ) THEN
        RAISE EXCEPTION 'audit export revocation authority keys require an activation event';
    END IF;
    RETURN NEW;
END;
$$;

ALTER TABLE audit_export_revocation_authority_keys
    DROP COLUMN creation_transaction_id;

ALTER TABLE audit_export_revocation_authority_events
    DROP COLUMN creation_transaction_id;
