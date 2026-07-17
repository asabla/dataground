-- dataground:up

CREATE TABLE agent_services (
    isolation_domain_id text NOT NULL,
    id text NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    generation bigint NOT NULL DEFAULT 1 CHECK (generation >= 1),
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    created_by text NOT NULL,
    PRIMARY KEY (isolation_domain_id, id),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (id ~ '^svc_[0-9a-z]{20,32}$'),
    CHECK (length(name) BETWEEN 1 AND 128)
);

CREATE TABLE service_revisions (
    isolation_domain_id text NOT NULL,
    id text NOT NULL,
    service_id text NOT NULL,
    revision_number bigint NOT NULL CHECK (revision_number >= 1),
    state text NOT NULL CHECK (state IN ('draft', 'published', 'retired')),
    runtime_profile text NOT NULL,
    required_capabilities text[] NOT NULL DEFAULT '{}',
    input_schema jsonb,
    output_schema jsonb,
    published_at timestamptz,
    generation bigint NOT NULL DEFAULT 1 CHECK (generation >= 1),
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    created_by text NOT NULL,
    PRIMARY KEY (isolation_domain_id, id),
    UNIQUE (isolation_domain_id, service_id, revision_number),
    FOREIGN KEY (isolation_domain_id, service_id)
        REFERENCES agent_services (isolation_domain_id, id),
    CHECK (id ~ '^rev_[0-9a-z]{20,32}$')
);

CREATE TABLE service_aliases (
    isolation_domain_id text NOT NULL,
    id text NOT NULL,
    service_id text NOT NULL,
    name text NOT NULL,
    revision_id text NOT NULL,
    generation bigint NOT NULL DEFAULT 1 CHECK (generation >= 1),
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    created_by text NOT NULL,
    PRIMARY KEY (isolation_domain_id, service_id, name),
    UNIQUE (isolation_domain_id, id),
    FOREIGN KEY (isolation_domain_id, service_id)
        REFERENCES agent_services (isolation_domain_id, id),
    FOREIGN KEY (isolation_domain_id, revision_id)
        REFERENCES service_revisions (isolation_domain_id, id),
    CHECK (name ~ '^[a-z](?:[a-z0-9-]*[a-z0-9])?$')
);

CREATE TABLE invocations (
    isolation_domain_id text NOT NULL,
    id text NOT NULL,
    service_id text NOT NULL,
    revision_id text NOT NULL,
    alias text NOT NULL,
    state text NOT NULL CHECK (state IN (
        'accepted', 'running', 'waiting', 'cancelling',
        'succeeded', 'failed', 'cancelled', 'unknown'
    )),
    input jsonb NOT NULL,
    result jsonb,
    error jsonb,
    usage jsonb,
    correlation_id text NOT NULL,
    operation_id text NOT NULL,
    completed_at timestamptz,
    generation bigint NOT NULL DEFAULT 1 CHECK (generation >= 1),
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    created_by text NOT NULL,
    PRIMARY KEY (isolation_domain_id, id),
    UNIQUE (operation_id),
    FOREIGN KEY (isolation_domain_id, service_id)
        REFERENCES agent_services (isolation_domain_id, id),
    FOREIGN KEY (isolation_domain_id, revision_id)
        REFERENCES service_revisions (isolation_domain_id, id),
    CHECK (id ~ '^inv_[0-9a-z]{20,32}$'),
    CHECK (operation_id ~ '^op_[0-9a-z]{20,32}$')
);

CREATE TABLE invocation_events (
    isolation_domain_id text NOT NULL,
    invocation_id text NOT NULL,
    id text NOT NULL,
    sequence bigint NOT NULL CHECK (sequence >= 1),
    schema_version text NOT NULL,
    event_type text NOT NULL,
    occurred_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL,
    correlation_id text NOT NULL,
    actor_id text NOT NULL,
    service_id text NOT NULL,
    revision_id text NOT NULL,
    payload jsonb NOT NULL,
    extensions jsonb,
    PRIMARY KEY (isolation_domain_id, invocation_id, sequence),
    UNIQUE (id),
    FOREIGN KEY (isolation_domain_id, invocation_id)
        REFERENCES invocations (isolation_domain_id, id) ON DELETE CASCADE,
    CHECK (id ~ '^evt_[0-9a-z]{20,32}$'),
    CHECK (schema_version = 'dataground.event/v1')
);

CREATE TABLE artifacts (
    isolation_domain_id text NOT NULL,
    id text NOT NULL,
    invocation_id text NOT NULL,
    name text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('file', 'structured-output', 'event-payload', 'log', 'other')),
    media_type text NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    digest text NOT NULL,
    state text NOT NULL CHECK (state IN ('pending', 'available', 'failed', 'deleted')),
    sensitive boolean NOT NULL,
    generation bigint NOT NULL DEFAULT 1 CHECK (generation >= 1),
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    created_by text NOT NULL,
    PRIMARY KEY (isolation_domain_id, id),
    FOREIGN KEY (isolation_domain_id, invocation_id)
        REFERENCES invocations (isolation_domain_id, id) ON DELETE CASCADE,
    CHECK (id ~ '^art_[0-9a-z]{20,32}$'),
    CHECK (digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE TABLE idempotency_records (
    isolation_domain_id text NOT NULL,
    method text NOT NULL,
    request_path text NOT NULL,
    idempotency_key text NOT NULL,
    request_digest bytea NOT NULL,
    response_status integer,
    response_body bytea,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (isolation_domain_id, method, request_path, idempotency_key),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (length(idempotency_key) BETWEEN 8 AND 128)
);

CREATE TABLE service_publication_operations (
    isolation_domain_id text NOT NULL,
    id text NOT NULL,
    revision_id text NOT NULL,
    command text NOT NULL CHECK (command IN ('publish', 'cancel', 'repair')),
    desired_state text NOT NULL CHECK (desired_state IN ('published', 'cancelled')),
    observed_state text NOT NULL CHECK (observed_state IN (
        'queued', 'validating', 'applying', 'observing',
        'published', 'failed', 'cancelled'
    )),
    state_machine_version integer NOT NULL CHECK (state_machine_version >= 1),
    generation bigint NOT NULL DEFAULT 1 CHECK (generation >= 1),
    attempt integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    lease_owner text,
    lease_token bigint NOT NULL DEFAULT 0 CHECK (lease_token >= 0),
    lease_expires_at timestamptz,
    due_at timestamptz NOT NULL,
    deadline_at timestamptz NOT NULL,
    error_classification text CHECK (error_classification IN ('retryable', 'terminal', 'cancelled', 'unknown')),
    error jsonb,
    terminal_result jsonb,
    correlation_id text NOT NULL,
    actor_id text NOT NULL,
    last_transition_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (isolation_domain_id, id),
    UNIQUE (isolation_domain_id, revision_id),
    FOREIGN KEY (isolation_domain_id, revision_id)
        REFERENCES service_revisions (isolation_domain_id, id) ON DELETE CASCADE,
    CHECK (id ~ '^op_[0-9a-z]{20,32}$')
);

CREATE TABLE invocation_execution_operations (
    isolation_domain_id text NOT NULL,
    id text NOT NULL,
    invocation_id text NOT NULL,
    command text NOT NULL CHECK (command IN ('invoke', 'cancel', 'repair')),
    desired_state text NOT NULL CHECK (desired_state IN ('succeeded', 'cancelled')),
    observed_state text NOT NULL CHECK (observed_state IN (
        'queued', 'starting', 'running', 'waiting', 'cancelling', 'observing',
        'succeeded', 'failed', 'cancelled'
    )),
    state_machine_version integer NOT NULL CHECK (state_machine_version >= 1),
    generation bigint NOT NULL DEFAULT 1 CHECK (generation >= 1),
    attempt integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    lease_owner text,
    lease_token bigint NOT NULL DEFAULT 0 CHECK (lease_token >= 0),
    lease_expires_at timestamptz,
    due_at timestamptz NOT NULL,
    deadline_at timestamptz NOT NULL,
    error_classification text CHECK (error_classification IN ('retryable', 'terminal', 'cancelled', 'unknown')),
    error jsonb,
    terminal_result jsonb,
    correlation_id text NOT NULL,
    actor_id text NOT NULL,
    last_transition_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (isolation_domain_id, id),
    UNIQUE (isolation_domain_id, invocation_id),
    FOREIGN KEY (isolation_domain_id, invocation_id)
        REFERENCES invocations (isolation_domain_id, id) ON DELETE CASCADE,
    CHECK (id ~ '^op_[0-9a-z]{20,32}$')
);

CREATE TABLE outbox_events (
    id text PRIMARY KEY,
    isolation_domain_id text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    correlation_id text NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'dead_letter')),
    attempt integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    available_at timestamptz NOT NULL,
    lease_owner text,
    lease_token bigint NOT NULL DEFAULT 0 CHECK (lease_token >= 0),
    lease_expires_at timestamptz,
    created_at timestamptz NOT NULL,
    delivered_at timestamptz,
    CHECK (id ~ '^out_[0-9a-z]{20,32}$'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$')
);

CREATE TABLE inbox_records (
    isolation_domain_id text NOT NULL,
    source_kind text NOT NULL CHECK (source_kind IN ('command', 'callback', 'provider-observation')),
    deduplication_id text NOT NULL,
    payload_digest bytea NOT NULL,
    result jsonb,
    processed_at timestamptz,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (isolation_domain_id, source_kind, deduplication_id)
);

CREATE TABLE external_effects (
    isolation_domain_id text NOT NULL,
    effect_id text NOT NULL,
    operation_kind text NOT NULL CHECK (operation_kind IN ('service-publication', 'invocation-execution')),
    operation_id text NOT NULL,
    phase text NOT NULL,
    request_digest bytea NOT NULL,
    status text NOT NULL CHECK (status IN ('prepared', 'unknown', 'succeeded', 'failed')),
    attempt integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    observation jsonb,
    last_error_code text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (isolation_domain_id, effect_id),
    UNIQUE (isolation_domain_id, operation_kind, operation_id, phase),
    CHECK (effect_id ~ '^eff_[0-9a-z]{20,32}$')
);

-- Persistent receipts model the reference runtime's provider-side idempotency
-- boundary. Production adapters use their own native observation contract.
CREATE TABLE reference_runtime_receipts (
    isolation_domain_id text NOT NULL,
    effect_id text NOT NULL,
    phase text NOT NULL,
    result jsonb NOT NULL,
    applied_at timestamptz NOT NULL,
    PRIMARY KEY (isolation_domain_id, effect_id),
    CHECK (effect_id ~ '^eff_[0-9a-z]{20,32}$')
);

CREATE TABLE audit_records (
    id text PRIMARY KEY,
    isolation_domain_id text NOT NULL,
    actor_id text NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('accepted', 'succeeded', 'failed', 'cancelled', 'denied')),
    correlation_id text NOT NULL,
    operation_id text,
    safe_metadata jsonb NOT NULL DEFAULT '{}',
    occurred_at timestamptz NOT NULL,
    CHECK (id ~ '^aud_[0-9a-z]{20,32}$'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$')
);

CREATE INDEX service_publication_due_idx
    ON service_publication_operations (due_at, updated_at, isolation_domain_id)
    WHERE observed_state NOT IN ('published', 'failed', 'cancelled');

CREATE INDEX invocation_execution_due_idx
    ON invocation_execution_operations (due_at, updated_at, isolation_domain_id)
    WHERE observed_state NOT IN ('succeeded', 'failed', 'cancelled', 'waiting');

CREATE INDEX outbox_pending_idx
    ON outbox_events (available_at, created_at, isolation_domain_id)
    WHERE status = 'pending';

CREATE INDEX invocation_events_replay_idx
    ON invocation_events (isolation_domain_id, invocation_id, sequence);

CREATE INDEX audit_resource_idx
    ON audit_records (isolation_domain_id, resource_type, resource_id, occurred_at);

-- dataground:down

DROP TABLE audit_records;
DROP TABLE reference_runtime_receipts;
DROP TABLE external_effects;
DROP TABLE inbox_records;
DROP TABLE outbox_events;
DROP TABLE invocation_execution_operations;
DROP TABLE service_publication_operations;
DROP TABLE idempotency_records;
DROP TABLE artifacts;
DROP TABLE invocation_events;
DROP TABLE invocations;
DROP TABLE service_aliases;
DROP TABLE service_revisions;
DROP TABLE agent_services;
