-- dataground:up

ALTER TABLE service_publication_operations
    DROP CONSTRAINT governed_publication_states,
    ADD CONSTRAINT governed_publication_states CHECK (
        state_machine_version NOT IN (3, 4) OR observed_state IN ('queued', 'validating', 'published', 'failed', 'cancelled')
    );
CREATE OR REPLACE FUNCTION enforce_governed_publication_request()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'governed publication requests are immutable';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM service_publication_operations operation
        JOIN service_revisions revision
          ON revision.isolation_domain_id = operation.isolation_domain_id
         AND revision.id = operation.revision_id
        WHERE operation.isolation_domain_id = NEW.isolation_domain_id
          AND operation.id = NEW.operation_id
          AND operation.revision_id = NEW.revision_id
          AND operation.state_machine_version IN (3, 4)
          AND operation.observed_state = 'queued'
          AND revision.service_id = NEW.service_id
          AND revision.runtime_profile = 'codex.app-server/v1'
          AND revision.state = 'draft'
          AND revision.version = NEW.expected_version
    ) THEN
        RAISE EXCEPTION 'governed publication request scope is invalid';
    END IF;
    RETURN NEW;
END; $$;

-- dataground:down

DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM service_publication_operations WHERE state_machine_version = 4) THEN
        RAISE EXCEPTION 'authorized publication history prevents downgrade';
    END IF;
END; $$;
ALTER TABLE service_publication_operations
    DROP CONSTRAINT governed_publication_states,
    ADD CONSTRAINT governed_publication_states CHECK (
        state_machine_version <> 3 OR observed_state IN ('queued', 'validating', 'published', 'failed', 'cancelled')
    );
CREATE OR REPLACE FUNCTION enforce_governed_publication_request()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'governed publication requests are immutable';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM service_publication_operations operation
        JOIN service_revisions revision
          ON revision.isolation_domain_id = operation.isolation_domain_id
         AND revision.id = operation.revision_id
        WHERE operation.isolation_domain_id = NEW.isolation_domain_id
          AND operation.id = NEW.operation_id
          AND operation.revision_id = NEW.revision_id
          AND operation.state_machine_version = 3
          AND operation.observed_state = 'queued'
          AND revision.service_id = NEW.service_id
          AND revision.runtime_profile = 'codex.app-server/v1'
          AND revision.state = 'draft'
          AND revision.version = NEW.expected_version
    ) THEN
        RAISE EXCEPTION 'governed publication request scope is invalid';
    END IF;
    RETURN NEW;
END; $$;
