-- dataground:up

CREATE TABLE invocation_artifact_objects (
    isolation_domain_id text NOT NULL,
    id text NOT NULL,
    invocation_id text NOT NULL,
    operation_id text NOT NULL,
    effect_id text NOT NULL,
    schema_version text NOT NULL
        CHECK (schema_version = 'dataground.invocation-artifact/v1'),
    name text NOT NULL,
    kind text NOT NULL
        CHECK (kind IN ('file', 'structured-output', 'event-payload', 'log', 'other')),
    media_type text NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    artifact_digest text NOT NULL,
    sensitive boolean NOT NULL,
    object_key text NOT NULL,
    lease_owner text NOT NULL,
    fencing_token bigint NOT NULL CHECK (fencing_token > 0),
    bound_by text NOT NULL,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (isolation_domain_id, id),
    UNIQUE (isolation_domain_id, object_key),
    FOREIGN KEY (isolation_domain_id, id)
        REFERENCES artifacts (isolation_domain_id, id) ON DELETE CASCADE,
    FOREIGN KEY (isolation_domain_id, invocation_id)
        REFERENCES invocations (isolation_domain_id, id) ON DELETE CASCADE,
    FOREIGN KEY (isolation_domain_id, operation_id)
        REFERENCES invocation_execution_operations (isolation_domain_id, id) ON DELETE CASCADE,
    FOREIGN KEY (isolation_domain_id, effect_id)
        REFERENCES external_effects (isolation_domain_id, effect_id) ON DELETE CASCADE,
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (id ~ '^art_[0-9a-z]{20,32}$'),
    CHECK (invocation_id ~ '^inv_[0-9a-z]{20,32}$'),
    CHECK (operation_id ~ '^op_[0-9a-z]{20,32}$'),
    CHECK (effect_id ~ '^eff_[0-9a-z]{20,32}$'),
    CHECK (length(name) BETWEEN 1 AND 255),
    CHECK (length(media_type) BETWEEN 1 AND 255),
    CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (
        object_key ~
        '^invocation-artifacts/v1/iso_[0-9a-z]{20,32}/inv_[0-9a-z]{20,32}/art_[0-9a-z]{20,32}/[0-9a-f]{64}$'
    ),
    CHECK (length(lease_owner) BETWEEN 1 AND 256),
    CHECK (length(bound_by) BETWEEN 1 AND 256),
    CHECK (length(correlation_id) BETWEEN 1 AND 256)
);

CREATE INDEX invocation_artifact_objects_invocation_idx
    ON invocation_artifact_objects (isolation_domain_id, invocation_id, id);

-- dataground:down

DROP TABLE invocation_artifact_objects;
