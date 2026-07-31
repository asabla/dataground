-- dataground:up

CREATE TABLE api_authorization_decisions (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    isolation_domain_id text NOT NULL,
    principal_id text NOT NULL,
    principal_kind text NOT NULL
        CHECK (
            principal_kind IN (
                'human',
                'service',
                'platform-service',
                'sandbox-workload',
                'distributed-compute-workload'
            )
        ),
    action text NOT NULL
        CHECK (
            action IN (
                'createAgentService',
                'createServiceRevision',
                'publishServiceRevision',
                'assignServiceAlias',
                'invokeAgentService',
                'readInvocation',
                'readOperation',
                'cancelInvocation',
                'readInvocationEvents',
                'readInvocationArtifact'
            )
        ),
    resource_type text NOT NULL
        CHECK (
            resource_type IN (
                'DataGround::IsolationDomain',
                'DataGround::AgentService',
                'DataGround::ServiceRevision',
                'DataGround::Invocation',
                'DataGround::Operation',
                'DataGround::Artifact'
            )
        ),
    resource_id text NOT NULL,
    outcome text NOT NULL
        CHECK (outcome IN ('allowed', 'denied', 'unavailable')),
    policy_set_id text NOT NULL,
    policy_digest text NOT NULL,
    correlation_id text NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (principal_id ~ '^[a-z][a-z0-9_-]{2,127}$'),
    CHECK (resource_id ~ '^[a-z][a-z0-9_-]{2,127}$'),
    CHECK (policy_set_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    CHECK (policy_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$')
);

CREATE INDEX api_authorization_decisions_scope_sequence_idx
    ON api_authorization_decisions (isolation_domain_id, sequence);

CREATE FUNCTION reject_api_authorization_decision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'api authorization decisions are append-only';
END;
$$;

CREATE TRIGGER api_authorization_decisions_append_only
BEFORE UPDATE OR DELETE ON api_authorization_decisions
FOR EACH ROW EXECUTE FUNCTION reject_api_authorization_decision_mutation();

-- dataground:down

DROP TABLE api_authorization_decisions;
DROP FUNCTION reject_api_authorization_decision_mutation();
