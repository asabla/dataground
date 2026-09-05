-- dataground:up

ALTER TABLE service_aliases ADD COLUMN withdrawn_at timestamptz;


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

-- dataground:down

DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM service_aliases WHERE withdrawn_at IS NOT NULL)
 OR EXISTS (SELECT 1 FROM api_authorization_decisions WHERE action='withdrawServiceAlias')
 OR EXISTS (SELECT 1 FROM audit_records WHERE action='service-alias.withdrawn') THEN
  RAISE EXCEPTION 'cannot remove alias withdrawal evidence';
 END IF;
END; $$;

ALTER TABLE service_aliases DROP COLUMN withdrawn_at;


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

