-- dataground:up

ALTER TABLE api_authorization_decisions
    DROP CONSTRAINT api_authorization_decisions_action_check,
    ADD CONSTRAINT api_authorization_decisions_action_check
        CHECK (
            action IN (
                'createWorkspace',
                'listWorkspaces',
                'readWorkspace',
                'createWorkspaceAgentService',
                'listWorkspaceAgentServices',
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
                'listScopeInvocations',
                'listScopeArtifacts',
                'searchResources',
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

-- dataground:down
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM api_authorization_decisions WHERE action='searchResources') THEN
    RAISE EXCEPTION 'cannot remove resource search authorization evidence';
  END IF;
END; $$;
ALTER TABLE api_authorization_decisions
    DROP CONSTRAINT api_authorization_decisions_action_check,
    ADD CONSTRAINT api_authorization_decisions_action_check
        CHECK (
            action IN (
                'createWorkspace',
                'listWorkspaces',
                'readWorkspace',
                'createWorkspaceAgentService',
                'listWorkspaceAgentServices',
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
                'listScopeInvocations',
                'listScopeArtifacts',
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
