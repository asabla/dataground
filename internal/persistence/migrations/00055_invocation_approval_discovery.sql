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
                'listInvocationApprovals',
                'readInvocationApproval',
                'resolveInvocationApproval',
                'readInvocationEvents',
                'readInvocationArtifact'
            )
        );

CREATE INDEX invocation_approval_discovery_idx
    ON invocation_runtime_approvals (isolation_domain_id, invocation_id, created_at DESC, id DESC);

-- dataground:down

DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM api_authorization_decisions WHERE action = 'listInvocationApprovals') THEN
        RAISE EXCEPTION 'cannot remove invocation approval discovery authorization evidence';
    END IF;
END; $$;

DROP INDEX invocation_approval_discovery_idx;

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
                'readInvocationApproval',
                'resolveInvocationApproval',
                'readInvocationEvents',
                'readInvocationArtifact'
            )
        );
