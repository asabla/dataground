-- dataground:up

ALTER TABLE api_authorization_decisions
    DROP CONSTRAINT api_authorization_decisions_action_check,
    DROP CONSTRAINT api_authorization_decisions_resource_type_check,
    ADD CONSTRAINT api_authorization_decisions_action_check
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
                'resolveInvocationApproval',
                'readInvocationEvents',
                'readInvocationArtifact'
            )
        ),
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

-- dataground:down

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM api_authorization_decisions
        WHERE action = 'resolveInvocationApproval'
           OR resource_type = 'DataGround::InvocationApproval'
    ) THEN
        RAISE EXCEPTION 'cannot remove invocation approval API authorization evidence';
    END IF;
END;
$$;

ALTER TABLE api_authorization_decisions
    DROP CONSTRAINT api_authorization_decisions_action_check,
    DROP CONSTRAINT api_authorization_decisions_resource_type_check,
    ADD CONSTRAINT api_authorization_decisions_action_check
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
    ADD CONSTRAINT api_authorization_decisions_resource_type_check
        CHECK (
            resource_type IN (
                'DataGround::IsolationDomain',
                'DataGround::AgentService',
                'DataGround::ServiceRevision',
                'DataGround::Invocation',
                'DataGround::Operation',
                'DataGround::Artifact'
            )
        );
