-- dataground:up
CREATE TABLE invocation_workspace_change_exports (
 isolation_domain_id text NOT NULL,
 operation_id text NOT NULL,
 record jsonb NOT NULL,
 runtime_token bigint NOT NULL,
 lease_owner text NOT NULL CHECK (length(lease_owner) BETWEEN 1 AND 256),
 fencing_token bigint NOT NULL CHECK (fencing_token>0),
 actor_id text NOT NULL,
 correlation_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(created_at)),
 PRIMARY KEY (isolation_domain_id,operation_id),
 FOREIGN KEY (isolation_domain_id,operation_id,runtime_token) REFERENCES invocation_workspace_preparations(isolation_domain_id,operation_id,fencing_token),
 CHECK (jsonb_typeof(record)='object' AND record ?& ARRAY['schemaVersion','source','invocationId','operationId','effectId','executionId','planDigest','changes']),
 CHECK (record - ARRAY['schemaVersion','source','invocationId','operationId','effectId','executionId','planDigest','changes']='{}'::jsonb),
 CHECK (record->>'schemaVersion' IS NOT DISTINCT FROM 'dataground.invocation-workspace-change-export/v1'),
 CHECK (record->>'operationId' IS NOT DISTINCT FROM operation_id AND record->'source'->>'isolationDomainId' IS NOT DISTINCT FROM isolation_domain_id),
 CHECK (jsonb_typeof(record->'changes')='object' AND (record->'changes') ?& ARRAY['schemaVersion','digest','sizeBytes','baseArchiveDigest','resultArchiveDigest','changeCount','entryCount','sourceBytes']),
 CHECK ((record->'changes') - ARRAY['schemaVersion','digest','sizeBytes','baseArchiveDigest','resultArchiveDigest','changeCount','entryCount','sourceBytes']='{}'::jsonb),
 CHECK (record->'changes'->>'schemaVersion' IS NOT DISTINCT FROM 'dataground.workspace-changes/v1'),
 CHECK (record->'changes'->>'baseArchiveDigest' IS NOT DISTINCT FROM record->'source'->>'archiveDigest'),
 CHECK (COALESCE(record->'changes'->>'digest' ~ '^sha256:[0-9a-f]{64}$',false) AND COALESCE(record->'changes'->>'resultArchiveDigest' ~ '^sha256:[0-9a-f]{64}$',false)),
 CHECK (COALESCE((record->'changes'->>'sizeBytes')::bigint BETWEEN 1 AND 67108864,false)),
 CHECK (COALESCE((record->'changes'->>'changeCount')::bigint BETWEEN 0 AND 8192,false)),
 CHECK (COALESCE((record->'changes'->>'entryCount')::bigint BETWEEN 0 AND 4096,false)),
 CHECK (COALESCE((record->'changes'->>'sourceBytes')::bigint BETWEEN 0 AND 25165824,false))
);
CREATE TABLE invocation_workspace_change_completions (
 isolation_domain_id text NOT NULL,
 operation_id text NOT NULL,
 artifact_id text NOT NULL,
 lease_owner text NOT NULL CHECK (length(lease_owner) BETWEEN 1 AND 256),
 fencing_token bigint NOT NULL CHECK (fencing_token>0),
 actor_id text NOT NULL,
 correlation_id text NOT NULL,
 completed_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(completed_at)),
 PRIMARY KEY (isolation_domain_id,operation_id),
 UNIQUE (isolation_domain_id,artifact_id),
 FOREIGN KEY (isolation_domain_id,operation_id) REFERENCES invocation_workspace_change_exports(isolation_domain_id,operation_id),
 FOREIGN KEY (isolation_domain_id,artifact_id) REFERENCES invocation_artifact_objects(isolation_domain_id,id),
 CHECK (artifact_id='art_' || substring(encode(sha256(convert_to(isolation_domain_id || ':' || operation_id || ':workspace-changes:v1','UTF8')),'hex') FROM 1 FOR 20))
);
CREATE TRIGGER workspace_change_exports_append_only BEFORE UPDATE OR DELETE ON invocation_workspace_change_exports FOR EACH ROW EXECUTE FUNCTION reject_workspace_source_mutation();
CREATE TRIGGER workspace_change_completions_append_only BEFORE UPDATE OR DELETE ON invocation_workspace_change_completions FOR EACH ROW EXECUTE FUNCTION reject_workspace_source_mutation();

-- A current worker can retain the output of the original runtime attempt. This
-- does not change that attempt's owner or permit a replacement native turn.
CREATE FUNCTION lock_workspace_change_export_claim(scope text,op text,owner text,token bigint,actor text,correlation text,value jsonb) RETURNS boolean LANGUAGE plpgsql AS $$
BEGIN
 PERFORM operation.id FROM invocation_execution_operations operation
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
 AND attempt.status IN ('reserved','succeeded','failed','output_invalid') AND instance.observed_state='ready'
 AND value->>'schemaVersion'='dataground.invocation-workspace-change-export/v1'
 AND value->>'invocationId'=invocation.id AND value->>'operationId'=operation.id AND value->>'effectId'=effect.effect_id AND value->>'executionId'=instance.id
 AND value->'source'=preparation.source_record AND value->>'planDigest'=preparation.plan_digest
 AND plan.schema_version='dataground.execution-plan/v2' AND plan.workspace_source=preparation.source_record AND plan.plan_digest=preparation.plan_digest
 FOR UPDATE OF invocation,operation,instance NOWAIT;
 RETURN FOUND;
END; $$;
CREATE FUNCTION enforce_workspace_change_export() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT lock_workspace_change_export_claim(NEW.isolation_domain_id,NEW.operation_id,NEW.lease_owner,NEW.fencing_token,NEW.actor_id,NEW.correlation_id,NEW.record) THEN
  RAISE EXCEPTION 'workspace change export requires a current exact invocation claim';
 END IF;
 SELECT fencing_token INTO NEW.runtime_token FROM invocation_runtime_attempts WHERE isolation_domain_id=NEW.isolation_domain_id AND operation_id=NEW.operation_id;
 RETURN NEW;
END; $$;
CREATE TRIGGER workspace_change_export_binding BEFORE INSERT ON invocation_workspace_change_exports FOR EACH ROW EXECUTE FUNCTION enforce_workspace_change_export();
CREATE FUNCTION enforce_workspace_change_completion() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER workspace_change_completion_binding BEFORE INSERT ON invocation_workspace_change_completions FOR EACH ROW EXECUTE FUNCTION enforce_workspace_change_completion();

-- dataground:down
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM invocation_workspace_change_exports) OR EXISTS(SELECT 1 FROM invocation_workspace_change_completions) THEN
  RAISE EXCEPTION 'cannot remove workspace change export evidence';
 END IF;
END; $$;
DROP TABLE invocation_workspace_change_completions;
DROP TABLE invocation_workspace_change_exports;
DROP FUNCTION enforce_workspace_change_completion();
DROP FUNCTION enforce_workspace_change_export();
DROP FUNCTION lock_workspace_change_export_claim(text,text,text,bigint,text,text,jsonb);
