-- dataground:up

CREATE TABLE oidc_provider_credential_operations (
    isolation_domain_id text NOT NULL,
    provider_id text NOT NULL,
    endpoint text NOT NULL,
    generation bigint NOT NULL,
    contract text NOT NULL,
    operation text NOT NULL,
    provider_registry_sha256 text NOT NULL,
    publication_path_digest bytea NOT NULL,
    credential_digest bytea NOT NULL,
    activated_at timestamptz,
    expires_at timestamptz,
    revoked_at timestamptz,
    actor_id text NOT NULL,
    correlation_id text NOT NULL UNIQUE,
    reason_digest bytea NOT NULL,
    status text NOT NULL DEFAULT 'prepared',
    prepared_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz,
    PRIMARY KEY (isolation_domain_id, provider_id, endpoint, generation),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (provider_id ~ '^[a-z][a-z0-9._-]{0,127}$'),
    CHECK (endpoint IN ('discovery', 'jwks')),
    CHECK (generation > 0),
    CHECK (contract = 'dataground.oidc-provider-credential-request/v2'),
    CHECK (operation IN ('activate', 'revoke')),
    CHECK (provider_registry_sha256 ~ '^[0-9a-f]{64}$'),
    CHECK (octet_length(publication_path_digest) = 32),
    CHECK (octet_length(credential_digest) = 32),
    CHECK (actor_id ~ '^[a-z][a-z0-9_-]{2,127}$'),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (octet_length(reason_digest) = 32),
    CHECK (status IN ('prepared', 'succeeded')),
    CHECK (
        (operation = 'activate' AND activated_at IS NOT NULL AND expires_at IS NOT NULL AND
         revoked_at IS NULL AND expires_at > activated_at AND expires_at - activated_at <= interval '31 days') OR
        (operation = 'revoke' AND activated_at IS NULL AND expires_at IS NULL AND revoked_at IS NOT NULL)
    ),
    CHECK (
        (status = 'prepared' AND completed_at IS NULL) OR
        (status = 'succeeded' AND completed_at IS NOT NULL)
    )
);

CREATE INDEX oidc_provider_credential_operations_pending_idx
    ON oidc_provider_credential_operations (prepared_at, isolation_domain_id, provider_id, endpoint, generation)
    WHERE status = 'prepared';

CREATE FUNCTION enforce_oidc_provider_credential_operation_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.status <> 'prepared' OR NEW.status <> 'succeeded' OR
       OLD.isolation_domain_id <> NEW.isolation_domain_id OR
       OLD.provider_id <> NEW.provider_id OR
       OLD.endpoint <> NEW.endpoint OR
       OLD.generation <> NEW.generation OR
       OLD.contract <> NEW.contract OR
       OLD.operation <> NEW.operation OR
       OLD.provider_registry_sha256 <> NEW.provider_registry_sha256 OR
       OLD.publication_path_digest <> NEW.publication_path_digest OR
       OLD.credential_digest <> NEW.credential_digest OR
       OLD.activated_at IS DISTINCT FROM NEW.activated_at OR
       OLD.expires_at IS DISTINCT FROM NEW.expires_at OR
       OLD.revoked_at IS DISTINCT FROM NEW.revoked_at OR
       OLD.actor_id <> NEW.actor_id OR
       OLD.correlation_id <> NEW.correlation_id OR
       OLD.reason_digest <> NEW.reason_digest OR
       OLD.prepared_at <> NEW.prepared_at OR
       NEW.completed_at IS NULL THEN
        RAISE EXCEPTION 'OIDC provider credential operations permit only prepared-to-succeeded transitions';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER oidc_provider_credential_operations_controlled_update
BEFORE UPDATE ON oidc_provider_credential_operations
FOR EACH ROW EXECUTE FUNCTION enforce_oidc_provider_credential_operation_transition();

CREATE FUNCTION reject_oidc_provider_credential_operation_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'OIDC provider credential operations cannot be deleted';
END;
$$;

CREATE TRIGGER oidc_provider_credential_operations_no_delete
BEFORE DELETE ON oidc_provider_credential_operations
FOR EACH ROW EXECUTE FUNCTION reject_oidc_provider_credential_operation_delete();

CREATE FUNCTION reject_audit_record_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit records are append-only';
END;
$$;

CREATE TRIGGER audit_records_append_only
BEFORE UPDATE OR DELETE ON audit_records
FOR EACH ROW EXECUTE FUNCTION reject_audit_record_mutation();

-- dataground:down

DROP TRIGGER audit_records_append_only ON audit_records;
DROP FUNCTION reject_audit_record_mutation();
DROP TABLE oidc_provider_credential_operations;
DROP FUNCTION reject_oidc_provider_credential_operation_delete();
DROP FUNCTION enforce_oidc_provider_credential_operation_transition();
