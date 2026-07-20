-- dataground:up

CREATE TABLE service_revision_execution_plans (
    isolation_domain_id text NOT NULL,
    revision_id text NOT NULL,
    schema_version text NOT NULL CHECK (schema_version = 'dataground.execution-plan/v1'),
    runtime_profile text NOT NULL,
    environment_revision_id text NOT NULL,
    image_reference text NOT NULL,
    environment_manifest_digest text NOT NULL,
    enforcement_bundle_id text NOT NULL,
    enforcement_bundle_digest text NOT NULL,
    runtime_matrix_id text NOT NULL,
    runtime_matrix_digest text NOT NULL,
    provider_profiles text[] NOT NULL DEFAULT '{}',
    required_capabilities text[] NOT NULL DEFAULT '{}',
    plan_digest text NOT NULL,
    bound_by text NOT NULL,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (isolation_domain_id, revision_id),
    FOREIGN KEY (isolation_domain_id, revision_id)
        REFERENCES service_revisions (isolation_domain_id, id) ON DELETE CASCADE,
    CHECK (length(runtime_profile) BETWEEN 1 AND 128),
    CHECK (environment_revision_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    CHECK (image_reference ~ '^[^[:space:]@]+@sha256:[0-9a-f]{64}$'),
    CHECK (environment_manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (enforcement_bundle_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    CHECK (enforcement_bundle_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (runtime_matrix_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    CHECK (runtime_matrix_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (array_position(provider_profiles, '') IS NULL),
    CHECK (cardinality(provider_profiles) <= 64),
    CHECK (array_position(required_capabilities, '') IS NULL),
    CHECK (cardinality(required_capabilities) <= 256),
    CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (length(bound_by) BETWEEN 1 AND 256),
    CHECK (length(correlation_id) BETWEEN 1 AND 256)
);

-- dataground:down

DROP TABLE service_revision_execution_plans;
