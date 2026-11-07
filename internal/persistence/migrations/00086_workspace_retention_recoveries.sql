-- dataground:up
CREATE TABLE workspace_retention_recoveries (
 isolation_domain_id text NOT NULL,
 correlation_id text NOT NULL CHECK (length(correlation_id) BETWEEN 1 AND 256),
 contract text NOT NULL CHECK (contract='dataground.workspace-retention-recovery/v1'),
 operation_id text NOT NULL,
 request_digest bytea NOT NULL CHECK (octet_length(request_digest)=32),
 actor_id text NOT NULL CHECK (length(actor_id) BETWEEN 1 AND 256),
 reason_digest text NOT NULL CHECK (reason_digest ~ '^sha256:[0-9a-f]{64}$'),
 owner_binding text NOT NULL CHECK (owner_binding ~ '^sha256:[0-9a-f]{64}$'),
 prior_generation bigint NOT NULL CHECK (prior_generation>0),
 prior_deadline timestamptz NOT NULL CHECK (isfinite(prior_deadline)),
 deadline_at timestamptz NOT NULL CHECK (isfinite(deadline_at)),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(recorded_at)),
 PRIMARY KEY (isolation_domain_id,correlation_id),
 UNIQUE (isolation_domain_id,operation_id,prior_generation),
 FOREIGN KEY (isolation_domain_id,operation_id) REFERENCES invocation_workspace_owner_stop_receipts(isolation_domain_id,operation_id),
 CHECK (prior_deadline<=recorded_at AND deadline_at>recorded_at AND deadline_at<=recorded_at+interval '15 minutes')
);
CREATE FUNCTION enforce_workspace_retention_recovery() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS (
  SELECT 1 FROM invocation_execution_operations operation
  JOIN invocations invocation ON invocation.isolation_domain_id=operation.isolation_domain_id AND invocation.id=operation.invocation_id
  JOIN invocation_workspace_owner_stop_receipts stopped ON stopped.isolation_domain_id=operation.isolation_domain_id AND stopped.operation_id=operation.id
  JOIN invocation_workspace_quiescence_intents intent ON intent.isolation_domain_id=stopped.isolation_domain_id AND intent.operation_id=stopped.operation_id
  JOIN service_revision_execution_plans plan ON plan.isolation_domain_id=invocation.isolation_domain_id AND plan.revision_id=invocation.revision_id
  JOIN execution_instances instance ON instance.isolation_domain_id=operation.isolation_domain_id AND instance.operation_id=operation.id AND instance.id=intent.record->>'executionId'
  WHERE operation.isolation_domain_id=NEW.isolation_domain_id AND operation.id=NEW.operation_id
  AND operation.observed_state='failed' AND invocation.state='failed' AND operation.state_machine_version=2
  AND operation.command='cancel' AND operation.desired_state='cancelled'
  AND operation.error->>'code'='OPERATION_DEADLINE_EXCEEDED'
  AND operation.generation=NEW.prior_generation AND operation.deadline_at=NEW.prior_deadline
  AND operation.lease_owner IS NULL AND operation.lease_expires_at IS NULL
  AND stopped.owner_binding=NEW.owner_binding AND instance.observed_state='ready'
  AND plan.schema_version='dataground.execution-plan/v2' AND plan.workspace_source=intent.record->'source' AND plan.plan_digest=intent.record->>'planDigest'
 ) THEN RAISE EXCEPTION 'retention recovery requires an exact stopped expired workspace'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER workspace_retention_recovery_binding BEFORE INSERT ON workspace_retention_recoveries FOR EACH ROW EXECUTE FUNCTION enforce_workspace_retention_recovery();
CREATE TRIGGER workspace_retention_recovery_append_only BEFORE UPDATE OR DELETE ON workspace_retention_recoveries FOR EACH ROW EXECUTE FUNCTION reject_workspace_source_mutation();

-- dataground:down
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM workspace_retention_recoveries) THEN
  RAISE EXCEPTION 'cannot remove workspace retention recovery evidence';
 END IF;
END; $$;
DROP TABLE workspace_retention_recoveries;
DROP FUNCTION enforce_workspace_retention_recovery();
