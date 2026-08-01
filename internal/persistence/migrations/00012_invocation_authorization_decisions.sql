-- dataground:up

CREATE TABLE invocation_authorization_decisions (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    isolation_domain_id text NOT NULL,
    operation_id text NOT NULL,
    invocation_id text NOT NULL,
    service_id text NOT NULL,
    revision_id text NOT NULL,
    actor_id text NOT NULL,
    action text NOT NULL CHECK (action IN ('admit', 'run', 'cancel')),
    outcome text NOT NULL CHECK (outcome IN ('allowed', 'denied', 'unavailable')),
    policy_set_id text NOT NULL,
    policy_digest text NOT NULL,
    correlation_id text NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (operation_id ~ '^op_[0-9a-z]{20,32}$'),
    CHECK (invocation_id ~ '^inv_[0-9a-z]{20,32}$'),
    CHECK (service_id ~ '^svc_[0-9a-z]{20,32}$'),
    CHECK (revision_id ~ '^rev_[0-9a-z]{20,32}$'),
    CHECK (length(actor_id) BETWEEN 1 AND 256),
    CHECK (actor_id !~ '[[:cntrl:]]'),
    CHECK (policy_set_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    CHECK (policy_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$')
);

CREATE INDEX invocation_authorization_decisions_scope_sequence_idx
    ON invocation_authorization_decisions (isolation_domain_id, operation_id, sequence);

CREATE FUNCTION reject_invocation_authorization_decision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'invocation authorization decisions are append-only';
END;
$$;

CREATE TRIGGER invocation_authorization_decisions_append_only
BEFORE UPDATE OR DELETE ON invocation_authorization_decisions
FOR EACH ROW EXECUTE FUNCTION reject_invocation_authorization_decision_mutation();

-- dataground:down

DROP TABLE invocation_authorization_decisions;
DROP FUNCTION reject_invocation_authorization_decision_mutation();
