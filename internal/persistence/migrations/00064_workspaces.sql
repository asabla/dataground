-- dataground:up

CREATE TABLE workspaces (
    isolation_domain_id text NOT NULL,
    id text NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    generation bigint NOT NULL DEFAULT 1 CHECK (generation >= 1),
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    created_by text NOT NULL,
    PRIMARY KEY (isolation_domain_id, id),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (id ~ '^wsp_[0-9a-z]{20,32}$'),
    CHECK (octet_length(name) BETWEEN 1 AND 128 AND octet_length(description) <= 2048)
);

CREATE INDEX workspaces_discovery_idx ON workspaces (isolation_domain_id, created_at DESC, id DESC);

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

ALTER TABLE api_authorization_decisions
    DROP CONSTRAINT api_authorization_decisions_resource_type_check,
    ADD CONSTRAINT api_authorization_decisions_resource_type_check
        CHECK (
            resource_type IN (
                'DataGround::Workspace',
                'DataGround::IsolationDomain',
                'DataGround::AgentService',
                'DataGround::ServiceRevision',
                'DataGround::Invocation',
                'DataGround::InvocationApproval',
                'DataGround::InvocationQuestion',
                'DataGround::Operation',
                'DataGround::Artifact'
            )
        );

-- dataground:down
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM workspaces) OR EXISTS (
        SELECT 1 FROM api_authorization_decisions
        WHERE action IN ('createWorkspace', 'listWorkspaces', 'readWorkspace')
           OR resource_type = 'DataGround::Workspace'
    ) THEN
        RAISE EXCEPTION 'cannot remove workspace resources or authorization evidence';
    END IF;
END; $$;

DROP TABLE workspaces;

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
                'readInvocationArtifact',
                'readInvocationArtifactContent'
            )
        );

ALTER TABLE api_authorization_decisions
    DROP CONSTRAINT api_authorization_decisions_resource_type_check,
    ADD CONSTRAINT api_authorization_decisions_resource_type_check
        CHECK (
            resource_type IN (
                'DataGround::IsolationDomain',
                'DataGround::AgentService',
                'DataGround::ServiceRevision',
                'DataGround::Invocation',
                'DataGround::InvocationApproval',
                'DataGround::InvocationQuestion',
                'DataGround::Operation',
                'DataGround::Artifact'
            )
        );
