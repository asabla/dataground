-- dataground:up
-- Cancellation retains the stopped workspace under its own current lease. It
-- never restores invocation execution authority or grants sandbox deletion.
CREATE FUNCTION lock_workspace_cancellation_retention(scope text,op text,owner text,token bigint,actor text,correlation text,value jsonb) RETURNS timestamptz LANGUAGE plpgsql AS $$
DECLARE expires timestamptz;
BEGIN
 SELECT LEAST(operation.lease_expires_at,operation.deadline_at) INTO expires FROM invocation_execution_operations operation
 JOIN invocations invocation ON invocation.isolation_domain_id=operation.isolation_domain_id AND invocation.id=operation.invocation_id
 JOIN external_effects effect ON effect.isolation_domain_id=operation.isolation_domain_id AND effect.operation_id=operation.id
 JOIN invocation_runtime_attempts attempt ON attempt.isolation_domain_id=operation.isolation_domain_id AND attempt.operation_id=operation.id AND attempt.effect_id=effect.effect_id
 JOIN invocation_workspace_preparations preparation ON preparation.isolation_domain_id=attempt.isolation_domain_id AND preparation.operation_id=attempt.operation_id AND preparation.fencing_token=attempt.fencing_token AND preparation.lease_owner=attempt.lease_owner AND preparation.effect_id=attempt.effect_id
 JOIN invocation_workspace_completions completion ON completion.isolation_domain_id=preparation.isolation_domain_id AND completion.operation_id=preparation.operation_id AND completion.fencing_token=preparation.fencing_token
 JOIN execution_instances instance ON instance.isolation_domain_id=operation.isolation_domain_id AND instance.operation_id=operation.id AND instance.id=preparation.execution_id
 JOIN invocation_workspace_quiescence_intents cleanup ON cleanup.isolation_domain_id=operation.isolation_domain_id AND cleanup.operation_id=operation.id AND cleanup.record=value
 JOIN invocation_workspace_owner_stop_receipts stopped ON stopped.isolation_domain_id=cleanup.isolation_domain_id AND stopped.operation_id=cleanup.operation_id
 AND stopped.owner_binding=value->>'ownerBinding' AND stopped.runtime_token=attempt.fencing_token
 JOIN external_effects cancellation ON cancellation.isolation_domain_id=operation.isolation_domain_id AND cancellation.operation_id=operation.id
 AND cancellation.operation_kind='invocation-execution' AND cancellation.phase='cancel-invocation' AND cancellation.status IN ('prepared','failed','unknown','succeeded')
 JOIN service_revision_execution_plans plan ON plan.isolation_domain_id=invocation.isolation_domain_id AND plan.revision_id=invocation.revision_id
 WHERE operation.isolation_domain_id=scope AND operation.id=op AND operation.lease_owner=owner AND operation.lease_token=token
 AND operation.command='cancel' AND operation.observed_state='cancelling' AND invocation.state='cancelling' AND operation.state_machine_version=2
 AND operation.lease_expires_at>clock_timestamp() AND operation.deadline_at>clock_timestamp()
 AND COALESCE(operation.effect_actor_id,operation.actor_id)=actor AND COALESCE(operation.effect_correlation_id,operation.correlation_id)=correlation
 AND effect.operation_kind='invocation-execution' AND effect.phase='run-invocation' AND effect.status IN ('prepared','failed','unknown','succeeded')
 AND attempt.status IN ('reserved','succeeded','failed','output_invalid')
 AND instance.observed_state='ready' AND value->>'schemaVersion'='dataground.invocation-workspace-quiescence/v1'
 AND value->>'invocationId'=invocation.id AND value->>'operationId'=operation.id AND value->>'effectId'=effect.effect_id AND value->>'executionId'=instance.id
 AND value->'source'=preparation.source_record AND value->>'planDigest'=preparation.plan_digest
 AND value->>'runtimeOwner'=attempt.lease_owner AND (value->>'runtimeToken')::bigint=attempt.fencing_token
 AND plan.schema_version='dataground.execution-plan/v2' AND plan.workspace_source=preparation.source_record AND plan.plan_digest=preparation.plan_digest
 FOR UPDATE OF invocation,operation,instance NOWAIT;
 -- A missing row is not authority. Callers must reject NULL.
 IF NOT FOUND THEN RETURN NULL; END IF;
 RETURN expires;
END; $$;
CREATE TABLE workspace_cancellation_export_decisions (
 isolation_domain_id text NOT NULL,
 id text NOT NULL CHECK (id ~ '^wed_[0-9a-z]{20,32}$'),
 contract text NOT NULL CHECK (contract='dataground.workspace-cancellation-export-authorization/v1'),
 operation_id text NOT NULL,
 request jsonb NOT NULL CHECK (jsonb_typeof(request)='object' AND octet_length(request::text)<=16384),
 lease_owner text NOT NULL CHECK (length(lease_owner) BETWEEN 1 AND 256),
 fencing_token bigint NOT NULL CHECK (fencing_token>0),
 expires_at timestamptz NOT NULL CHECK (isfinite(expires_at)),
 policy_contract text,
 policy_set_id text,
 policy_digest text,
 policy_outcome text NOT NULL CHECK (policy_outcome IN ('allowed','denied','unavailable')),
 outcome text NOT NULL CHECK (outcome IN ('allowed','denied','unavailable')),
 occurred_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(occurred_at)),
 PRIMARY KEY (isolation_domain_id,id),
 FOREIGN KEY (isolation_domain_id,operation_id) REFERENCES invocation_workspace_owner_stop_receipts(isolation_domain_id,operation_id),
 CHECK (request ?& ARRAY['source','serviceId','revisionId','invocationId','operationId','effectId','executionId','actorId','correlationId','planDigest','ownerBinding','runtimeToken','fencingToken','phase','changes']),
 CHECK (request - ARRAY['source','serviceId','revisionId','invocationId','operationId','effectId','executionId','actorId','correlationId','planDigest','ownerBinding','runtimeToken','fencingToken','phase','changes']='{}'::jsonb),
 CHECK (request->>'operationId' IS NOT DISTINCT FROM operation_id AND request->'source'->>'isolationDomainId' IS NOT DISTINCT FROM isolation_domain_id),
 CHECK ((request->>'fencingToken')::bigint IS NOT DISTINCT FROM fencing_token),
 CHECK (COALESCE(request->>'phase' IN ('capture','retain'),false)),
 CHECK ((policy_contract IS NULL AND policy_set_id IS NULL AND policy_digest IS NULL AND policy_outcome='denied' AND outcome='denied') OR
  (policy_contract IS NOT NULL AND policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6','dataground.invocation-authorization-policy/v7')
   AND policy_set_id IS NOT NULL AND policy_set_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$' AND policy_digest IS NOT NULL AND policy_digest ~ '^sha256:[0-9a-f]{64}$')),
 CHECK (policy_outcome<>'allowed' OR policy_contract IS NOT DISTINCT FROM 'dataground.invocation-authorization-policy/v7'),
 CHECK (outcome<>'allowed' OR (policy_outcome='allowed' AND expires_at>occurred_at))
);
CREATE INDEX workspace_cancellation_export_decisions_operation ON workspace_cancellation_export_decisions(isolation_domain_id,operation_id,occurred_at,id);
CREATE TRIGGER workspace_cancellation_export_decisions_append_only BEFORE UPDATE OR DELETE ON workspace_cancellation_export_decisions FOR EACH ROW EXECUTE FUNCTION reject_workspace_source_mutation();

CREATE OR REPLACE FUNCTION enforce_workspace_change_export() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT lock_workspace_change_export_claim(NEW.isolation_domain_id,NEW.operation_id,NEW.lease_owner,NEW.fencing_token,NEW.actor_id,NEW.correlation_id,NEW.record)
 AND lock_workspace_cancellation_retention(NEW.isolation_domain_id,NEW.operation_id,NEW.lease_owner,NEW.fencing_token,NEW.actor_id,NEW.correlation_id,
 (SELECT record FROM invocation_workspace_quiescence_intents WHERE isolation_domain_id=NEW.isolation_domain_id AND operation_id=NEW.operation_id)) IS NULL THEN
  RAISE EXCEPTION 'workspace change export requires a current exact invocation claim';
 END IF;
 IF EXISTS(SELECT 1 FROM invocation_execution_operations WHERE isolation_domain_id=NEW.isolation_domain_id AND id=NEW.operation_id AND command='cancel') THEN

 IF NOT EXISTS (SELECT 1 FROM invocation_workspace_quiescence_intents stopped
 WHERE stopped.isolation_domain_id=NEW.isolation_domain_id AND stopped.operation_id=NEW.operation_id
 AND stopped.record->'source'=NEW.record->'source' AND stopped.record->>'planDigest'=NEW.record->>'planDigest'
 AND stopped.record->>'invocationId'=NEW.record->>'invocationId' AND stopped.record->>'executionId'=NEW.record->>'executionId'
 AND stopped.record->>'effectId'=NEW.record->>'effectId') THEN
  RAISE EXCEPTION 'workspace retention must bind its original source and execution';
 END IF;
 END IF;
 SELECT fencing_token INTO NEW.runtime_token FROM invocation_runtime_attempts WHERE isolation_domain_id=NEW.isolation_domain_id AND operation_id=NEW.operation_id;
 RETURN NEW;
END; $$;
CREATE OR REPLACE FUNCTION enforce_workspace_change_completion() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE value jsonb;
BEGIN
 SELECT record INTO value FROM invocation_workspace_change_exports WHERE isolation_domain_id=NEW.isolation_domain_id AND operation_id=NEW.operation_id;
 IF NOT FOUND OR (NOT lock_workspace_change_export_claim(NEW.isolation_domain_id,NEW.operation_id,NEW.lease_owner,NEW.fencing_token,NEW.actor_id,NEW.correlation_id,value)
 AND lock_workspace_cancellation_retention(NEW.isolation_domain_id,NEW.operation_id,NEW.lease_owner,NEW.fencing_token,NEW.actor_id,NEW.correlation_id,
 (SELECT record FROM invocation_workspace_quiescence_intents WHERE isolation_domain_id=NEW.isolation_domain_id AND operation_id=NEW.operation_id)) IS NULL) THEN
  RAISE EXCEPTION 'workspace change completion requires a current exact invocation claim';
 END IF;
 IF EXISTS(SELECT 1 FROM invocation_execution_operations WHERE isolation_domain_id=NEW.isolation_domain_id AND id=NEW.operation_id AND command='cancel') THEN

 IF NOT EXISTS (SELECT 1 FROM invocation_workspace_quiescence_intents stopped
 WHERE stopped.isolation_domain_id=NEW.isolation_domain_id AND stopped.operation_id=NEW.operation_id
 AND stopped.record->'source'=value->'source' AND stopped.record->>'planDigest'=value->>'planDigest'
 AND stopped.record->>'invocationId'=value->>'invocationId' AND stopped.record->>'executionId'=value->>'executionId'
 AND stopped.record->>'effectId'=value->>'effectId') THEN
  RAISE EXCEPTION 'workspace retention must bind its original source and execution';
 END IF;
 END IF;
 PERFORM object.id FROM invocation_artifact_objects object JOIN artifacts public ON public.isolation_domain_id=object.isolation_domain_id AND public.id=object.id
 WHERE object.isolation_domain_id=NEW.isolation_domain_id AND object.id=NEW.artifact_id
 AND object.operation_id=NEW.operation_id AND object.invocation_id=value->>'invocationId' AND object.effect_id=value->>'effectId'
 AND object.name='Workspace changes' AND object.kind='file' AND object.media_type='application/vnd.dataground.workspace-changes+json' AND object.sensitive
 AND object.size_bytes=(value->'changes'->>'sizeBytes')::bigint AND object.artifact_digest=value->'changes'->>'digest'
 AND object.lease_owner=NEW.lease_owner AND object.fencing_token=NEW.fencing_token AND object.bound_by=NEW.actor_id AND object.correlation_id=NEW.correlation_id
 AND public.invocation_id=object.invocation_id AND public.name=object.name AND public.kind=object.kind AND public.media_type=object.media_type
 AND public.size_bytes=object.size_bytes AND public.digest=object.artifact_digest AND public.sensitive AND public.state='available';
 IF NOT FOUND THEN RAISE EXCEPTION 'workspace change completion requires its exact governed artifact'; END IF;
 RETURN NEW;
END; $$;
ALTER TABLE workspace_source_use_decisions
 ADD COLUMN stopped_operation_id text,
 ADD CONSTRAINT workspace_source_stopped_reference FOREIGN KEY(isolation_domain_id,stopped_operation_id) REFERENCES invocation_workspace_owner_stop_receipts(isolation_domain_id,operation_id),
 ADD CONSTRAINT workspace_source_stopped_binding CHECK (
  (claim_contract IS DISTINCT FROM 'dataground.invocation-source-read/v6' AND stopped_operation_id IS NULL) OR
  (claim_contract IS NOT DISTINCT FROM 'dataground.invocation-source-read/v6' AND stopped_operation_id IS NOT NULL AND stopped_operation_id=operation_id
   AND (policy_outcome<>'allowed' OR policy_contract IS NOT DISTINCT FROM 'dataground.invocation-authorization-policy/v7'))
 );
ALTER TABLE workspace_source_use_decisions DROP CONSTRAINT workspace_source_claim_binding, DROP CONSTRAINT workspace_source_policy_binding;
ALTER TABLE workspace_source_use_decisions ADD CONSTRAINT workspace_source_claim_binding CHECK (
 (claim_contract IS NULL AND invocation_id IS NULL AND effect_id IS NULL AND claim_owner IS NULL AND claim_token IS NULL AND claim_expires_at IS NULL)
 OR (claim_contract IS NOT NULL AND claim_contract IN ('dataground.invocation-source-read/v1','dataground.invocation-source-read/v2','dataground.invocation-source-read/v3','dataground.invocation-source-read/v4','dataground.invocation-source-read/v5','dataground.invocation-source-read/v6')
 AND invocation_id IS NOT NULL AND invocation_id ~ '^inv_[0-9a-z]{20,32}$'
 AND effect_id IS NOT NULL AND effect_id ~ '^eff_[0-9a-z]{20,32}$'
 AND claim_owner IS NOT NULL AND length(claim_owner)>0 AND claim_token IS NOT NULL AND claim_token>0
 AND claim_expires_at IS NOT NULL AND isfinite(claim_expires_at) AND (outcome<>'allowed' OR claim_expires_at>occurred_at))
);
ALTER TABLE workspace_source_use_decisions
 ADD CONSTRAINT workspace_source_policy_binding CHECK (
  ((claim_contract IS NULL OR claim_contract NOT IN ('dataground.invocation-source-read/v3','dataground.invocation-source-read/v4','dataground.invocation-source-read/v5','dataground.invocation-source-read/v6')) AND policy_contract IS NULL AND policy_set_id IS NULL AND policy_digest IS NULL AND policy_outcome IS NULL AND plan_digest IS NULL)
  OR (claim_contract IS NOT NULL AND claim_contract IN ('dataground.invocation-source-read/v3','dataground.invocation-source-read/v4','dataground.invocation-source-read/v5','dataground.invocation-source-read/v6')
   AND policy_contract IS NOT NULL AND policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6','dataground.invocation-authorization-policy/v7')
   AND policy_set_id IS NOT NULL AND policy_set_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
   AND policy_digest IS NOT NULL AND policy_digest ~ '^sha256:[0-9a-f]{64}$'
   AND plan_digest IS NOT NULL AND plan_digest ~ '^sha256:[0-9a-f]{64}$'
   AND policy_outcome IS NOT NULL AND policy_outcome IN ('allowed','denied','unavailable')
   AND (policy_outcome<>'allowed' OR policy_contract IN ('dataground.invocation-authorization-policy/v6','dataground.invocation-authorization-policy/v7'))
   AND (outcome<>'allowed' OR policy_outcome='allowed'))
 );
-- dataground:down
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM workspace_cancellation_export_decisions)
 OR EXISTS(SELECT 1 FROM workspace_source_use_decisions WHERE claim_contract='dataground.invocation-source-read/v6')
 OR EXISTS(SELECT 1 FROM invocation_workspace_change_exports e JOIN invocation_execution_operations o ON o.isolation_domain_id=e.isolation_domain_id AND o.id=e.operation_id WHERE o.command='cancel') THEN
  RAISE EXCEPTION 'cannot remove cancellation retention evidence';
 END IF;
END; $$;
ALTER TABLE workspace_source_use_decisions DROP CONSTRAINT workspace_source_stopped_binding, DROP CONSTRAINT workspace_source_stopped_reference, DROP COLUMN stopped_operation_id;
ALTER TABLE workspace_source_use_decisions DROP CONSTRAINT workspace_source_claim_binding, DROP CONSTRAINT workspace_source_policy_binding;
ALTER TABLE workspace_source_use_decisions ADD CONSTRAINT workspace_source_claim_binding CHECK (
 (claim_contract IS NULL AND invocation_id IS NULL AND effect_id IS NULL AND claim_owner IS NULL AND claim_token IS NULL AND claim_expires_at IS NULL)
 OR (claim_contract IS NOT NULL AND claim_contract IN ('dataground.invocation-source-read/v1','dataground.invocation-source-read/v2','dataground.invocation-source-read/v3','dataground.invocation-source-read/v4','dataground.invocation-source-read/v5')
 AND invocation_id IS NOT NULL AND invocation_id ~ '^inv_[0-9a-z]{20,32}$'
 AND effect_id IS NOT NULL AND effect_id ~ '^eff_[0-9a-z]{20,32}$'
 AND claim_owner IS NOT NULL AND length(claim_owner)>0 AND claim_token IS NOT NULL AND claim_token>0
 AND claim_expires_at IS NOT NULL AND isfinite(claim_expires_at) AND (outcome<>'allowed' OR claim_expires_at>occurred_at))
);
ALTER TABLE workspace_source_use_decisions
 ADD CONSTRAINT workspace_source_policy_binding CHECK (
  ((claim_contract IS NULL OR claim_contract NOT IN ('dataground.invocation-source-read/v3','dataground.invocation-source-read/v4','dataground.invocation-source-read/v5')) AND policy_contract IS NULL AND policy_set_id IS NULL AND policy_digest IS NULL AND policy_outcome IS NULL AND plan_digest IS NULL)
  OR (claim_contract IS NOT NULL AND claim_contract IN ('dataground.invocation-source-read/v3','dataground.invocation-source-read/v4','dataground.invocation-source-read/v5')
   AND policy_contract IS NOT NULL AND policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6','dataground.invocation-authorization-policy/v7')
   AND policy_set_id IS NOT NULL AND policy_set_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
   AND policy_digest IS NOT NULL AND policy_digest ~ '^sha256:[0-9a-f]{64}$'
   AND plan_digest IS NOT NULL AND plan_digest ~ '^sha256:[0-9a-f]{64}$'
   AND policy_outcome IS NOT NULL AND policy_outcome IN ('allowed','denied','unavailable')
   AND (policy_outcome<>'allowed' OR policy_contract IN ('dataground.invocation-authorization-policy/v6','dataground.invocation-authorization-policy/v7'))
   AND (outcome<>'allowed' OR policy_outcome='allowed'))
 );
CREATE OR REPLACE FUNCTION enforce_workspace_change_export() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT lock_workspace_change_export_claim(NEW.isolation_domain_id,NEW.operation_id,NEW.lease_owner,NEW.fencing_token,NEW.actor_id,NEW.correlation_id,NEW.record) THEN
  RAISE EXCEPTION 'workspace change export requires a current exact invocation claim';
 END IF;
 SELECT fencing_token INTO NEW.runtime_token FROM invocation_runtime_attempts WHERE isolation_domain_id=NEW.isolation_domain_id AND operation_id=NEW.operation_id;
 RETURN NEW;
END; $$;
CREATE OR REPLACE FUNCTION enforce_workspace_change_completion() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE value jsonb;
BEGIN
 SELECT record INTO value FROM invocation_workspace_change_exports WHERE isolation_domain_id=NEW.isolation_domain_id AND operation_id=NEW.operation_id;
 IF NOT FOUND OR NOT lock_workspace_change_export_claim(NEW.isolation_domain_id,NEW.operation_id,NEW.lease_owner,NEW.fencing_token,NEW.actor_id,NEW.correlation_id,value) THEN
  RAISE EXCEPTION 'workspace change completion requires a current exact invocation claim';
 END IF;
 PERFORM object.id FROM invocation_artifact_objects object JOIN artifacts public ON public.isolation_domain_id=object.isolation_domain_id AND public.id=object.id
 WHERE object.isolation_domain_id=NEW.isolation_domain_id AND object.id=NEW.artifact_id
 AND object.operation_id=NEW.operation_id AND object.invocation_id=value->>'invocationId' AND object.effect_id=value->>'effectId'
 AND object.name='Workspace changes' AND object.kind='file' AND object.media_type='application/vnd.dataground.workspace-changes+json' AND object.sensitive
 AND object.size_bytes=(value->'changes'->>'sizeBytes')::bigint AND object.artifact_digest=value->'changes'->>'digest'
 AND object.lease_owner=NEW.lease_owner AND object.fencing_token=NEW.fencing_token AND object.bound_by=NEW.actor_id AND object.correlation_id=NEW.correlation_id
 AND public.invocation_id=object.invocation_id AND public.name=object.name AND public.kind=object.kind AND public.media_type=object.media_type
 AND public.size_bytes=object.size_bytes AND public.digest=object.artifact_digest AND public.sensitive AND public.state='available';
 IF NOT FOUND THEN RAISE EXCEPTION 'workspace change completion requires its exact governed artifact'; END IF;
 RETURN NEW;
END; $$;

DROP TABLE workspace_cancellation_export_decisions;
DROP FUNCTION lock_workspace_cancellation_retention(text,text,text,bigint,text,text,jsonb);
