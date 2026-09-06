-- dataground:up

ALTER TABLE invocation_runtime_approvals
    ADD COLUMN expires_at timestamptz,
    ADD COLUMN closed_at timestamptz,
    ADD COLUMN close_reason text,
    DROP CONSTRAINT invocation_runtime_approvals_contract_check,
    DROP CONSTRAINT invocation_runtime_approvals_state_check,
    DROP CONSTRAINT invocation_runtime_approvals_check,
    ADD CONSTRAINT invocation_runtime_approvals_contract_check
        CHECK (contract IN ('dataground.invocation-runtime-approval/v1', 'dataground.invocation-runtime-approval/v2')),
    ADD CONSTRAINT invocation_runtime_approvals_state_check
        CHECK (state IN ('pending', 'resolved', 'delivering', 'delivered', 'closed', 'expired', 'delivery_unknown')),
    ADD CONSTRAINT invocation_runtime_approvals_expiry_contract CHECK (
        (contract = 'dataground.invocation-runtime-approval/v1' AND expires_at IS NULL
            AND closed_at IS NULL AND close_reason IS NULL
            AND state IN ('pending', 'resolved', 'delivering', 'delivered'))
        OR
        (contract = 'dataground.invocation-runtime-approval/v2' AND expires_at IS NOT NULL
            AND isfinite(expires_at) AND expires_at > created_at
            AND expires_at <= created_at + interval '15 minutes'
            AND isfinite(created_at) AND isfinite(updated_at) AND updated_at >= created_at
            AND (resolved_at IS NULL OR (isfinite(resolved_at) AND resolved_at >= created_at AND resolved_at < expires_at AND resolved_at <= updated_at))
            AND (delivered_at IS NULL OR (isfinite(delivered_at) AND delivered_at >= resolved_at AND delivered_at < expires_at AND delivered_at <= updated_at)))
    ),
    ADD CONSTRAINT invocation_runtime_approvals_resolution_fields CHECK (
        (decision IS NULL AND resolved_by IS NULL AND resolution_correlation_id IS NULL AND resolved_at IS NULL)
        OR (decision IS NOT NULL AND resolved_by IS NOT NULL AND resolution_correlation_id IS NOT NULL AND resolved_at IS NOT NULL)
    ),
    ADD CONSTRAINT invocation_runtime_approvals_lifecycle CHECK (
        (state='pending' AND version=1 AND decision IS NULL AND effective_decision IS NULL AND delivered_at IS NULL)
        OR (state='resolved' AND version=2 AND decision IS NOT NULL AND effective_decision IS NULL AND delivered_at IS NULL)
        OR (state='delivering' AND version=3 AND decision IS NOT NULL AND effective_decision IS NOT NULL AND delivered_at IS NULL)
        OR (state='delivered' AND version=4 AND decision IS NOT NULL AND effective_decision IS NOT NULL AND delivered_at IS NOT NULL)
        OR (state IN ('closed','expired') AND effective_decision IS NULL AND delivered_at IS NULL
            AND ((version=2 AND decision IS NULL) OR (version=3 AND decision IS NOT NULL)))
        OR (state='delivery_unknown' AND version=4 AND decision IS NOT NULL AND effective_decision IS NOT NULL AND delivered_at IS NULL)
    ),
    ADD CONSTRAINT invocation_runtime_approvals_closure_fields CHECK (
        (state IN ('pending','resolved','delivering','delivered') AND closed_at IS NULL AND close_reason IS NULL)
        OR (state IN ('closed','expired','delivery_unknown') AND closed_at IS NOT NULL
            AND isfinite(closed_at) AND closed_at >= created_at AND closed_at <= updated_at AND close_reason IS NOT NULL
            AND close_reason IN ('expired','runtime-request-cleared','runtime-ended','cancelled')
            AND (close_reason<>'expired' OR closed_at>=expires_at)
            AND (state<>'expired' OR close_reason='expired')
            AND (state<>'closed' OR close_reason<>'expired'))
    );

CREATE INDEX invocation_runtime_approvals_expiry
ON invocation_runtime_approvals (isolation_domain_id, expires_at, id)
WHERE state IN ('pending','resolved','delivering') AND expires_at IS NOT NULL;

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
            NEW.source_sequence, NEW.requested_action, NEW.created_at, NEW.expires_at
        ) IS DISTINCT FROM ROW(
            OLD.contract, OLD.isolation_domain_id, OLD.id, OLD.operation_id,
            OLD.invocation_id, OLD.service_id, OLD.revision_id, OLD.effect_id,
            OLD.source_sequence, OLD.requested_action, OLD.created_at, OLD.expires_at
        ) OR NEW.version <> OLD.version + 1 OR
        NOT (
            (OLD.state = 'pending' AND NEW.state = 'resolved')
            OR (OLD.state = 'resolved' AND NEW.state = 'delivering')
            OR (OLD.state = 'delivering' AND NEW.state = 'delivered')
            OR (OLD.contract = 'dataground.invocation-runtime-approval/v2' AND (
                (OLD.state IN ('pending', 'resolved') AND NEW.state IN ('closed', 'expired'))
                OR (OLD.state = 'delivering' AND NEW.state = 'delivery_unknown')
            ))
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
    IF EXISTS (SELECT 1 FROM invocation_runtime_approvals WHERE contract='dataground.invocation-runtime-approval/v2') THEN
        RAISE EXCEPTION 'cannot remove expiring approval evidence';
    END IF;
END; $$;

DROP INDEX invocation_runtime_approvals_expiry;
ALTER TABLE invocation_runtime_approvals
    DROP CONSTRAINT invocation_runtime_approvals_contract_check,
    DROP CONSTRAINT invocation_runtime_approvals_state_check,
    DROP CONSTRAINT invocation_runtime_approvals_expiry_contract,
    DROP CONSTRAINT invocation_runtime_approvals_resolution_fields,
    DROP CONSTRAINT invocation_runtime_approvals_lifecycle,
    DROP CONSTRAINT invocation_runtime_approvals_closure_fields,
    DROP COLUMN expires_at,
    DROP COLUMN closed_at,
    DROP COLUMN close_reason,
    ADD CONSTRAINT invocation_runtime_approvals_contract_check CHECK (contract='dataground.invocation-runtime-approval/v1'),
    ADD CONSTRAINT invocation_runtime_approvals_state_check CHECK (state IN ('pending','resolved','delivering','delivered')),
    ADD CONSTRAINT invocation_runtime_approvals_check CHECK (
        (state='pending' AND version=1 AND decision IS NULL AND effective_decision IS NULL AND resolved_by IS NULL AND resolution_correlation_id IS NULL AND resolved_at IS NULL AND delivered_at IS NULL)
        OR (state='resolved' AND decision IS NOT NULL AND effective_decision IS NULL AND resolved_by IS NOT NULL AND resolution_correlation_id IS NOT NULL AND resolved_at IS NOT NULL AND delivered_at IS NULL)
        OR (state='delivering' AND decision IS NOT NULL AND effective_decision IS NOT NULL AND resolved_by IS NOT NULL AND resolution_correlation_id IS NOT NULL AND resolved_at IS NOT NULL AND delivered_at IS NULL)
        OR (state='delivered' AND decision IS NOT NULL AND effective_decision IS NOT NULL AND resolved_by IS NOT NULL AND resolution_correlation_id IS NOT NULL AND resolved_at IS NOT NULL AND delivered_at IS NOT NULL)
    );

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
