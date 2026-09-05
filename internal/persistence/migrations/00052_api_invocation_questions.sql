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
                'readInvocationApproval',
                'resolveInvocationApproval',
                'readInvocationEvents',
                'readInvocationArtifact'
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

-- dataground:down
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM api_authorization_decisions WHERE action IN ('readInvocationQuestion','answerInvocationQuestion') OR resource_type='DataGround::InvocationQuestion') THEN
  RAISE EXCEPTION 'question API authorization evidence prevents downgrade';
 END IF;
END; $$;
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
                'readInvocationApproval',
                'resolveInvocationApproval',
                'readInvocationEvents',
                'readInvocationArtifact'
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
                'DataGround::Operation',
                'DataGround::Artifact'
            )
        );

