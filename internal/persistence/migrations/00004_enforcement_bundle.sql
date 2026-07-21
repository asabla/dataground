-- dataground:up

CREATE TABLE service_revision_enforcement_bundles (
    isolation_domain_id text NOT NULL,
    id text NOT NULL,
    revision_id text NOT NULL,
    schema_version text NOT NULL CHECK (schema_version = 'dataground.enforcement-bundle/v1'),
    artifact_digest text NOT NULL,
    media_type text NOT NULL CHECK (media_type = 'application/yaml'),
    size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 1 AND 4194304),
    object_key text NOT NULL,
    producer text NOT NULL CHECK (producer = 'rosetta'),
    source_revision text NOT NULL,
    compiler_version text NOT NULL,
    catalog_version text NOT NULL,
    target_contract_version text NOT NULL,
    compilation_mode text NOT NULL CHECK (compilation_mode = 'strict'),
    input_digest text NOT NULL,
    binding_digest text NOT NULL,
    bound_by text NOT NULL,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (isolation_domain_id, id),
    UNIQUE (isolation_domain_id, object_key),
    FOREIGN KEY (isolation_domain_id, revision_id)
        REFERENCES service_revisions (isolation_domain_id, id) ON DELETE CASCADE,
    CHECK (id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (source_revision ~ '^[0-9a-f]{40}$'),
    CHECK (length(compiler_version) BETWEEN 1 AND 128),
    CHECK (length(catalog_version) BETWEEN 1 AND 128),
    CHECK (length(target_contract_version) BETWEEN 1 AND 128),
    CHECK (input_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (binding_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (object_key ~ '^enforcement-bundles/v1/iso_[0-9a-z]{20,32}/rev_[0-9a-z]{20,32}/[A-Za-z0-9][A-Za-z0-9._-]{0,127}/[0-9a-f]{64}[.]yaml$'),
    CHECK (length(bound_by) BETWEEN 1 AND 256),
    CHECK (length(correlation_id) BETWEEN 1 AND 256)
);

CREATE INDEX service_revision_enforcement_bundles_revision_idx
    ON service_revision_enforcement_bundles (isolation_domain_id, revision_id);

-- dataground:down

DROP TABLE service_revision_enforcement_bundles;
