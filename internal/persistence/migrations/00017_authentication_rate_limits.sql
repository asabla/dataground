-- dataground:up

CREATE TABLE authentication_rate_limit_buckets (
    scope text NOT NULL,
    subject_digest bytea NOT NULL,
    policy_digest bytea NOT NULL,
    theoretical_arrival_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (scope, subject_digest),
    CHECK (scope IN ('global', 'domain', 'credential')),
    CHECK (octet_length(subject_digest) = 32),
    CHECK (octet_length(policy_digest) = 32)
);

CREATE INDEX authentication_rate_limit_buckets_reclamation_idx
    ON authentication_rate_limit_buckets (updated_at, scope, subject_digest)
    WHERE scope <> 'global';

-- dataground:down

DROP TABLE authentication_rate_limit_buckets;
