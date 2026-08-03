-- dataground:up

ALTER TABLE audit_records
    ADD COLUMN sequence bigint GENERATED ALWAYS AS IDENTITY,
    ADD CONSTRAINT audit_records_sequence_positive CHECK (sequence > 0);

CREATE UNIQUE INDEX audit_records_domain_sequence_idx
    ON audit_records (isolation_domain_id, sequence);

CREATE TABLE operator_audit_exports (
    export_id text PRIMARY KEY,
    isolation_domain_id text NOT NULL,
    schema_version text NOT NULL,
    requested_by text NOT NULL,
    reason_digest bytea NOT NULL,
    correlation_id text NOT NULL UNIQUE,
    request_cursor text NOT NULL,
    frozen_cursor text NOT NULL,
    limit_value integer NOT NULL,
    record_count integer NOT NULL,
    request_digest bytea NOT NULL,
    content_digest bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (export_id ~ '^oax_[0-9a-z]{20,32}$'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (schema_version = 'dataground.dev.operator-audit-export/v1'),
    CHECK (length(reason_digest) = 32),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (limit_value BETWEEN 1 AND 1000),
    CHECK (record_count BETWEEN 0 AND limit_value),
    CHECK (length(request_digest) = 32),
    CHECK (length(content_digest) = 32)
);

CREATE FUNCTION reject_operator_audit_export_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'operator audit export receipts are append-only';
END;
$$;

CREATE TRIGGER operator_audit_exports_append_only
BEFORE UPDATE OR DELETE ON operator_audit_exports
FOR EACH ROW
EXECUTE FUNCTION reject_operator_audit_export_mutation();

-- dataground:down

DROP TRIGGER operator_audit_exports_append_only ON operator_audit_exports;
DROP FUNCTION reject_operator_audit_export_mutation();
DROP TABLE operator_audit_exports;
DROP INDEX audit_records_domain_sequence_idx;
ALTER TABLE audit_records DROP CONSTRAINT audit_records_sequence_positive;
ALTER TABLE audit_records DROP COLUMN sequence;
