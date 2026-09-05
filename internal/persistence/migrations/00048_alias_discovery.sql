-- dataground:up

CREATE INDEX service_aliases_active_name_idx
    ON service_aliases (isolation_domain_id, service_id, name COLLATE "C")
    WHERE withdrawn_at IS NULL;

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

-- dataground:down

DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM api_authorization_decisions WHERE action = 'listServiceAliases') THEN
        RAISE EXCEPTION 'cannot remove alias discovery authorization evidence';
    END IF;
END; $$;

DROP INDEX service_aliases_active_name_idx;

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
