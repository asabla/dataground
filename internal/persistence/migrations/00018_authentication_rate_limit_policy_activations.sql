-- dataground:up

CREATE TABLE authentication_rate_limit_policy_activations (
    generation bigint PRIMARY KEY,
    contract text NOT NULL,
    policy_digest bytea NOT NULL,
    window_nanoseconds bigint NOT NULL,
    global_burst bigint NOT NULL,
    isolation_domain_burst bigint NOT NULL,
    credential_burst bigint NOT NULL,
    activated_by text NOT NULL,
    activation_correlation_id text NOT NULL UNIQUE,
    reason_digest bytea NOT NULL,
    activated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (generation > 0),
    CHECK (contract = 'dataground.authentication-rate-limit-policy/v1'),
    CHECK (octet_length(policy_digest) = 32),
    CHECK (window_nanoseconds BETWEEN 1000000000 AND 86400000000000),
    CHECK (global_burst BETWEEN 1 AND 1000000),
    CHECK (isolation_domain_burst BETWEEN 1 AND global_burst),
    CHECK (credential_burst BETWEEN 1 AND isolation_domain_burst),
    CHECK (activated_by ~ '^[a-z][a-z0-9_-]{2,127}$'),
    CHECK (activation_correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (octet_length(reason_digest) = 32)
);

CREATE FUNCTION reject_authentication_rate_limit_policy_activation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'authentication rate limit policy activations are append-only';
END;
$$;

CREATE TRIGGER authentication_rate_limit_policy_activations_append_only
BEFORE UPDATE OR DELETE ON authentication_rate_limit_policy_activations
FOR EACH ROW EXECUTE FUNCTION reject_authentication_rate_limit_policy_activation_mutation();

TRUNCATE authentication_rate_limit_buckets;

ALTER TABLE authentication_rate_limit_buckets
    DROP CONSTRAINT authentication_rate_limit_buckets_pkey,
    ADD COLUMN policy_generation bigint NOT NULL,
    ADD CONSTRAINT authentication_rate_limit_buckets_pkey
        PRIMARY KEY (policy_generation, scope, subject_digest),
    ADD CONSTRAINT authentication_rate_limit_buckets_policy_generation_fk
        FOREIGN KEY (policy_generation)
        REFERENCES authentication_rate_limit_policy_activations (generation);

CREATE INDEX authentication_rate_limit_buckets_generation_reclamation_idx
    ON authentication_rate_limit_buckets (policy_generation, updated_at, scope, subject_digest);

-- dataground:down

DROP INDEX authentication_rate_limit_buckets_generation_reclamation_idx;

ALTER TABLE authentication_rate_limit_buckets
    DROP CONSTRAINT authentication_rate_limit_buckets_policy_generation_fk,
    DROP CONSTRAINT authentication_rate_limit_buckets_pkey,
    DROP COLUMN policy_generation,
    ADD CONSTRAINT authentication_rate_limit_buckets_pkey
        PRIMARY KEY (scope, subject_digest);

DROP TABLE authentication_rate_limit_policy_activations;
DROP FUNCTION reject_authentication_rate_limit_policy_activation_mutation();
