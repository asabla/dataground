-- dataground:up

CREATE TABLE authentication_attempts (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    isolation_domain_id text NOT NULL,
    principal_id text,
    principal_kind text,
    method text NOT NULL
        CHECK (method IN ('development-bearer', 'oidc')),
    outcome text NOT NULL
        CHECK (outcome IN ('authenticated', 'rejected', 'scope-denied', 'unavailable')),
    correlation_id text NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (isolation_domain_id, correlation_id),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (principal_id IS NULL OR principal_id ~ '^[a-z][a-z0-9_-]{2,127}$'),
    CHECK (
        principal_kind IS NULL
        OR principal_kind IN ('human', 'service')
    ),
    CHECK ((principal_id IS NULL) = (principal_kind IS NULL)),
    CHECK (
        (outcome = 'authenticated' AND principal_id IS NOT NULL)
        OR (outcome <> 'authenticated' AND principal_id IS NULL)
    ),
    CHECK (method <> 'development-bearer' OR principal_kind IS NULL OR principal_kind = 'human'),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$')
);

CREATE INDEX authentication_attempts_scope_sequence_idx
    ON authentication_attempts (isolation_domain_id, sequence);

CREATE FUNCTION reject_authentication_attempt_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'authentication attempts are append-only';
END;
$$;

CREATE TRIGGER authentication_attempts_append_only
BEFORE UPDATE OR DELETE ON authentication_attempts
FOR EACH ROW EXECUTE FUNCTION reject_authentication_attempt_mutation();

-- dataground:down

DROP TABLE authentication_attempts;
DROP FUNCTION reject_authentication_attempt_mutation();
