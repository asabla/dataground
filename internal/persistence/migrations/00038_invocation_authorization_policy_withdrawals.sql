-- dataground:up

CREATE TABLE invocation_authorization_policy_withdrawals (
    contract text NOT NULL,
    isolation_domain_id text NOT NULL,
    service_id text NOT NULL,
    revision_id text NOT NULL,
    policy_digest bytea NOT NULL,
    withdrawn_by text NOT NULL,
    reason_digest bytea NOT NULL,
    correlation_id text NOT NULL,
    withdrawn_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (isolation_domain_id, service_id, revision_id),
    UNIQUE (correlation_id),
    FOREIGN KEY (isolation_domain_id, service_id, revision_id)
        REFERENCES invocation_authorization_policies (
            isolation_domain_id,
            service_id,
            revision_id
        ),
    CHECK (contract = 'dataground.invocation-authorization-policy-withdrawal/v1'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (service_id ~ '^svc_[0-9a-z]{20,32}$'),
    CHECK (revision_id ~ '^rev_[0-9a-z]{20,32}$'),
    CHECK (octet_length(policy_digest) = 32),
    CHECK (withdrawn_by ~ '^[a-z][a-z0-9_-]{2,127}$'),
    CHECK (octet_length(reason_digest) = 32),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$')
);

CREATE FUNCTION enforce_invocation_authorization_policy_withdrawal_digest()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM invocation_authorization_policies
        WHERE isolation_domain_id = NEW.isolation_domain_id
          AND service_id = NEW.service_id
          AND revision_id = NEW.revision_id
          AND policy_digest = NEW.policy_digest
    ) THEN
        RAISE EXCEPTION 'invocation authorization policy withdrawal digest does not match';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER invocation_authorization_policy_withdrawals_exact_digest
BEFORE INSERT ON invocation_authorization_policy_withdrawals
FOR EACH ROW EXECUTE FUNCTION enforce_invocation_authorization_policy_withdrawal_digest();

CREATE FUNCTION reject_invocation_authorization_policy_withdrawal_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'invocation authorization policy withdrawals are append-only';
END;
$$;

CREATE TRIGGER invocation_authorization_policy_withdrawals_append_only
BEFORE UPDATE OR DELETE ON invocation_authorization_policy_withdrawals
FOR EACH ROW EXECUTE FUNCTION reject_invocation_authorization_policy_withdrawal_mutation();

-- dataground:down

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM invocation_authorization_policy_withdrawals
    ) THEN
        RAISE EXCEPTION 'cannot remove invocation authorization policy withdrawal evidence';
    END IF;
END;
$$;

DROP TABLE invocation_authorization_policy_withdrawals;
DROP FUNCTION reject_invocation_authorization_policy_withdrawal_mutation();
DROP FUNCTION enforce_invocation_authorization_policy_withdrawal_digest();
