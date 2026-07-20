-- dataground:up

CREATE TABLE execution_gateways (
    isolation_domain_id text NOT NULL,
    id text NOT NULL,
    driver text NOT NULL,
    endpoint text NOT NULL,
    state text NOT NULL CHECK (state IN ('active', 'draining', 'unavailable', 'lost')),
    capabilities text[] NOT NULL DEFAULT '{}',
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    registered_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (isolation_domain_id, id),
    UNIQUE (isolation_domain_id, endpoint),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (id ~ '^gtw_[0-9a-z]{20,32}$'),
    CHECK (driver ~ '^[a-z][a-z0-9-]{0,63}$'),
    CHECK (length(endpoint) BETWEEN 1 AND 2048),
    CHECK (array_position(capabilities, '') IS NULL)
);

CREATE TABLE execution_placements (
    isolation_domain_id text NOT NULL,
    id text NOT NULL,
    operation_id text NOT NULL,
    gateway_id text NOT NULL,
    required_capabilities text[] NOT NULL DEFAULT '{}',
    state text NOT NULL CHECK (state IN ('reserved', 'active', 'released', 'lost')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (isolation_domain_id, id),
    UNIQUE (isolation_domain_id, operation_id),
    FOREIGN KEY (isolation_domain_id, gateway_id)
        REFERENCES execution_gateways (isolation_domain_id, id),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (id ~ '^plc_[0-9a-z]{20,32}$'),
    CHECK (operation_id ~ '^op_[0-9a-z]{20,32}$'),
    CHECK (array_position(required_capabilities, '') IS NULL)
);

CREATE TABLE execution_instances (
    isolation_domain_id text NOT NULL,
    id text NOT NULL,
    placement_id text NOT NULL,
    operation_id text NOT NULL,
    gateway_id text NOT NULL,
    sandbox_name text NOT NULL,
    observed_state text NOT NULL CHECK (observed_state IN (
        'provisioning', 'ready', 'running', 'waiting', 'deleting',
        'terminated', 'error', 'unknown'
    )),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    terminated_at timestamptz,
    PRIMARY KEY (isolation_domain_id, id),
    UNIQUE (isolation_domain_id, operation_id),
    UNIQUE (isolation_domain_id, gateway_id, sandbox_name),
    FOREIGN KEY (isolation_domain_id, placement_id)
        REFERENCES execution_placements (isolation_domain_id, id),
    FOREIGN KEY (isolation_domain_id, gateway_id)
        REFERENCES execution_gateways (isolation_domain_id, id),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (id ~ '^exe_[0-9a-z]{20,32}$'),
    CHECK (operation_id ~ '^op_[0-9a-z]{20,32}$'),
    CHECK (length(sandbox_name) BETWEEN 1 AND 128),
    CHECK ((observed_state = 'terminated') = (terminated_at IS NOT NULL))
);

CREATE INDEX execution_gateway_placement_idx
    ON execution_placements (isolation_domain_id, gateway_id, state, created_at);

CREATE INDEX execution_instance_gateway_idx
    ON execution_instances (isolation_domain_id, gateway_id, observed_state, updated_at);

-- dataground:down

DROP TABLE execution_instances;
DROP TABLE execution_placements;
DROP TABLE execution_gateways;
