-- dataground:up

ALTER TABLE service_publication_operations
    ADD CONSTRAINT governed_publication_states CHECK (
        state_machine_version <> 3 OR observed_state IN ('queued', 'validating', 'published', 'failed', 'cancelled')
    );

CREATE TABLE governed_publication_requests (
    isolation_domain_id text NOT NULL,
    operation_id text NOT NULL,
    revision_id text NOT NULL,
    service_id text NOT NULL,
    expected_version bigint NOT NULL CHECK (expected_version > 0),
    plan_digest text NOT NULL CHECK (plan_digest ~ '^sha256:[a-f0-9]{64}$'),
    policy_digest text NOT NULL CHECK (policy_digest ~ '^sha256:[a-f0-9]{64}$'),
    verification_digest text NOT NULL CHECK (verification_digest ~ '^sha256:[a-f0-9]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(created_at)),
    PRIMARY KEY (isolation_domain_id, operation_id),
    UNIQUE (isolation_domain_id, revision_id),
    FOREIGN KEY (isolation_domain_id, operation_id)
        REFERENCES service_publication_operations (isolation_domain_id, id),
    FOREIGN KEY (isolation_domain_id, revision_id)
        REFERENCES service_revisions (isolation_domain_id, id),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (operation_id ~ '^op_[0-9a-z]{20,32}$'),
    CHECK (revision_id ~ '^rev_[0-9a-z]{20,32}$'),
    CHECK (service_id ~ '^svc_[0-9a-z]{20,32}$')
);

CREATE FUNCTION enforce_governed_publication_request()
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

CREATE TRIGGER governed_publication_requests_immutable
BEFORE INSERT OR UPDATE OR DELETE ON governed_publication_requests
FOR EACH ROW EXECUTE FUNCTION enforce_governed_publication_request();

-- dataground:down

DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM governed_publication_requests)
       OR EXISTS (SELECT 1 FROM service_publication_operations WHERE state_machine_version = 3) THEN
        RAISE EXCEPTION 'cannot remove governed publication history';
    END IF;
END; $$;

DROP TABLE governed_publication_requests;
DROP FUNCTION enforce_governed_publication_request();
ALTER TABLE service_publication_operations DROP CONSTRAINT governed_publication_states;
