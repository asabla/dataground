-- dataground:up

CREATE TABLE invocation_runtime_questions (
    contract text NOT NULL CHECK (contract = 'dataground.invocation-runtime-question/v1'),
    isolation_domain_id text NOT NULL CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    id text NOT NULL CHECK (id ~ '^qst_[0-9a-z]{20,32}$'),
    operation_id text NOT NULL,
    invocation_id text NOT NULL,
    service_id text NOT NULL,
    revision_id text NOT NULL,
    effect_id text NOT NULL,
    source_sequence bigint NOT NULL CHECK (source_sequence > 0),
    correlation_id text NOT NULL CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    requested_by text NOT NULL CHECK (requested_by <> '' AND length(requested_by) <= 256 AND requested_by !~ '[[:cntrl:]]'),
    prompts jsonb NOT NULL CHECK (jsonb_typeof(prompts) = 'array' AND jsonb_array_length(prompts) BETWEEN 1 AND 3 AND octet_length(prompts::text) <= 65536),
    expires_at timestamptz NOT NULL,
    state text NOT NULL CHECK (state IN ('pending', 'answered', 'delivering', 'delivered', 'expired', 'closed', 'delivery_unknown')),
    version bigint NOT NULL CHECK (version > 0),
    answers jsonb CHECK (jsonb_typeof(answers) = 'array' AND jsonb_array_length(answers) BETWEEN 1 AND 3 AND octet_length(answers::text) <= 32768),
    answered_by text CHECK (answered_by <> '' AND length(answered_by) <= 256 AND answered_by !~ '[[:cntrl:]]'),
    answer_correlation_id text CHECK (answer_correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    answered_at timestamptz,
    delivery_started_at timestamptz,
    delivered_at timestamptz,
    closed_at timestamptz,
    close_reason text CHECK (close_reason IN ('expired', 'runtime-request-cleared', 'cancelled', 'runtime-ended', 'delivery-ambiguous')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (isolation_domain_id, id),
    UNIQUE (isolation_domain_id, operation_id, source_sequence),
    FOREIGN KEY (isolation_domain_id, invocation_id) REFERENCES invocations (isolation_domain_id, id),
    FOREIGN KEY (isolation_domain_id, operation_id) REFERENCES invocation_execution_operations (isolation_domain_id, id),
    FOREIGN KEY (isolation_domain_id, service_id) REFERENCES agent_services (isolation_domain_id, id),
    FOREIGN KEY (isolation_domain_id, revision_id) REFERENCES service_revisions (isolation_domain_id, id),
    FOREIGN KEY (isolation_domain_id, effect_id) REFERENCES external_effects (isolation_domain_id, effect_id),
    CHECK (isfinite(created_at) AND isfinite(updated_at) AND isfinite(expires_at)),
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '15 minutes'),
    CHECK (answered_at IS NULL OR isfinite(answered_at)),
    CHECK (delivery_started_at IS NULL OR isfinite(delivery_started_at)),
    CHECK (delivered_at IS NULL OR isfinite(delivered_at)),
    CHECK (closed_at IS NULL OR isfinite(closed_at)),
    CHECK (
        (answers IS NULL AND answered_by IS NULL AND answer_correlation_id IS NULL AND answered_at IS NULL)
        OR (answers IS NOT NULL AND answered_by IS NOT NULL AND answer_correlation_id IS NOT NULL AND answered_at IS NOT NULL)
    ),
    CHECK (
        (state = 'pending' AND version = 1 AND answers IS NULL AND delivery_started_at IS NULL AND delivered_at IS NULL AND closed_at IS NULL AND close_reason IS NULL)
        OR (state = 'answered' AND answers IS NOT NULL AND delivery_started_at IS NULL AND delivered_at IS NULL AND closed_at IS NULL AND close_reason IS NULL)
        OR (state = 'delivering' AND answers IS NOT NULL AND delivery_started_at IS NOT NULL AND delivered_at IS NULL AND closed_at IS NULL AND close_reason IS NULL)
        OR (state = 'delivered' AND answers IS NOT NULL AND delivery_started_at IS NOT NULL AND delivered_at IS NOT NULL AND closed_at IS NULL AND close_reason IS NULL)
        OR (state IN ('expired', 'closed') AND delivery_started_at IS NULL AND delivered_at IS NULL AND closed_at IS NOT NULL AND close_reason IS NOT NULL)
        OR (state = 'delivery_unknown' AND answers IS NOT NULL AND delivery_started_at IS NOT NULL AND delivered_at IS NULL AND closed_at IS NOT NULL AND close_reason IS NOT NULL)
    ),
    CHECK (state <> 'expired' OR close_reason = 'expired')
);

CREATE INDEX invocation_runtime_questions_due_idx
    ON invocation_runtime_questions (isolation_domain_id, expires_at, id)
    WHERE state IN ('pending', 'answered', 'delivering');

CREATE FUNCTION enforce_invocation_runtime_question_transition()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'pending' OR NEW.version <> 1 THEN
            RAISE EXCEPTION 'invocation runtime questions must begin pending';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'invocation runtime questions cannot be deleted';
    END IF;
    IF ROW(NEW.contract, NEW.isolation_domain_id, NEW.id, NEW.operation_id, NEW.invocation_id,
           NEW.service_id, NEW.revision_id, NEW.effect_id, NEW.source_sequence, NEW.correlation_id,
           NEW.requested_by, NEW.prompts, NEW.expires_at, NEW.created_at)
       IS DISTINCT FROM
       ROW(OLD.contract, OLD.isolation_domain_id, OLD.id, OLD.operation_id, OLD.invocation_id,
           OLD.service_id, OLD.revision_id, OLD.effect_id, OLD.source_sequence, OLD.correlation_id,
           OLD.requested_by, OLD.prompts, OLD.expires_at, OLD.created_at)
       OR NEW.version <> OLD.version + 1
       OR NOT (
           (OLD.state = 'pending' AND NEW.state IN ('answered', 'expired', 'closed'))
           OR (OLD.state = 'answered' AND NEW.state IN ('delivering', 'expired', 'closed'))
           OR (OLD.state = 'delivering' AND NEW.state IN ('delivered', 'delivery_unknown'))
       ) THEN
        RAISE EXCEPTION 'invocation runtime question transition is invalid';
    END IF;
    IF NOT (OLD.state = 'pending' AND NEW.state = 'answered') AND
       ROW(NEW.answers, NEW.answered_by, NEW.answer_correlation_id, NEW.answered_at)
       IS DISTINCT FROM ROW(OLD.answers, OLD.answered_by, OLD.answer_correlation_id, OLD.answered_at) THEN
        RAISE EXCEPTION 'invocation runtime question answer is immutable';
    END IF;
    IF OLD.delivery_started_at IS NOT NULL AND NEW.delivery_started_at IS DISTINCT FROM OLD.delivery_started_at THEN
        RAISE EXCEPTION 'invocation runtime question delivery is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER invocation_runtime_questions_transition
BEFORE INSERT OR UPDATE OR DELETE ON invocation_runtime_questions
FOR EACH ROW EXECUTE FUNCTION enforce_invocation_runtime_question_transition();

-- dataground:down

DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM invocation_runtime_questions) THEN
        RAISE EXCEPTION 'invocation runtime question evidence prevents downgrade';
    END IF;
END; $$;
DROP TABLE invocation_runtime_questions;
DROP FUNCTION enforce_invocation_runtime_question_transition();
