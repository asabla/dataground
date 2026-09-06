-- dataground:up

ALTER TABLE invocation_runtime_approvals
    ADD CONSTRAINT invocation_runtime_approvals_effective_decision_bound
        CHECK (effective_decision IS NULL OR effective_decision = 'deny' OR decision = 'approve');

CREATE OR REPLACE FUNCTION enforce_invocation_runtime_approval_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'invocation runtime approvals cannot be deleted';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF ROW(
            NEW.contract, NEW.isolation_domain_id, NEW.id, NEW.operation_id,
            NEW.invocation_id, NEW.service_id, NEW.revision_id, NEW.effect_id,
            NEW.source_sequence, NEW.requested_action, NEW.created_at
        ) IS DISTINCT FROM ROW(
            OLD.contract, OLD.isolation_domain_id, OLD.id, OLD.operation_id,
            OLD.invocation_id, OLD.service_id, OLD.revision_id, OLD.effect_id,
            OLD.source_sequence, OLD.requested_action, OLD.created_at
        ) OR NEW.version <> OLD.version + 1 OR
        NOT (
            (OLD.state = 'pending' AND NEW.state = 'resolved')
            OR (OLD.state = 'resolved' AND NEW.state = 'delivering')
            OR (OLD.state = 'delivering' AND NEW.state = 'delivered')
        ) THEN
            RAISE EXCEPTION 'invocation runtime approval transition is invalid';
        END IF;
        IF OLD.state <> 'pending' AND ROW(
            NEW.decision, NEW.resolved_by, NEW.resolution_correlation_id, NEW.resolved_at
        ) IS DISTINCT FROM ROW(
            OLD.decision, OLD.resolved_by, OLD.resolution_correlation_id, OLD.resolved_at
        ) THEN
            RAISE EXCEPTION 'invocation runtime approval resolution is immutable';
        END IF;
        IF OLD.state = 'delivering' AND
            NEW.effective_decision IS DISTINCT FROM OLD.effective_decision THEN
            RAISE EXCEPTION 'invocation runtime approval delivery decision is immutable';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

-- dataground:down

DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM invocation_runtime_approvals) THEN
        RAISE EXCEPTION 'cannot remove retained approval decision protections';
    END IF;
END; $$;

ALTER TABLE invocation_runtime_approvals
    DROP CONSTRAINT invocation_runtime_approvals_effective_decision_bound;

CREATE OR REPLACE FUNCTION enforce_invocation_runtime_approval_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'invocation runtime approvals cannot be deleted';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF ROW(
            NEW.contract, NEW.isolation_domain_id, NEW.id, NEW.operation_id,
            NEW.invocation_id, NEW.service_id, NEW.revision_id, NEW.effect_id,
            NEW.source_sequence, NEW.requested_action, NEW.created_at
        ) IS DISTINCT FROM ROW(
            OLD.contract, OLD.isolation_domain_id, OLD.id, OLD.operation_id,
            OLD.invocation_id, OLD.service_id, OLD.revision_id, OLD.effect_id,
            OLD.source_sequence, OLD.requested_action, OLD.created_at
        ) OR NEW.version <> OLD.version + 1 OR
        NOT (
            (OLD.state = 'pending' AND NEW.state = 'resolved')
            OR (OLD.state = 'resolved' AND NEW.state = 'delivering')
            OR (OLD.state = 'delivering' AND NEW.state = 'delivered')
        ) THEN
            RAISE EXCEPTION 'invocation runtime approval transition is invalid';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
