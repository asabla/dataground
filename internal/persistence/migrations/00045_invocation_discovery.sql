-- dataground:up

ALTER TABLE api_authorization_decisions
    DROP CONSTRAINT api_authorization_decisions_action_check,
    ADD CONSTRAINT api_authorization_decisions_action_check
        CHECK (
            action IN (
                'createAgentService',
                'listAgentServices',
                'createServiceRevision',
                'listServiceRevisions',
                'publishServiceRevision',
                'readServiceAlias',
                'assignServiceAlias',
                'invokeAgentService',
                'listInvocations',
                'readInvocation',
                'readOperation',
                'cancelInvocation',
                'readInvocationApproval',
                'resolveInvocationApproval',
                'readInvocationEvents',
                'readInvocationArtifact'
            )
        );

CREATE INDEX invocations_service_history_idx
    ON invocations (isolation_domain_id, service_id, created_at DESC, id DESC);

-- dataground:down

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM api_authorization_decisions WHERE action = 'listInvocations') THEN
        RAISE EXCEPTION 'cannot remove invocation discovery authorization evidence';
    END IF;
END;
$$;

DROP INDEX invocations_service_history_idx;

ALTER TABLE api_authorization_decisions
    DROP CONSTRAINT api_authorization_decisions_action_check,
    ADD CONSTRAINT api_authorization_decisions_action_check
        CHECK (
            action IN (
                'createAgentService',
                'listAgentServices',
                'createServiceRevision',
                'listServiceRevisions',
                'publishServiceRevision',
                'readServiceAlias',
                'assignServiceAlias',
                'invokeAgentService',
                'readInvocation',
                'readOperation',
                'cancelInvocation',
                'readInvocationApproval',
                'resolveInvocationApproval',
                'readInvocationEvents',
                'readInvocationArtifact'
            )
        );
