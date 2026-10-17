-- dataground:up
CREATE TABLE invocation_workspace_quiescence_intents (
 isolation_domain_id text NOT NULL,
 operation_id text NOT NULL,
 record jsonb NOT NULL,
 runtime_token bigint NOT NULL,
 actor_id text NOT NULL,
 correlation_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(created_at)),
 PRIMARY KEY (isolation_domain_id,operation_id),
 FOREIGN KEY (isolation_domain_id,operation_id,runtime_token) REFERENCES invocation_workspace_preparations(isolation_domain_id,operation_id,fencing_token),
 CHECK (jsonb_typeof(record)='object' AND record ?& ARRAY['schemaVersion','source','invocationId','operationId','effectId','executionId','planDigest','ownerBinding','runtimeOwner','runtimeToken','actorId','correlationId']),
 CHECK (record - ARRAY['schemaVersion','source','invocationId','operationId','effectId','executionId','planDigest','ownerBinding','runtimeOwner','runtimeToken','actorId','correlationId']='{}'::jsonb),
 CHECK (record->>'schemaVersion' IS NOT DISTINCT FROM 'dataground.invocation-workspace-quiescence/v1'),
 CHECK (record->>'operationId' IS NOT DISTINCT FROM operation_id AND record->'source'->>'isolationDomainId' IS NOT DISTINCT FROM isolation_domain_id),
 CHECK (COALESCE(record->>'ownerBinding' ~ '^sha256:[0-9a-f]{64}$',false)),
 CHECK (record->>'actorId' IS NOT DISTINCT FROM actor_id AND record->>'correlationId' IS NOT DISTINCT FROM correlation_id),
 CHECK (jsonb_typeof(record->'runtimeToken')='number' AND (record->>'runtimeToken')::bigint IS NOT DISTINCT FROM runtime_token)
);
CREATE TABLE invocation_workspace_quiescence_completions (
 isolation_domain_id text NOT NULL,
 operation_id text NOT NULL,
 actor_id text NOT NULL,
 correlation_id text NOT NULL,
 completed_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(completed_at)),
 PRIMARY KEY (isolation_domain_id,operation_id),
 FOREIGN KEY (isolation_domain_id,operation_id) REFERENCES invocation_workspace_quiescence_intents(isolation_domain_id,operation_id)
);
CREATE TRIGGER workspace_quiescence_intents_append_only BEFORE UPDATE OR DELETE ON invocation_workspace_quiescence_intents FOR EACH ROW EXECUTE FUNCTION reject_workspace_source_mutation();
CREATE TRIGGER workspace_quiescence_completions_append_only BEFORE UPDATE OR DELETE ON invocation_workspace_quiescence_completions FOR EACH ROW EXECUTE FUNCTION reject_workspace_source_mutation();

-- Only the original runtime owner can confirm its connection. A replacement
-- claim can read an existing completion, but cannot acknowledge a new host.
CREATE FUNCTION lock_workspace_quiescence_claim(scope text,op text,owner text,token bigint,actor text,correlation text,value jsonb) RETURNS timestamptz LANGUAGE plpgsql AS $$
DECLARE expires timestamptz;
BEGIN
 SELECT LEAST(operation.lease_expires_at,operation.deadline_at) INTO expires FROM invocation_execution_operations operation
 JOIN invocations invocation ON invocation.isolation_domain_id=operation.isolation_domain_id AND invocation.id=operation.invocation_id
 JOIN external_effects effect ON effect.isolation_domain_id=operation.isolation_domain_id AND effect.operation_id=operation.id
 JOIN invocation_runtime_attempts attempt ON attempt.isolation_domain_id=operation.isolation_domain_id AND attempt.operation_id=operation.id AND attempt.effect_id=effect.effect_id
 JOIN invocation_workspace_preparations preparation ON preparation.isolation_domain_id=attempt.isolation_domain_id AND preparation.operation_id=attempt.operation_id AND preparation.fencing_token=attempt.fencing_token AND preparation.lease_owner=attempt.lease_owner AND preparation.effect_id=attempt.effect_id
 JOIN invocation_workspace_completions completion ON completion.isolation_domain_id=preparation.isolation_domain_id AND completion.operation_id=preparation.operation_id AND completion.fencing_token=preparation.fencing_token
 JOIN execution_instances instance ON instance.isolation_domain_id=operation.isolation_domain_id AND instance.operation_id=operation.id AND instance.id=preparation.execution_id
 JOIN service_revision_execution_plans plan ON plan.isolation_domain_id=invocation.isolation_domain_id AND plan.revision_id=invocation.revision_id
 WHERE operation.isolation_domain_id=scope AND operation.id=op AND operation.lease_owner=owner AND operation.lease_token=token
 AND operation.command IN ('invoke','repair') AND operation.observed_state='running' AND operation.state_machine_version=2
 AND operation.lease_expires_at>clock_timestamp() AND operation.deadline_at>clock_timestamp()
 AND COALESCE(operation.effect_actor_id,operation.actor_id)=actor AND COALESCE(operation.effect_correlation_id,operation.correlation_id)=correlation
 AND effect.operation_kind='invocation-execution' AND effect.phase='run-invocation' AND effect.status IN ('prepared','failed','unknown')
 AND attempt.lease_owner=owner AND attempt.fencing_token=token AND attempt.status IN ('reserved','succeeded','failed','output_invalid')
 AND instance.observed_state='ready' AND value->>'schemaVersion'='dataground.invocation-workspace-quiescence/v1'
 AND value->>'invocationId'=invocation.id AND value->>'operationId'=operation.id AND value->>'effectId'=effect.effect_id AND value->>'executionId'=instance.id
 AND value->'source'=preparation.source_record AND value->>'planDigest'=preparation.plan_digest
 AND value->>'runtimeOwner'=owner AND (value->>'runtimeToken')::bigint=token
 AND value->>'actorId'=actor AND value->>'correlationId'=correlation
 AND plan.schema_version='dataground.execution-plan/v2' AND plan.workspace_source=preparation.source_record AND plan.plan_digest=preparation.plan_digest
 FOR UPDATE OF invocation,operation,instance NOWAIT;
 IF NOT FOUND THEN RAISE EXCEPTION 'workspace cleanup requires its original current runtime claim'; END IF;
 RETURN expires;
END; $$;
CREATE FUNCTION enforce_workspace_quiescence_intent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM lock_workspace_quiescence_claim(NEW.isolation_domain_id,NEW.operation_id,NEW.record->>'runtimeOwner',NEW.runtime_token,NEW.actor_id,NEW.correlation_id,NEW.record);
 RETURN NEW;
END; $$;
CREATE TRIGGER workspace_quiescence_intent_binding BEFORE INSERT ON invocation_workspace_quiescence_intents FOR EACH ROW EXECUTE FUNCTION enforce_workspace_quiescence_intent();
CREATE FUNCTION enforce_workspace_quiescence_completion() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE value jsonb;
BEGIN
 SELECT record INTO value FROM invocation_workspace_quiescence_intents WHERE isolation_domain_id=NEW.isolation_domain_id AND operation_id=NEW.operation_id;
 IF NOT FOUND THEN RAISE EXCEPTION 'workspace cleanup requires immutable intent'; END IF;
 PERFORM lock_workspace_quiescence_claim(NEW.isolation_domain_id,NEW.operation_id,value->>'runtimeOwner',(value->>'runtimeToken')::bigint,NEW.actor_id,NEW.correlation_id,value);
 RETURN NEW;
END; $$;
CREATE TRIGGER workspace_quiescence_completion_binding BEFORE INSERT ON invocation_workspace_quiescence_completions FOR EACH ROW EXECUTE FUNCTION enforce_workspace_quiescence_completion();

-- dataground:down
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM invocation_workspace_quiescence_intents) OR EXISTS(SELECT 1 FROM invocation_workspace_quiescence_completions) THEN
  RAISE EXCEPTION 'cannot remove workspace cleanup evidence';
 END IF;
END; $$;
DROP TABLE invocation_workspace_quiescence_completions;
DROP TABLE invocation_workspace_quiescence_intents;
DROP FUNCTION enforce_workspace_quiescence_completion();
DROP FUNCTION enforce_workspace_quiescence_intent();
DROP FUNCTION lock_workspace_quiescence_claim(text,text,text,bigint,text,text,jsonb);
