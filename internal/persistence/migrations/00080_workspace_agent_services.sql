-- dataground:up

ALTER TABLE agent_services ADD COLUMN workspace_id text,
    ADD CONSTRAINT agent_services_workspace_fk FOREIGN KEY (isolation_domain_id, workspace_id) REFERENCES workspaces (isolation_domain_id, id);
CREATE INDEX agent_services_workspace_discovery_idx ON agent_services (isolation_domain_id, workspace_id, created_at DESC, id DESC) WHERE workspace_id IS NOT NULL;

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

-- dataground:down
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM agent_services WHERE workspace_id IS NOT NULL)
     OR EXISTS (SELECT 1 FROM api_authorization_decisions WHERE action IN ('createWorkspaceAgentService','listWorkspaceAgentServices')) THEN
    RAISE EXCEPTION 'cannot remove workspace service relationships or authorization evidence';
  END IF;
END; $$;
DROP INDEX agent_services_workspace_discovery_idx;
ALTER TABLE agent_services DROP COLUMN workspace_id;
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

