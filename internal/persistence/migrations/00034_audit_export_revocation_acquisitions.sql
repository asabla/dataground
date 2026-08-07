-- dataground:up

CREATE TABLE audit_export_revocation_acquisitions (
    contract text NOT NULL,
    purpose text NOT NULL,
    revocation_sha256 text NOT NULL,
    isolation_domain_id text NOT NULL,
    source_id text NOT NULL,
    source_registry_sha256 text NOT NULL,
    trust_profile_sha256 text NOT NULL,
    correlation_id text NOT NULL UNIQUE,
    acquired_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (purpose, revocation_sha256),
    CHECK (contract = 'dataground.audit-export-revocation-acquisition/v1'),
    CHECK (purpose IN ('recipient-proof', 'workload-identity')),
    CHECK (revocation_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (source_id ~ '^[a-z][a-z0-9._-]{0,127}$'),
    CHECK (source_registry_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (isfinite(acquired_at))
);

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

CREATE FUNCTION reject_audit_export_revocation_acquisition_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit export revocation acquisitions are append-only';
END;
$$;

CREATE TRIGGER audit_export_revocation_acquisitions_append_only
BEFORE UPDATE OR DELETE ON audit_export_revocation_acquisitions
FOR EACH ROW EXECUTE FUNCTION reject_audit_export_revocation_acquisition_mutation();

-- dataground:down

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM audit_export_revocation_acquisitions) THEN
        RAISE EXCEPTION 'schema 34 contains revocation acquisition evidence and cannot be downgraded safely';
    END IF;
END;
$$;

DROP TRIGGER audit_export_revocation_acquisitions_append_only
    ON audit_export_revocation_acquisitions;
DROP FUNCTION reject_audit_export_revocation_acquisition_mutation();
DROP TRIGGER audit_export_revocation_acquisitions_binding
    ON audit_export_revocation_acquisitions;
DROP FUNCTION enforce_audit_export_revocation_acquisition_binding();
DROP TABLE audit_export_revocation_acquisitions;
