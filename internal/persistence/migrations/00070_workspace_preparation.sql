-- dataground:up
CREATE TABLE invocation_workspace_preparations (
 isolation_domain_id text NOT NULL,
 operation_id text NOT NULL,
 fencing_token bigint NOT NULL CHECK (fencing_token>0),
 effect_id text NOT NULL,
 lease_owner text NOT NULL CHECK (length(lease_owner)>0),
 execution_id text NOT NULL,
 plan_digest text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
 source_record jsonb NOT NULL CHECK (jsonb_typeof(source_record)='object'),
 grant_generation bigint NOT NULL CHECK (grant_generation>0),
 actor_id text NOT NULL,
 correlation_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(created_at)),
 PRIMARY KEY (isolation_domain_id,operation_id,fencing_token),
 FOREIGN KEY (isolation_domain_id,operation_id) REFERENCES invocation_execution_operations(isolation_domain_id,id),
 FOREIGN KEY (isolation_domain_id,execution_id) REFERENCES execution_instances(isolation_domain_id,id),
 FOREIGN KEY (isolation_domain_id,effect_id) REFERENCES external_effects(isolation_domain_id,effect_id)
);
CREATE TABLE invocation_workspace_completions (
 isolation_domain_id text NOT NULL,
 operation_id text NOT NULL,
 fencing_token bigint NOT NULL,
 decision_id text NOT NULL,
 completed_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(completed_at)),
 PRIMARY KEY (isolation_domain_id,operation_id,fencing_token),
 FOREIGN KEY (isolation_domain_id,operation_id,fencing_token) REFERENCES invocation_workspace_preparations(isolation_domain_id,operation_id,fencing_token),
 FOREIGN KEY (isolation_domain_id,decision_id) REFERENCES workspace_source_use_decisions(isolation_domain_id,id)
);
CREATE TRIGGER invocation_workspace_preparations_append_only BEFORE UPDATE OR DELETE ON invocation_workspace_preparations FOR EACH ROW EXECUTE FUNCTION reject_workspace_source_mutation();
CREATE TRIGGER invocation_workspace_completions_append_only BEFORE UPDATE OR DELETE ON invocation_workspace_completions FOR EACH ROW EXECUTE FUNCTION reject_workspace_source_mutation();

ALTER TABLE workspace_source_use_decisions DROP CONSTRAINT workspace_source_claim_binding, DROP CONSTRAINT workspace_source_policy_binding;
ALTER TABLE workspace_source_use_decisions ADD CONSTRAINT workspace_source_claim_binding CHECK (
 (claim_contract IS NULL AND invocation_id IS NULL AND effect_id IS NULL AND claim_owner IS NULL AND claim_token IS NULL AND claim_expires_at IS NULL)
 OR (claim_contract IS NOT NULL AND claim_contract IN ('dataground.invocation-source-read/v1','dataground.invocation-source-read/v2','dataground.invocation-source-read/v3','dataground.invocation-source-read/v4')
 AND invocation_id IS NOT NULL AND invocation_id ~ '^inv_[0-9a-z]{20,32}$'
 AND effect_id IS NOT NULL AND effect_id ~ '^eff_[0-9a-z]{20,32}$'
 AND claim_owner IS NOT NULL AND length(claim_owner)>0 AND claim_token IS NOT NULL AND claim_token>0
 AND claim_expires_at IS NOT NULL AND isfinite(claim_expires_at) AND (outcome<>'allowed' OR claim_expires_at>occurred_at))
);

ALTER TABLE workspace_source_use_decisions
 ADD CONSTRAINT workspace_source_policy_binding CHECK (
  ((claim_contract IS NULL OR claim_contract NOT IN ('dataground.invocation-source-read/v3','dataground.invocation-source-read/v4')) AND policy_contract IS NULL AND policy_set_id IS NULL AND policy_digest IS NULL AND policy_outcome IS NULL AND plan_digest IS NULL)
  OR (claim_contract IS NOT NULL AND claim_contract IN ('dataground.invocation-source-read/v3','dataground.invocation-source-read/v4')
   AND policy_contract IS NOT NULL AND policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6')
   AND policy_set_id IS NOT NULL AND policy_set_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
   AND policy_digest IS NOT NULL AND policy_digest ~ '^sha256:[0-9a-f]{64}$'
   AND plan_digest IS NOT NULL AND plan_digest ~ '^sha256:[0-9a-f]{64}$'
   AND policy_outcome IS NOT NULL AND policy_outcome IN ('allowed','denied','unavailable')
   AND (policy_outcome<>'allowed' OR policy_contract='dataground.invocation-authorization-policy/v6')
   AND (outcome<>'allowed' OR policy_outcome='allowed'))
 );
CREATE FUNCTION enforce_workspace_preparation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM operation.id FROM invocation_execution_operations operation
 JOIN invocations invocation ON invocation.isolation_domain_id=operation.isolation_domain_id AND invocation.id=operation.invocation_id
 JOIN service_revision_execution_plans plan ON plan.isolation_domain_id=invocation.isolation_domain_id AND plan.revision_id=invocation.revision_id
 JOIN execution_instances instance ON instance.isolation_domain_id=operation.isolation_domain_id AND instance.operation_id=operation.id
 JOIN external_effects effect ON effect.isolation_domain_id=operation.isolation_domain_id AND effect.operation_id=operation.id
 WHERE operation.isolation_domain_id=NEW.isolation_domain_id AND operation.id=NEW.operation_id
 AND operation.command IN ('invoke','repair') AND operation.state_machine_version=2 AND operation.observed_state='running'
 AND operation.lease_owner=NEW.lease_owner AND operation.lease_token=NEW.fencing_token
 AND operation.lease_expires_at>clock_timestamp() AND operation.deadline_at>clock_timestamp()
 AND NEW.actor_id=COALESCE(operation.effect_actor_id,operation.actor_id) AND NEW.correlation_id=COALESCE(operation.effect_correlation_id,operation.correlation_id)
 AND plan.schema_version='dataground.execution-plan/v2' AND plan.plan_digest=NEW.plan_digest AND plan.workspace_source=NEW.source_record
 AND instance.id=NEW.execution_id AND instance.observed_state='ready'
 AND effect.effect_id=NEW.effect_id AND effect.operation_kind='invocation-execution' AND effect.phase='run-invocation' AND effect.status IN ('prepared','failed','unknown')
 AND NOT EXISTS(SELECT 1 FROM invocation_runtime_attempts attempt WHERE attempt.isolation_domain_id=operation.isolation_domain_id AND attempt.operation_id=operation.id)
 FOR UPDATE OF invocation,operation,instance NOWAIT;
 IF NOT FOUND THEN RAISE EXCEPTION 'workspace preparation requires an inactive exact claimed execution'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER workspace_preparation_binding BEFORE INSERT ON invocation_workspace_preparations FOR EACH ROW EXECUTE FUNCTION enforce_workspace_preparation();

CREATE FUNCTION enforce_workspace_completion() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM operation.id FROM invocation_workspace_preparations preparation
 JOIN invocation_execution_operations operation ON operation.isolation_domain_id=preparation.isolation_domain_id AND operation.id=preparation.operation_id
 JOIN invocations invocation ON invocation.isolation_domain_id=operation.isolation_domain_id AND invocation.id=operation.invocation_id
 JOIN workspace_source_use_decisions decision ON decision.isolation_domain_id=preparation.isolation_domain_id AND decision.id=NEW.decision_id
 WHERE preparation.isolation_domain_id=NEW.isolation_domain_id AND preparation.operation_id=NEW.operation_id AND preparation.fencing_token=NEW.fencing_token
 AND operation.lease_owner=preparation.lease_owner AND operation.lease_token=preparation.fencing_token
 AND operation.command IN ('invoke','repair') AND operation.state_machine_version=2 AND operation.observed_state='running' AND operation.lease_expires_at>clock_timestamp() AND operation.deadline_at>clock_timestamp()
 AND decision.claim_contract='dataground.invocation-source-read/v4' AND decision.operation_id=operation.id AND decision.invocation_id=invocation.id
 AND decision.effect_id=preparation.effect_id AND decision.claim_owner=preparation.lease_owner AND decision.claim_token=preparation.fencing_token
 AND decision.phase='read-delivery' AND decision.outcome='allowed' AND decision.policy_outcome='allowed'
 AND decision.expires_at>clock_timestamp() AND decision.claim_expires_at>clock_timestamp()
 AND decision.plan_digest=preparation.plan_digest AND decision.requested_generation=preparation.grant_generation
 AND decision.archive_digest=preparation.source_record->>'archiveDigest' AND decision.storage_profile_digest=preparation.source_record->>'storageProfileDigest'
 AND decision.workspace_id=preparation.source_record->>'workspaceId' AND decision.source_id=preparation.source_record->>'id'
 AND decision.revision_id=invocation.revision_id AND decision.actor_id=preparation.actor_id AND decision.correlation_id=preparation.correlation_id
 AND NOT EXISTS(SELECT 1 FROM invocation_runtime_attempts attempt WHERE attempt.isolation_domain_id=operation.isolation_domain_id AND attempt.operation_id=operation.id)
 FOR UPDATE OF invocation,operation NOWAIT;
 IF NOT FOUND THEN RAISE EXCEPTION 'workspace completion requires a current exact delivery decision'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER workspace_completion_binding BEFORE INSERT ON invocation_workspace_completions FOR EACH ROW EXECUTE FUNCTION enforce_workspace_completion();

CREATE FUNCTION require_workspace_preparation_for_runtime() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='reserved' AND EXISTS (
  SELECT 1 FROM invocation_execution_operations operation JOIN invocations invocation
  ON invocation.isolation_domain_id=operation.isolation_domain_id AND invocation.id=operation.invocation_id
  JOIN service_revision_execution_plans plan ON plan.isolation_domain_id=invocation.isolation_domain_id AND plan.revision_id=invocation.revision_id
  WHERE operation.isolation_domain_id=NEW.isolation_domain_id AND operation.id=NEW.operation_id AND plan.schema_version='dataground.execution-plan/v2'
 ) AND NOT EXISTS (
  SELECT 1 FROM invocation_workspace_preparations preparation JOIN invocation_workspace_completions completion
  USING (isolation_domain_id,operation_id,fencing_token)
  WHERE preparation.isolation_domain_id=NEW.isolation_domain_id AND preparation.operation_id=NEW.operation_id
  AND preparation.fencing_token=NEW.fencing_token AND preparation.lease_owner=NEW.lease_owner AND preparation.effect_id=NEW.effect_id
  AND EXISTS(SELECT 1 FROM invocation_execution_operations operation WHERE operation.isolation_domain_id=NEW.isolation_domain_id AND operation.id=NEW.operation_id
   AND operation.lease_owner=NEW.lease_owner AND operation.lease_token=NEW.fencing_token AND operation.command IN ('invoke','repair') AND operation.state_machine_version=2 AND operation.observed_state='running'
   AND operation.lease_expires_at>clock_timestamp() AND operation.deadline_at>clock_timestamp())
 ) THEN RAISE EXCEPTION 'runtime requires completed workspace preparation'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER runtime_requires_workspace_preparation BEFORE INSERT OR UPDATE ON invocation_runtime_attempts FOR EACH ROW EXECUTE FUNCTION require_workspace_preparation_for_runtime();

-- dataground:down
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM invocation_workspace_preparations) OR EXISTS (SELECT 1 FROM invocation_workspace_completions)
 OR EXISTS (SELECT 1 FROM workspace_source_use_decisions WHERE claim_contract='dataground.invocation-source-read/v4') THEN
 RAISE EXCEPTION 'cannot remove workspace preparation evidence'; END IF;
END; $$;
DROP TRIGGER runtime_requires_workspace_preparation ON invocation_runtime_attempts;
DROP FUNCTION require_workspace_preparation_for_runtime();
DROP TRIGGER workspace_completion_binding ON invocation_workspace_completions;
DROP FUNCTION enforce_workspace_completion();
DROP TABLE invocation_workspace_completions;
DROP TRIGGER workspace_preparation_binding ON invocation_workspace_preparations;
DROP FUNCTION enforce_workspace_preparation();
DROP TABLE invocation_workspace_preparations;
ALTER TABLE workspace_source_use_decisions DROP CONSTRAINT workspace_source_claim_binding, DROP CONSTRAINT workspace_source_policy_binding;
ALTER TABLE workspace_source_use_decisions ADD CONSTRAINT workspace_source_claim_binding CHECK (
 (claim_contract IS NULL AND invocation_id IS NULL AND effect_id IS NULL AND claim_owner IS NULL AND claim_token IS NULL AND claim_expires_at IS NULL)
 OR (claim_contract IS NOT NULL AND claim_contract IN ('dataground.invocation-source-read/v1','dataground.invocation-source-read/v2','dataground.invocation-source-read/v3')
 AND invocation_id IS NOT NULL AND invocation_id ~ '^inv_[0-9a-z]{20,32}$'
 AND effect_id IS NOT NULL AND effect_id ~ '^eff_[0-9a-z]{20,32}$'
 AND claim_owner IS NOT NULL AND length(claim_owner)>0 AND claim_token IS NOT NULL AND claim_token>0
 AND claim_expires_at IS NOT NULL AND isfinite(claim_expires_at) AND (outcome<>'allowed' OR claim_expires_at>occurred_at))
);

ALTER TABLE workspace_source_use_decisions
 ADD CONSTRAINT workspace_source_policy_binding CHECK (
  ((claim_contract IS NULL OR claim_contract<>'dataground.invocation-source-read/v3') AND policy_contract IS NULL AND policy_set_id IS NULL AND policy_digest IS NULL AND policy_outcome IS NULL AND plan_digest IS NULL)
  OR (claim_contract IS NOT NULL AND claim_contract='dataground.invocation-source-read/v3'
   AND policy_contract IS NOT NULL AND policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6')
   AND policy_set_id IS NOT NULL AND policy_set_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
   AND policy_digest IS NOT NULL AND policy_digest ~ '^sha256:[0-9a-f]{64}$'
   AND plan_digest IS NOT NULL AND plan_digest ~ '^sha256:[0-9a-f]{64}$'
   AND policy_outcome IS NOT NULL AND policy_outcome IN ('allowed','denied','unavailable')
   AND (policy_outcome<>'allowed' OR policy_contract='dataground.invocation-authorization-policy/v6')
   AND (outcome<>'allowed' OR policy_outcome='allowed'))
 );
