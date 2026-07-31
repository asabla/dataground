-- dataground:up

CREATE TABLE invocation_authorization_policies (
    isolation_domain_id text NOT NULL,
    service_id text NOT NULL,
    revision_id text NOT NULL,
    contract text NOT NULL,
    policy_set_id text NOT NULL,
    policy_digest bytea NOT NULL,
    cedar_schema bytea NOT NULL,
    cedar_policies bytea NOT NULL,
    installed_by text NOT NULL,
    installation_correlation_id text NOT NULL,
    reason_digest bytea NOT NULL,
    installed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (isolation_domain_id, service_id, revision_id),
    FOREIGN KEY (isolation_domain_id, revision_id)
        REFERENCES service_revisions (isolation_domain_id, id),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (service_id ~ '^svc_[0-9a-z]{20,32}$'),
    CHECK (revision_id ~ '^rev_[0-9a-z]{20,32}$'),
    CHECK (contract = 'dataground.invocation-authorization-policy/v1'),
    CHECK (policy_set_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    CHECK (octet_length(policy_digest) = 32),
    CHECK (octet_length(cedar_schema) BETWEEN 1 AND 1048576),
    CHECK (octet_length(cedar_policies) BETWEEN 1 AND 1048576),
    CHECK (installed_by ~ '^[a-z][a-z0-9_-]{2,127}$'),
    CHECK (installation_correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (octet_length(reason_digest) = 32)
);

CREATE FUNCTION enforce_invocation_authorization_policy_revision_scope()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM service_revisions
        WHERE isolation_domain_id = NEW.isolation_domain_id
          AND service_id = NEW.service_id
          AND id = NEW.revision_id
    ) THEN
        RAISE EXCEPTION 'invocation authorization policy scope does not match a service revision';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER invocation_authorization_policies_revision_scope
BEFORE INSERT ON invocation_authorization_policies
FOR EACH ROW EXECUTE FUNCTION enforce_invocation_authorization_policy_revision_scope();

CREATE FUNCTION reject_invocation_authorization_policy_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'invocation authorization policies are append-only';
END;
$$;

CREATE TRIGGER invocation_authorization_policies_append_only
BEFORE UPDATE OR DELETE ON invocation_authorization_policies
FOR EACH ROW EXECUTE FUNCTION reject_invocation_authorization_policy_mutation();

-- dataground:down

DROP TABLE invocation_authorization_policies;
DROP FUNCTION reject_invocation_authorization_policy_mutation();
DROP FUNCTION enforce_invocation_authorization_policy_revision_scope();
