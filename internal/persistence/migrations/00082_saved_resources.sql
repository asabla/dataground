-- dataground:up

CREATE TABLE saved_resources (
    isolation_domain_id text NOT NULL CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    principal_kind text NOT NULL CHECK (principal_kind IN ('human','service','platform-service','sandbox-workload','distributed-compute-workload')),
    principal_id text NOT NULL CHECK (principal_id ~ '^[a-z][a-z0-9_-]{2,127}$'),
    resource_kind text NOT NULL,
    resource_id text NOT NULL,
    saved boolean NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (isolation_domain_id, principal_kind, principal_id, resource_kind, resource_id),
    CHECK ((resource_kind='workspace' AND resource_id ~ '^wsp_[0-9a-z]{20,32}$') OR
           (resource_kind='agent-service' AND resource_id ~ '^svc_[0-9a-z]{20,32}$') OR
           (resource_kind='invocation' AND resource_id ~ '^inv_[0-9a-z]{20,32}$') OR
           (resource_kind='artifact' AND resource_id ~ '^art_[0-9a-z]{20,32}$'))
);
CREATE INDEX saved_resources_discovery_idx ON saved_resources
    (isolation_domain_id, principal_kind, principal_id, updated_at DESC, resource_id DESC) WHERE saved;

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
                'listSavedResources',
                'setSavedResource',
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
  IF EXISTS (SELECT 1 FROM saved_resources) OR EXISTS (
    SELECT 1 FROM api_authorization_decisions WHERE action IN ('listSavedResources','setSavedResource')
  ) THEN
    RAISE EXCEPTION 'cannot remove saved resources or authorization evidence';
  END IF;
END; $$;
DROP TABLE saved_resources;

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

