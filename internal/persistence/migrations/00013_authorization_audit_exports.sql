-- dataground:up

CREATE TABLE authorization_audit_exports (
    export_id text PRIMARY KEY,
    isolation_domain_id text NOT NULL,
    schema_version text NOT NULL,
    requested_by text NOT NULL,
    reason_digest bytea NOT NULL,
    correlation_id text NOT NULL,
    request_cursor text NOT NULL,
    frozen_cursor text NOT NULL,
    limit_value integer NOT NULL,
    record_count integer NOT NULL,
    request_digest bytea NOT NULL,
    content_digest bytea NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (export_id ~ '^aex_[0-9a-z]{20,32}$'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (schema_version = 'dataground.dev.authorization-audit-export/v1'),
    CHECK (length(requested_by) BETWEEN 1 AND 256),
    CHECK (requested_by !~ '[[:cntrl:]]'),
    CHECK (octet_length(reason_digest) = 32),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (length(request_cursor) <= 256),
    CHECK (length(frozen_cursor) BETWEEN 1 AND 256),
    CHECK (limit_value BETWEEN 1 AND 1000),
    CHECK (record_count BETWEEN 0 AND limit_value),
    CHECK (octet_length(request_digest) = 32),
    CHECK (octet_length(content_digest) = 32)
);

CREATE INDEX authorization_audit_exports_scope_recorded_idx
    ON authorization_audit_exports (isolation_domain_id, recorded_at, export_id);

CREATE FUNCTION reject_authorization_audit_export_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'authorization audit export receipts are append-only';
END;
$$;

CREATE TRIGGER authorization_audit_exports_append_only
BEFORE UPDATE OR DELETE ON authorization_audit_exports
FOR EACH ROW EXECUTE FUNCTION reject_authorization_audit_export_mutation();

-- dataground:down

DROP TABLE authorization_audit_exports;
DROP FUNCTION reject_authorization_audit_export_mutation();
