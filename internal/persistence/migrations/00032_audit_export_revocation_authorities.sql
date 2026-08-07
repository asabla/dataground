-- dataground:up

CREATE TABLE audit_export_revocation_authority_events (
    authorization_contract text NOT NULL,
    isolation_domain_id text NOT NULL,
    purpose text NOT NULL,
    authority_id text NOT NULL,
    generation bigint NOT NULL,
    operation text NOT NULL,
    trust_contract text NOT NULL,
    trust_profile_sha256 text NOT NULL,
    actor_id text NOT NULL,
    reason_digest bytea NOT NULL,
    correlation_id text NOT NULL UNIQUE,
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (isolation_domain_id, purpose, generation),
    CHECK (authorization_contract = 'dataground.audit-export-revocation-authority-authorization/v1'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (purpose IN ('recipient-proof', 'workload-identity')),
    CHECK (authority_id ~ '^[a-z][a-z0-9._-]{0,127}$'),
    CHECK (generation > 0),
    CHECK (operation IN ('activate', 'revoke')),
    CHECK (
        (purpose = 'recipient-proof' AND
         trust_contract = 'dataground.audit-export-recipient-revocation-trust/ed25519/v1') OR
        (purpose = 'workload-identity' AND
         trust_contract = 'dataground.audit-export-workload-identity-revocation-trust/ed25519/v1')
    ),
    CHECK (trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (actor_id <> '' AND length(actor_id) <= 256 AND actor_id !~ '[[:cntrl:]]'),
    CHECK (octet_length(reason_digest) = 32),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (isfinite(occurred_at))
);

CREATE TABLE audit_export_revocation_authority_keys (
    isolation_domain_id text NOT NULL,
    purpose text NOT NULL,
    generation bigint NOT NULL,
    key_id text NOT NULL,
    PRIMARY KEY (isolation_domain_id, purpose, generation, key_id),
    FOREIGN KEY (isolation_domain_id, purpose, generation)
        REFERENCES audit_export_revocation_authority_events (
            isolation_domain_id, purpose, generation
        ),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (purpose IN ('recipient-proof', 'workload-identity')),
    CHECK (generation > 0),
    CHECK (key_id ~ '^[a-z][a-z0-9_-]{2,63}$')
);

CREATE FUNCTION enforce_audit_export_revocation_authority_sequence()
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

CREATE TRIGGER audit_export_revocation_authority_events_sequence
BEFORE INSERT ON audit_export_revocation_authority_events
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_revocation_authority_sequence();

CREATE FUNCTION enforce_audit_export_revocation_authority_key_binding()
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

CREATE TRIGGER audit_export_revocation_authority_keys_binding
BEFORE INSERT ON audit_export_revocation_authority_keys
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_revocation_authority_key_binding();

CREATE FUNCTION enforce_audit_export_revocation_authority_key_count()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    key_count bigint;
BEGIN
    SELECT count(*)
    INTO key_count
    FROM audit_export_revocation_authority_keys
    WHERE isolation_domain_id = NEW.isolation_domain_id
      AND purpose = NEW.purpose
      AND generation = NEW.generation;
    IF (NEW.operation = 'activate' AND (key_count < 1 OR key_count > 8)) OR
       (NEW.operation = 'revoke' AND key_count <> 0) THEN
        RAISE EXCEPTION 'audit export revocation authority event has an invalid key set';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER audit_export_revocation_authority_events_key_count
AFTER INSERT ON audit_export_revocation_authority_events
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_revocation_authority_key_count();

CREATE FUNCTION reject_audit_export_revocation_authority_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit export revocation authority evidence is append-only';
END;
$$;

CREATE TRIGGER audit_export_revocation_authority_events_append_only
BEFORE UPDATE OR DELETE ON audit_export_revocation_authority_events
FOR EACH ROW EXECUTE FUNCTION reject_audit_export_revocation_authority_mutation();

CREATE TRIGGER audit_export_revocation_authority_keys_append_only
BEFORE UPDATE OR DELETE ON audit_export_revocation_authority_keys
FOR EACH ROW EXECUTE FUNCTION reject_audit_export_revocation_authority_mutation();

CREATE FUNCTION audit_export_revocation_authority_is_active(
    requested_domain text,
    requested_purpose text,
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
        FROM audit_export_revocation_authority_events AS authority_event
        JOIN audit_export_revocation_authority_keys AS authority_key
          ON authority_key.isolation_domain_id = authority_event.isolation_domain_id
         AND authority_key.purpose = authority_event.purpose
         AND authority_key.generation = authority_event.generation
        WHERE authority_event.isolation_domain_id = requested_domain
          AND authority_event.purpose = requested_purpose
          AND authority_event.authority_id = requested_authority
          AND authority_event.operation = 'activate'
          AND authority_event.trust_profile_sha256 = requested_profile
          AND authority_key.key_id = requested_key
          AND authority_event.generation = (
              SELECT max(latest.generation)
              FROM audit_export_revocation_authority_events AS latest
              WHERE latest.isolation_domain_id = requested_domain
                AND latest.purpose = requested_purpose
          )
    );
$$;

CREATE OR REPLACE FUNCTION enforce_audit_export_recipient_proof_revocation()
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
    ELSIF NOT audit_export_revocation_authority_is_active(
        NEW.isolation_domain_id, 'recipient-proof', NEW.revocation_authority_id,
        NEW.revocation_trust_profile_sha256, NEW.revocation_signing_key_id
    ) THEN
        RAISE EXCEPTION 'audit export recipient proof revocation authority is not active';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION enforce_audit_export_workload_identity_revocation_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-workload-identity-revocation' || E'\n' || NEW.isolation_domain_id, 0
    ));
    IF NEW.issued_at > clock_timestamp() + interval '5 minutes' THEN
        RAISE EXCEPTION 'audit export workload identity revocation is issued in the future';
    ELSIF NOT audit_export_revocation_authority_is_active(
        NEW.isolation_domain_id, 'workload-identity', NEW.revocation_authority_id,
        NEW.revocation_trust_profile_sha256, NEW.revocation_signing_key_id
    ) THEN
        RAISE EXCEPTION 'audit export workload identity revocation authority is not active';
    END IF;
    RETURN NEW;
END;
$$;

-- dataground:down

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM audit_export_revocation_authority_events) THEN
        RAISE EXCEPTION 'schema 32 contains revocation authority evidence and cannot be downgraded safely';
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION enforce_audit_export_recipient_proof_revocation()
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

CREATE OR REPLACE FUNCTION enforce_audit_export_workload_identity_revocation_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-workload-identity-revocation' || E'\n' || NEW.isolation_domain_id, 0
    ));
    RETURN NEW;
END;
$$;

DROP FUNCTION audit_export_revocation_authority_is_active(text, text, text, text, text);
DROP TRIGGER audit_export_revocation_authority_keys_append_only
    ON audit_export_revocation_authority_keys;
DROP TRIGGER audit_export_revocation_authority_events_append_only
    ON audit_export_revocation_authority_events;
DROP FUNCTION reject_audit_export_revocation_authority_mutation();
DROP TRIGGER audit_export_revocation_authority_events_key_count
    ON audit_export_revocation_authority_events;
DROP FUNCTION enforce_audit_export_revocation_authority_key_count();
DROP TRIGGER audit_export_revocation_authority_keys_binding
    ON audit_export_revocation_authority_keys;
DROP FUNCTION enforce_audit_export_revocation_authority_key_binding();
DROP TRIGGER audit_export_revocation_authority_events_sequence
    ON audit_export_revocation_authority_events;
DROP FUNCTION enforce_audit_export_revocation_authority_sequence();
DROP TABLE audit_export_revocation_authority_keys;
DROP TABLE audit_export_revocation_authority_events;
