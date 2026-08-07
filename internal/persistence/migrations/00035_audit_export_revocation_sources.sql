-- dataground:up

CREATE TABLE audit_export_revocation_source_events (
    authorization_contract text NOT NULL,
    isolation_domain_id text NOT NULL,
    purpose text NOT NULL,
    source_id text NOT NULL,
    generation bigint NOT NULL,
    operation text NOT NULL,
    source_registry_sha256 text NOT NULL,
    actor_id text NOT NULL,
    reason_digest bytea NOT NULL,
    correlation_id text NOT NULL UNIQUE,
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
    PRIMARY KEY (isolation_domain_id, purpose, generation),
    CHECK (authorization_contract = 'dataground.audit-export-revocation-source-authorization/v1'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (purpose IN ('recipient-proof', 'workload-identity')),
    CHECK (source_id ~ '^[a-z][a-z0-9._-]{0,127}$'),
    CHECK (generation > 0),
    CHECK (operation IN ('activate', 'revoke')),
    CHECK (source_registry_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (actor_id <> '' AND length(actor_id) <= 256 AND actor_id !~ '[[:cntrl:]]'),
    CHECK (octet_length(reason_digest) = 32),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (isfinite(occurred_at))
);

CREATE FUNCTION enforce_audit_export_revocation_source_sequence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    latest_generation bigint;
    latest_operation text;
    latest_source_id text;
    latest_registry_sha256 text;
    lock_namespace text;
BEGIN
    IF NEW.creation_transaction_id <> txid_current() THEN
        RAISE EXCEPTION 'audit export revocation source transaction binding is invalid';
    END IF;
    IF NEW.purpose = 'recipient-proof' THEN
        lock_namespace := 'audit-export-recipient-proof-revocation';
    ELSIF NEW.purpose = 'workload-identity' THEN
        lock_namespace := 'audit-export-workload-identity-revocation';
    ELSE
        RAISE EXCEPTION 'audit export revocation source purpose is invalid';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(
        lock_namespace || E'\n' || NEW.isolation_domain_id, 0
    ));
    SELECT generation, operation, source_id, source_registry_sha256
    INTO latest_generation, latest_operation, latest_source_id, latest_registry_sha256
    FROM audit_export_revocation_source_events
    WHERE isolation_domain_id = NEW.isolation_domain_id
      AND purpose = NEW.purpose
    ORDER BY generation DESC
    LIMIT 1;
    IF NOT FOUND THEN
        IF NEW.generation <> 1 OR NEW.operation <> 'activate' THEN
            RAISE EXCEPTION 'audit export revocation source must begin with generation 1 activation';
        END IF;
    ELSIF latest_generation = 9223372036854775807 OR
          NEW.generation <> latest_generation + 1 THEN
        RAISE EXCEPTION 'audit export revocation source generations must be sequential';
    ELSIF NEW.operation = 'revoke' AND
          (latest_operation <> 'activate' OR NEW.source_id <> latest_source_id OR
           NEW.source_registry_sha256 <> latest_registry_sha256) THEN
        RAISE EXCEPTION 'audit export revocation source withdrawal must match the active source';
    ELSIF NEW.operation = 'activate' AND latest_operation = 'activate' AND
          NEW.source_id = latest_source_id AND
          NEW.source_registry_sha256 = latest_registry_sha256 THEN
        RAISE EXCEPTION 'audit export revocation source rotation must change the active source';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_revocation_source_events_sequence
BEFORE INSERT ON audit_export_revocation_source_events
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_revocation_source_sequence();

CREATE FUNCTION reject_audit_export_revocation_source_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit export revocation source evidence is append-only';
END;
$$;

CREATE TRIGGER audit_export_revocation_source_events_append_only
BEFORE UPDATE OR DELETE ON audit_export_revocation_source_events
FOR EACH ROW EXECUTE FUNCTION reject_audit_export_revocation_source_mutation();

ALTER TABLE audit_export_revocation_acquisitions
    DROP CONSTRAINT audit_export_revocation_acquisitions_contract_check;

ALTER TABLE audit_export_revocation_acquisitions
    ADD COLUMN source_generation bigint,
    ADD CONSTRAINT audit_export_revocation_acquisitions_governed_contract CHECK (
        (contract = 'dataground.audit-export-revocation-acquisition/v1' AND
         source_generation IS NULL) OR
        (contract = 'dataground.audit-export-revocation-acquisition/v2' AND
         source_generation > 0)
    );

DROP TRIGGER audit_export_revocation_acquisitions_binding
    ON audit_export_revocation_acquisitions;
DROP FUNCTION enforce_audit_export_revocation_acquisition_binding();

CREATE FUNCTION enforce_audit_export_revocation_acquisition_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    lock_namespace text;
BEGIN
    IF NEW.purpose = 'recipient-proof' THEN
        lock_namespace := 'audit-export-recipient-proof-revocation';
    ELSIF NEW.purpose = 'workload-identity' THEN
        lock_namespace := 'audit-export-workload-identity-revocation';
    ELSE
        RAISE EXCEPTION 'audit export revocation acquisition purpose is invalid';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(
        lock_namespace || E'\n' || NEW.isolation_domain_id, 0
    ));
    IF NEW.contract <> 'dataground.audit-export-revocation-acquisition/v2' OR
       NEW.source_generation IS NULL OR NOT EXISTS (
        SELECT 1
        FROM audit_export_revocation_source_events AS source_event
        WHERE source_event.isolation_domain_id = NEW.isolation_domain_id
          AND source_event.purpose = NEW.purpose
          AND source_event.source_id = NEW.source_id
          AND source_event.source_registry_sha256 = NEW.source_registry_sha256
          AND source_event.generation = NEW.source_generation
          AND source_event.operation = 'activate'
          AND source_event.generation = (
              SELECT max(latest.generation)
              FROM audit_export_revocation_source_events AS latest
              WHERE latest.isolation_domain_id = NEW.isolation_domain_id
                AND latest.purpose = NEW.purpose
          )
    ) THEN
        RAISE EXCEPTION 'audit export revocation acquisition source is not active';
    END IF;
    IF NEW.purpose = 'recipient-proof' THEN
        IF NOT EXISTS (
            SELECT 1
            FROM audit_export_recipient_proof_revocations AS revocation
            WHERE revocation.revocation_sha256 = NEW.revocation_sha256
              AND revocation.isolation_domain_id = NEW.isolation_domain_id
              AND revocation.revocation_trust_profile_sha256 = NEW.trust_profile_sha256
              AND revocation.correlation_id = NEW.correlation_id
        ) THEN
            RAISE EXCEPTION 'audit export recipient proof revocation acquisition is unbound';
        END IF;
    ELSIF NEW.purpose = 'workload-identity' THEN
        IF NOT EXISTS (
            SELECT 1
            FROM audit_export_workload_identity_revocations AS revocation
            WHERE revocation.revocation_sha256 = NEW.revocation_sha256
              AND revocation.isolation_domain_id = NEW.isolation_domain_id
              AND revocation.revocation_trust_profile_sha256 = NEW.trust_profile_sha256
              AND revocation.correlation_id = NEW.correlation_id
        ) THEN
            RAISE EXCEPTION 'audit export workload identity revocation acquisition is unbound';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_revocation_acquisitions_binding
BEFORE INSERT ON audit_export_revocation_acquisitions
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_revocation_acquisition_binding();

-- dataground:down

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM audit_export_revocation_source_events) OR
       EXISTS (
           SELECT 1 FROM audit_export_revocation_acquisitions
           WHERE contract = 'dataground.audit-export-revocation-acquisition/v2'
       ) THEN
        RAISE EXCEPTION 'schema 35 contains governed revocation source evidence and cannot be downgraded safely';
    END IF;
END;
$$;

DROP TRIGGER audit_export_revocation_acquisitions_binding
    ON audit_export_revocation_acquisitions;
DROP FUNCTION enforce_audit_export_revocation_acquisition_binding();

ALTER TABLE audit_export_revocation_acquisitions
    DROP CONSTRAINT audit_export_revocation_acquisitions_governed_contract,
    DROP COLUMN source_generation,
    ADD CONSTRAINT audit_export_revocation_acquisitions_contract_check
        CHECK (contract = 'dataground.audit-export-revocation-acquisition/v1');

CREATE FUNCTION enforce_audit_export_revocation_acquisition_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.purpose = 'recipient-proof' THEN
        IF NOT EXISTS (
            SELECT 1
            FROM audit_export_recipient_proof_revocations AS revocation
            WHERE revocation.revocation_sha256 = NEW.revocation_sha256
              AND revocation.isolation_domain_id = NEW.isolation_domain_id
              AND revocation.revocation_trust_profile_sha256 = NEW.trust_profile_sha256
              AND revocation.correlation_id = NEW.correlation_id
        ) THEN
            RAISE EXCEPTION 'audit export recipient proof revocation acquisition is unbound';
        END IF;
    ELSIF NEW.purpose = 'workload-identity' THEN
        IF NOT EXISTS (
            SELECT 1
            FROM audit_export_workload_identity_revocations AS revocation
            WHERE revocation.revocation_sha256 = NEW.revocation_sha256
              AND revocation.isolation_domain_id = NEW.isolation_domain_id
              AND revocation.revocation_trust_profile_sha256 = NEW.trust_profile_sha256
              AND revocation.correlation_id = NEW.correlation_id
        ) THEN
            RAISE EXCEPTION 'audit export workload identity revocation acquisition is unbound';
        END IF;
    ELSE
        RAISE EXCEPTION 'audit export revocation acquisition purpose is invalid';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_revocation_acquisitions_binding
BEFORE INSERT ON audit_export_revocation_acquisitions
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_revocation_acquisition_binding();

DROP TRIGGER audit_export_revocation_source_events_append_only
    ON audit_export_revocation_source_events;
DROP FUNCTION reject_audit_export_revocation_source_mutation();
DROP TRIGGER audit_export_revocation_source_events_sequence
    ON audit_export_revocation_source_events;
DROP FUNCTION enforce_audit_export_revocation_source_sequence();
DROP TABLE audit_export_revocation_source_events;
