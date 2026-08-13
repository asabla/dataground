-- dataground:up

ALTER TABLE api_authorization_decisions
    DROP CONSTRAINT api_authorization_decisions_action_check,
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
                'readInvocationApproval',
                'resolveInvocationApproval',
                'readInvocationEvents',
                'readInvocationArtifact'
            )
        );

-- dataground:down

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM api_authorization_decisions
        WHERE action = 'readInvocationApproval'
    ) THEN
        RAISE EXCEPTION 'cannot remove invocation approval read authorization evidence';
    END IF;
END;
$$;

ALTER TABLE api_authorization_decisions
    DROP CONSTRAINT api_authorization_decisions_action_check,
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
        );
