-- dataground:up

CREATE TABLE invocation_runtime_attempts (
    isolation_domain_id text NOT NULL,
    operation_id text NOT NULL,
    effect_id text NOT NULL,
    lease_owner text NOT NULL CHECK (lease_owner <> ''),
    fencing_token bigint NOT NULL CHECK (fencing_token > 0),
    status text NOT NULL CHECK (status IN ('reserved', 'succeeded')),
    result jsonb,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    PRIMARY KEY (isolation_domain_id, operation_id),
    UNIQUE (isolation_domain_id, effect_id),
    FOREIGN KEY (isolation_domain_id, operation_id)
        REFERENCES invocation_execution_operations (isolation_domain_id, id)
        ON DELETE CASCADE,
    CHECK (
        (status = 'reserved' AND result IS NULL AND completed_at IS NULL)
        OR (status = 'succeeded' AND result IS NOT NULL AND completed_at IS NOT NULL)
    )
);

-- dataground:down

DROP TABLE invocation_runtime_attempts;
