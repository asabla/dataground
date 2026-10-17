-- dataground:up
ALTER TABLE invocation_authorization_policies
 DROP CONSTRAINT invocation_authorization_policies_contract_check,
 DROP CONSTRAINT invocation_authorization_policies_entities_check,
 ADD CONSTRAINT invocation_authorization_policies_contract_check CHECK (contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6','dataground.invocation-authorization-policy/v7')),
 ADD CONSTRAINT invocation_authorization_policies_entities_check CHECK (
  (contract='dataground.invocation-authorization-policy/v1' AND cedar_entities IS NULL) OR
  (contract IN ('dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6','dataground.invocation-authorization-policy/v7') AND octet_length(cedar_entities) BETWEEN 1 AND 1048576)
 );
ALTER TABLE invocation_question_authorization_decisions
 DROP CONSTRAINT invocation_question_authorization_decisio_policy_contract_check,
 DROP CONSTRAINT invocation_question_authorization_decisions_check2,
 ADD CONSTRAINT invocation_question_authorization_decisio_policy_contract_check CHECK (policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6','dataground.invocation-authorization-policy/v7')),
 ADD CONSTRAINT invocation_question_authorization_decisions_check2 CHECK (outcome<>'allowed' OR policy_contract IN ('dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6','dataground.invocation-authorization-policy/v7'));
-- Preserve the separate phase/token constraint repaired in schema 71.
ALTER TABLE publication_authorization_decisions
 DROP CONSTRAINT publication_authorization_decisions_policy_contract_check,
 DROP CONSTRAINT publication_authorization_decisions_check1,
 ADD CONSTRAINT publication_authorization_decisions_policy_contract_check CHECK (policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6','dataground.invocation-authorization-policy/v7')),
 ADD CONSTRAINT publication_authorization_decisions_check1 CHECK (outcome<>'allowed' OR policy_contract IN ('dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6','dataground.invocation-authorization-policy/v7'));
ALTER TABLE workspace_source_use_decisions DROP CONSTRAINT workspace_source_policy_binding;
ALTER TABLE workspace_source_use_decisions
 ADD CONSTRAINT workspace_source_policy_binding CHECK (
  ((claim_contract IS NULL OR claim_contract NOT IN ('dataground.invocation-source-read/v3','dataground.invocation-source-read/v4')) AND policy_contract IS NULL AND policy_set_id IS NULL AND policy_digest IS NULL AND policy_outcome IS NULL AND plan_digest IS NULL)
  OR (claim_contract IS NOT NULL AND claim_contract IN ('dataground.invocation-source-read/v3','dataground.invocation-source-read/v4')
   AND policy_contract IS NOT NULL AND policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6','dataground.invocation-authorization-policy/v7')
   AND policy_set_id IS NOT NULL AND policy_set_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
   AND policy_digest IS NOT NULL AND policy_digest ~ '^sha256:[0-9a-f]{64}$'
   AND plan_digest IS NOT NULL AND plan_digest ~ '^sha256:[0-9a-f]{64}$'
   AND policy_outcome IS NOT NULL AND policy_outcome IN ('allowed','denied','unavailable')
   AND (policy_outcome<>'allowed' OR policy_contract IN ('dataground.invocation-authorization-policy/v6','dataground.invocation-authorization-policy/v7'))
   AND (outcome<>'allowed' OR policy_outcome='allowed'))
 );

-- Export may use a replacement claim, but must retain the original stopped owner.
CREATE FUNCTION lock_workspace_export_authorization(scope text,op text,owner text,token bigint,actor text,correlation text,value jsonb) RETURNS timestamptz LANGUAGE plpgsql AS $$
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
 JOIN invocation_workspace_quiescence_completions confirmed ON confirmed.isolation_domain_id=cleanup.isolation_domain_id AND confirmed.operation_id=cleanup.operation_id
 JOIN service_revision_execution_plans plan ON plan.isolation_domain_id=invocation.isolation_domain_id AND plan.revision_id=invocation.revision_id
 WHERE operation.isolation_domain_id=scope AND operation.id=op AND operation.lease_owner=owner AND operation.lease_token=token
 AND operation.command IN ('invoke','repair') AND operation.observed_state='running' AND operation.state_machine_version=2
 AND operation.lease_expires_at>clock_timestamp() AND operation.deadline_at>clock_timestamp()
 AND COALESCE(operation.effect_actor_id,operation.actor_id)=actor AND COALESCE(operation.effect_correlation_id,operation.correlation_id)=correlation
 AND effect.operation_kind='invocation-execution' AND effect.phase='run-invocation' AND effect.status IN ('prepared','failed','unknown')
 AND attempt.status IN ('reserved','succeeded','failed','output_invalid')
 AND instance.observed_state='ready' AND value->>'schemaVersion'='dataground.invocation-workspace-quiescence/v1'
 AND value->>'invocationId'=invocation.id AND value->>'operationId'=operation.id AND value->>'effectId'=effect.effect_id AND value->>'executionId'=instance.id
 AND value->'source'=preparation.source_record AND value->>'planDigest'=preparation.plan_digest
 AND value->>'runtimeOwner'=attempt.lease_owner AND (value->>'runtimeToken')::bigint=attempt.fencing_token
 AND plan.schema_version='dataground.execution-plan/v2' AND plan.workspace_source=preparation.source_record AND plan.plan_digest=preparation.plan_digest
 FOR UPDATE OF invocation,operation,instance NOWAIT;
 IF NOT FOUND THEN RAISE EXCEPTION 'workspace export requires a current claim and exact confirmed cleanup'; END IF;
 RETURN expires;
END; $$;
CREATE TABLE workspace_change_export_decisions (
 isolation_domain_id text NOT NULL,
 id text NOT NULL CHECK (id ~ '^wed_[0-9a-z]{20,32}$'),
 contract text NOT NULL CHECK (contract='dataground.workspace-change-export-authorization/v1'),
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
 FOREIGN KEY (isolation_domain_id,operation_id) REFERENCES invocation_workspace_quiescence_completions(isolation_domain_id,operation_id),
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
CREATE INDEX workspace_change_export_decisions_operation ON workspace_change_export_decisions(isolation_domain_id,operation_id,occurred_at,id);
CREATE TRIGGER workspace_change_export_decisions_append_only BEFORE UPDATE OR DELETE ON workspace_change_export_decisions FOR EACH ROW EXECUTE FUNCTION reject_workspace_source_mutation();

-- dataground:down
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM workspace_change_export_decisions)
 OR EXISTS (SELECT 1 FROM invocation_authorization_policies WHERE contract='dataground.invocation-authorization-policy/v7')
 OR EXISTS (SELECT 1 FROM invocation_question_authorization_decisions WHERE policy_contract='dataground.invocation-authorization-policy/v7')
 OR EXISTS (SELECT 1 FROM publication_authorization_decisions WHERE policy_contract='dataground.invocation-authorization-policy/v7')
 OR EXISTS (SELECT 1 FROM workspace_source_use_decisions WHERE policy_contract='dataground.invocation-authorization-policy/v7') THEN
  RAISE EXCEPTION 'cannot remove workspace export authorization evidence';
 END IF;
END; $$;
DROP TABLE workspace_change_export_decisions;
DROP FUNCTION lock_workspace_export_authorization(text,text,text,bigint,text,text,jsonb);
ALTER TABLE invocation_authorization_policies
 DROP CONSTRAINT invocation_authorization_policies_contract_check,
 DROP CONSTRAINT invocation_authorization_policies_entities_check,
 ADD CONSTRAINT invocation_authorization_policies_contract_check CHECK (contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6')),
 ADD CONSTRAINT invocation_authorization_policies_entities_check CHECK (
  (contract='dataground.invocation-authorization-policy/v1' AND cedar_entities IS NULL) OR
  (contract IN ('dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6') AND octet_length(cedar_entities) BETWEEN 1 AND 1048576)
 );
ALTER TABLE invocation_question_authorization_decisions
 DROP CONSTRAINT invocation_question_authorization_decisio_policy_contract_check,
 DROP CONSTRAINT invocation_question_authorization_decisions_check2,
 ADD CONSTRAINT invocation_question_authorization_decisio_policy_contract_check CHECK (policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6')),
 ADD CONSTRAINT invocation_question_authorization_decisions_check2 CHECK (outcome<>'allowed' OR policy_contract IN ('dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6'));
ALTER TABLE publication_authorization_decisions
 DROP CONSTRAINT publication_authorization_decisions_policy_contract_check,
 DROP CONSTRAINT publication_authorization_decisions_check1,
 ADD CONSTRAINT publication_authorization_decisions_policy_contract_check CHECK (policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6')),
 ADD CONSTRAINT publication_authorization_decisions_check1 CHECK (outcome<>'allowed' OR policy_contract IN ('dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6'));
ALTER TABLE workspace_source_use_decisions DROP CONSTRAINT workspace_source_policy_binding;
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
