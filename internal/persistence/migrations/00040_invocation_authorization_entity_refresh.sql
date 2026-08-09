-- dataground:up

CREATE TABLE invocation_authorization_entity_generations (
    contract text NOT NULL,
    isolation_domain_id text NOT NULL,
    service_id text NOT NULL,
    revision_id text NOT NULL,
    generation bigint NOT NULL,
    entity_digest bytea NOT NULL,
    cedar_entities bytea NOT NULL,
    published_by text NOT NULL,
    publication_correlation_id text NOT NULL,
    reason_digest bytea NOT NULL,
    published_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (isolation_domain_id, service_id, revision_id, generation),
    UNIQUE (publication_correlation_id),
    FOREIGN KEY (isolation_domain_id, service_id, revision_id)
        REFERENCES invocation_authorization_policies (
            isolation_domain_id,
            service_id,
            revision_id
        ),
    CHECK (contract = 'dataground.invocation-authorization-entity-generation/v1'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (service_id ~ '^svc_[0-9a-z]{20,32}$'),
    CHECK (revision_id ~ '^rev_[0-9a-z]{20,32}$'),
    CHECK (generation > 0),
    CHECK (octet_length(entity_digest) = 32),
    CHECK (octet_length(cedar_entities) BETWEEN 1 AND 1048576),
    CHECK (published_by ~ '^[a-z][a-z0-9_-]{2,127}$'),
    CHECK (publication_correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (octet_length(reason_digest) = 32)
);

CREATE TABLE invocation_authorization_entity_activations (
    contract text NOT NULL,
    isolation_domain_id text NOT NULL,
    service_id text NOT NULL,
    revision_id text NOT NULL,
    generation bigint NOT NULL,
    installed_policy_digest bytea NOT NULL,
    effective_policy_digest bytea NOT NULL,
    activated_by text NOT NULL,
    activation_correlation_id text NOT NULL,
    reason_digest bytea NOT NULL,
    activated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (isolation_domain_id, service_id, revision_id, generation),
    UNIQUE (activation_correlation_id),
    FOREIGN KEY (isolation_domain_id, service_id, revision_id, generation)
        REFERENCES invocation_authorization_entity_generations (
            isolation_domain_id,
            service_id,
            revision_id,
            generation
        ),
    CHECK (contract = 'dataground.invocation-authorization-entity-activation/v1'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (service_id ~ '^svc_[0-9a-z]{20,32}$'),
    CHECK (revision_id ~ '^rev_[0-9a-z]{20,32}$'),
    CHECK (generation > 0),
    CHECK (octet_length(installed_policy_digest) = 32),
    CHECK (octet_length(effective_policy_digest) = 32),
    CHECK (activated_by ~ '^[a-z][a-z0-9_-]{2,127}$'),
    CHECK (activation_correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (octet_length(reason_digest) = 32)
);

CREATE FUNCTION reject_invocation_authorization_entity_refresh_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'invocation authorization entity refresh evidence is append-only';
END;
$$;

CREATE TRIGGER invocation_authorization_entity_generations_append_only
BEFORE UPDATE OR DELETE ON invocation_authorization_entity_generations
FOR EACH ROW EXECUTE FUNCTION reject_invocation_authorization_entity_refresh_mutation();

CREATE TRIGGER invocation_authorization_entity_activations_append_only
BEFORE UPDATE OR DELETE ON invocation_authorization_entity_activations
FOR EACH ROW EXECUTE FUNCTION reject_invocation_authorization_entity_refresh_mutation();

-- dataground:down

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM invocation_authorization_entity_activations)
        OR EXISTS (SELECT 1 FROM invocation_authorization_entity_generations) THEN
        RAISE EXCEPTION 'cannot remove invocation authorization entity refresh evidence';
    END IF;
END;
$$;

DROP TABLE invocation_authorization_entity_activations;
DROP TABLE invocation_authorization_entity_generations;
DROP FUNCTION reject_invocation_authorization_entity_refresh_mutation();
