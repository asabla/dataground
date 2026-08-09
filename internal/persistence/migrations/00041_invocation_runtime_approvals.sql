-- dataground:up

ALTER TABLE invocation_authorization_policies
    DROP CONSTRAINT invocation_authorization_policies_contract_check,
    DROP CONSTRAINT invocation_authorization_policies_entities_check,
    ADD CONSTRAINT invocation_authorization_policies_contract_check
        CHECK (contract IN (
            'dataground.invocation-authorization-policy/v1',
            'dataground.invocation-authorization-policy/v2',
            'dataground.invocation-authorization-policy/v3'
        )),
    ADD CONSTRAINT invocation_authorization_policies_entities_check
        CHECK (
            (contract = 'dataground.invocation-authorization-policy/v1' AND cedar_entities IS NULL)
            OR
            (contract IN (
                'dataground.invocation-authorization-policy/v2',
                'dataground.invocation-authorization-policy/v3'
            ) AND octet_length(cedar_entities) BETWEEN 1 AND 1048576)
        );

ALTER TABLE invocation_authorization_decisions
    DROP CONSTRAINT invocation_authorization_decisions_action_check,
    ADD CONSTRAINT invocation_authorization_decisions_action_check
        CHECK (action IN ('admit', 'run', 'cancel', 'approve'));

CREATE TABLE invocation_runtime_approvals (
    contract text NOT NULL,
    isolation_domain_id text NOT NULL,
    id text NOT NULL,
    operation_id text NOT NULL,
    invocation_id text NOT NULL,
    service_id text NOT NULL,
    revision_id text NOT NULL,
    effect_id text NOT NULL,
    source_sequence bigint NOT NULL,
    requested_action text NOT NULL,
    state text NOT NULL,
    version bigint NOT NULL,
    decision text,
    effective_decision text,
    resolved_by text,
    resolution_correlation_id text,
    resolved_at timestamptz,
    delivered_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (isolation_domain_id, id),
    UNIQUE (isolation_domain_id, operation_id, source_sequence),
    FOREIGN KEY (isolation_domain_id, invocation_id)
        REFERENCES invocations (isolation_domain_id, id),
    FOREIGN KEY (isolation_domain_id, effect_id)
        REFERENCES external_effects (isolation_domain_id, effect_id),
    CHECK (contract = 'dataground.invocation-runtime-approval/v1'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (id ~ '^apr_[0-9a-z]{20,32}$'),
    CHECK (operation_id ~ '^op_[0-9a-z]{20,32}$'),
    CHECK (invocation_id ~ '^inv_[0-9a-z]{20,32}$'),
    CHECK (service_id ~ '^svc_[0-9a-z]{20,32}$'),
    CHECK (revision_id ~ '^rev_[0-9a-z]{20,32}$'),
    CHECK (effect_id ~ '^eff_[0-9a-z]{20,32}$'),
    CHECK (source_sequence > 0),
    CHECK (requested_action IN ('process.execute', 'workspace.change')),
    CHECK (state IN ('pending', 'resolved', 'delivering', 'delivered')),
    CHECK (version > 0),
    CHECK (decision IS NULL OR decision IN ('approve', 'deny')),
    CHECK (effective_decision IS NULL OR effective_decision IN ('approve', 'deny')),
    CHECK (resolved_by IS NULL OR (
        resolved_by <> '' AND length(resolved_by) <= 256 AND resolved_by !~ '[[:cntrl:]]'
    )),
    CHECK (resolution_correlation_id IS NULL
        OR resolution_correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (
        (state = 'pending' AND version = 1 AND decision IS NULL
            AND effective_decision IS NULL AND resolved_by IS NULL
            AND resolution_correlation_id IS NULL AND resolved_at IS NULL
            AND delivered_at IS NULL)
        OR
        (state = 'resolved' AND decision IS NOT NULL
            AND effective_decision IS NULL AND resolved_by IS NOT NULL
            AND resolution_correlation_id IS NOT NULL AND resolved_at IS NOT NULL
            AND delivered_at IS NULL)
        OR
        (state = 'delivering' AND decision IS NOT NULL
            AND effective_decision IS NOT NULL AND resolved_by IS NOT NULL
            AND resolution_correlation_id IS NOT NULL AND resolved_at IS NOT NULL
            AND delivered_at IS NULL)
        OR
        (state = 'delivered' AND decision IS NOT NULL
            AND effective_decision IS NOT NULL AND resolved_by IS NOT NULL
            AND resolution_correlation_id IS NOT NULL AND resolved_at IS NOT NULL
            AND delivered_at IS NOT NULL)
    ),
    CHECK (isfinite(created_at) AND isfinite(updated_at)),
    CHECK (resolved_at IS NULL OR isfinite(resolved_at)),
    CHECK (delivered_at IS NULL OR isfinite(delivered_at))
);

CREATE FUNCTION enforce_invocation_runtime_approval_transition()
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

CREATE TRIGGER invocation_runtime_approvals_transition
BEFORE UPDATE OR DELETE ON invocation_runtime_approvals
FOR EACH ROW EXECUTE FUNCTION enforce_invocation_runtime_approval_transition();

CREATE INDEX invocation_runtime_approvals_operation
ON invocation_runtime_approvals (isolation_domain_id, operation_id, state, created_at, id);

-- dataground:down

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM invocation_runtime_approvals)
       OR EXISTS (
            SELECT 1 FROM invocation_authorization_policies
            WHERE contract = 'dataground.invocation-authorization-policy/v3'
       )
       OR EXISTS (
            SELECT 1 FROM invocation_authorization_decisions
            WHERE action = 'approve'
       ) THEN
        RAISE EXCEPTION 'cannot remove invocation approval evidence';
    END IF;
END;
$$;

DROP INDEX invocation_runtime_approvals_operation;
DROP TRIGGER invocation_runtime_approvals_transition ON invocation_runtime_approvals;
DROP FUNCTION enforce_invocation_runtime_approval_transition();
DROP TABLE invocation_runtime_approvals;

ALTER TABLE invocation_authorization_decisions
    DROP CONSTRAINT invocation_authorization_decisions_action_check,
    ADD CONSTRAINT invocation_authorization_decisions_action_check
        CHECK (action IN ('admit', 'run', 'cancel'));

ALTER TABLE invocation_authorization_policies
    DROP CONSTRAINT invocation_authorization_policies_contract_check,
    DROP CONSTRAINT invocation_authorization_policies_entities_check,
    ADD CONSTRAINT invocation_authorization_policies_contract_check
        CHECK (contract IN (
            'dataground.invocation-authorization-policy/v1',
            'dataground.invocation-authorization-policy/v2'
        )),
    ADD CONSTRAINT invocation_authorization_policies_entities_check
        CHECK (
            (contract = 'dataground.invocation-authorization-policy/v1' AND cedar_entities IS NULL)
            OR
            (contract = 'dataground.invocation-authorization-policy/v2'
                AND octet_length(cedar_entities) BETWEEN 1 AND 1048576)
        );
