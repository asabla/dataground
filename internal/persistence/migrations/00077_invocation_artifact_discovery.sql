-- dataground:up

ALTER TABLE api_authorization_decisions
    DROP CONSTRAINT api_authorization_decisions_action_check,
    ADD CONSTRAINT api_authorization_decisions_action_check
        CHECK (
            action IN (
                'createWorkspace',
                'listWorkspaces',
                'readWorkspace',
                'createAgentService',
                'listAgentServices',
                'readAgentService',
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
                'listInvocationArtifacts',
                'readInvocationArtifact',
                'readInvocationArtifactContent'
            )
        );

CREATE INDEX artifacts_invocation_discovery_idx
    ON artifacts (isolation_domain_id, invocation_id, created_at DESC, id DESC);

-- dataground:down

DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM api_authorization_decisions WHERE action = 'listInvocationArtifacts') THEN
        RAISE EXCEPTION 'cannot remove artifact discovery authorization evidence';
    END IF;
END; $$;

DROP INDEX artifacts_invocation_discovery_idx;

ALTER TABLE api_authorization_decisions
    DROP CONSTRAINT api_authorization_decisions_action_check,
    ADD CONSTRAINT api_authorization_decisions_action_check
        CHECK (
            action IN (
                'createWorkspace',
                'listWorkspaces',
                'readWorkspace',
                'createAgentService',
                'listAgentServices',
                'readAgentService',
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
                'readInvocationArtifact',
                'readInvocationArtifactContent'
            )
        );
