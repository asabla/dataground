-- dataground:up

-- Store the top-level transaction ID, never xmin (which can be a subtransaction
-- or frozen). Legacy rows belong to this migration transaction. This preserves
-- snapshot membership across connections without holding an open transaction.
ALTER TABLE audit_records ADD COLUMN recorded_transaction_id xid8 NOT NULL DEFAULT pg_current_xact_id();
ALTER TABLE invocation_authorization_decisions ADD COLUMN recorded_transaction_id xid8 NOT NULL DEFAULT pg_current_xact_id();
ALTER TABLE publication_authorization_decisions ADD COLUMN recorded_transaction_id xid8 NOT NULL DEFAULT pg_current_xact_id();

-- Random stable public identifiers do not reveal an enumerable global sequence.
ALTER TABLE invocation_authorization_decisions ADD COLUMN public_audit_id uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE;
ALTER TABLE publication_authorization_decisions ADD COLUMN public_audit_id uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE;

CREATE INDEX resource_audit_lifecycle_idx ON audit_records (isolation_domain_id, resource_type, resource_id, sequence);
CREATE INDEX resource_audit_invocation_idx ON invocation_authorization_decisions (isolation_domain_id, invocation_id, sequence);
CREATE INDEX resource_audit_publication_idx ON publication_authorization_decisions (isolation_domain_id, revision_id, sequence);

CREATE TABLE resource_audit_read_receipts (
    id text PRIMARY KEY CHECK (id ~ '^arr_[0-9a-z]{20,32}$'),
    contract text NOT NULL CHECK (contract = 'dataground.resource-audit-page/v1'),
    isolation_domain_id text NOT NULL CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    resource_type text NOT NULL CHECK (resource_type IN ('service-revision','invocation')),
    resource_id text NOT NULL,
    principal_id text NOT NULL CHECK (principal_id ~ '^[a-z][a-z0-9_-]{2,127}$'),
    principal_kind text NOT NULL CHECK (principal_kind IN ('human','service','platform-service','sandbox-workload','distributed-compute-workload')),
    correlation_id text NOT NULL CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    request_cursor text NOT NULL CHECK (request_cursor = '' OR request_cursor ~ '^arr_[0-9a-z]{20,32}$'),
    snapshot pg_snapshot NOT NULL CHECK (octet_length(snapshot::text) <= 65536),
    operation_id text NOT NULL CHECK (operation_id = '' OR operation_id ~ '^op_[0-9a-z]{20,32}$'),
    after_source integer NOT NULL CHECK (after_source BETWEEN 0 AND 1),
    after_sequence bigint NOT NULL CHECK (after_sequence >= 0),
    has_more boolean NOT NULL,
    limit_value integer NOT NULL CHECK (limit_value BETWEEN 1 AND 100),
    record_count integer NOT NULL CHECK (record_count BETWEEN 0 AND limit_value),
    content_digest bytea NOT NULL CHECK (length(content_digest) = 32),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(recorded_at)),
    CHECK ((resource_type = 'service-revision' AND resource_id ~ '^rev_[0-9a-z]{20,32}$') OR
           (resource_type = 'invocation' AND resource_id ~ '^inv_[0-9a-z]{20,32}$')),
    CHECK (NOT has_more OR record_count = limit_value)
);
CREATE INDEX resource_audit_read_scope_idx ON resource_audit_read_receipts (isolation_domain_id, resource_type, resource_id, recorded_at);
CREATE TRIGGER resource_audit_read_append_only BEFORE UPDATE OR DELETE ON resource_audit_read_receipts
    FOR EACH ROW EXECUTE FUNCTION reject_operator_audit_export_mutation();

ALTER TABLE api_authorization_decisions
    DROP CONSTRAINT api_authorization_decisions_action_check,
    ADD CONSTRAINT api_authorization_decisions_action_check
        CHECK (
            action IN (
                'createAgentService',
                'listAgentServices',
                'createServiceRevision',
                'listServiceRevisions',
                'readServiceRevision',
                'readServiceRevisionAudit',
                'readInvocationAudit',
                'publishServiceRevision',
                'retireServiceRevision',
                'readServiceAlias',
                'listServiceAliases',
                'assignServiceAlias',
                'withdrawServiceAlias',
                'invokeAgentService',
                'listInvocations',
                'readInvocation',
                'readOperation',
                'cancelInvocation',
                'readInvocationQuestion',
                'answerInvocationQuestion',
                'listInvocationQuestions',
                'listInvocationApprovals',
                'readInvocationApproval',
                'resolveInvocationApproval',
                'readInvocationEvents',
                'readInvocationArtifact'
            )
        );

-- dataground:down

DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM resource_audit_read_receipts) OR
       EXISTS (SELECT 1 FROM api_authorization_decisions WHERE action IN ('readServiceRevisionAudit','readInvocationAudit')) THEN
        RAISE EXCEPTION 'cannot remove resource audit read evidence';
    END IF;
END; $$;
DROP TABLE resource_audit_read_receipts;
ALTER TABLE invocation_authorization_decisions DROP COLUMN public_audit_id;
ALTER TABLE publication_authorization_decisions DROP COLUMN public_audit_id;
DROP INDEX resource_audit_lifecycle_idx;
DROP INDEX resource_audit_invocation_idx;
DROP INDEX resource_audit_publication_idx;
ALTER TABLE audit_records DROP COLUMN recorded_transaction_id;
ALTER TABLE invocation_authorization_decisions DROP COLUMN recorded_transaction_id;
ALTER TABLE publication_authorization_decisions DROP COLUMN recorded_transaction_id;

ALTER TABLE api_authorization_decisions
    DROP CONSTRAINT api_authorization_decisions_action_check,
    ADD CONSTRAINT api_authorization_decisions_action_check
        CHECK (
            action IN (
                'createAgentService',
                'listAgentServices',
                'createServiceRevision',
                'listServiceRevisions',
                'readServiceRevision',
                'publishServiceRevision',
                'retireServiceRevision',
                'readServiceAlias',
                'listServiceAliases',
                'assignServiceAlias',
                'withdrawServiceAlias',
                'invokeAgentService',
                'listInvocations',
                'readInvocation',
                'readOperation',
                'cancelInvocation',
                'readInvocationQuestion',
                'answerInvocationQuestion',
                'listInvocationQuestions',
                'listInvocationApprovals',
                'readInvocationApproval',
                'resolveInvocationApproval',
                'readInvocationEvents',
                'readInvocationArtifact'
            )
        );
