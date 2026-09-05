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
                'retireServiceRevision',
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

-- dataground:down

DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM api_authorization_decisions WHERE action='retireServiceRevision')
 OR EXISTS (SELECT 1 FROM audit_records WHERE action='service-revision.retired') THEN
  RAISE EXCEPTION 'cannot remove revision retirement evidence';
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

