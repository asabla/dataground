-- dataground:up
ALTER TABLE service_revision_execution_plans
 DROP CONSTRAINT service_revision_execution_plans_schema_version_check,
 ADD COLUMN workspace_source jsonb,
 ADD CONSTRAINT execution_plan_source_version CHECK (
  (schema_version='dataground.execution-plan/v1' AND workspace_source IS NULL)
  OR (schema_version='dataground.execution-plan/v2' AND workspace_source IS NOT NULL AND jsonb_typeof(workspace_source)='object')
 );
CREATE FUNCTION enforce_execution_plan_source() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN
  IF OLD.schema_version='dataground.execution-plan/v2' OR (TG_OP='UPDATE' AND NEW.schema_version='dataground.execution-plan/v2') THEN
   RAISE EXCEPTION 'source execution plan is immutable';
  END IF;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
 END IF;
 IF NEW.schema_version='dataground.execution-plan/v2' THEN
  PERFORM id FROM service_revisions WHERE isolation_domain_id=NEW.isolation_domain_id AND id=NEW.revision_id AND state='draft' FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'source plan requires a draft revision'; END IF;
  IF NOT EXISTS (
   SELECT 1 FROM workspace_source_installations source JOIN workspace_source_completions completion
   ON completion.isolation_domain_id=source.isolation_domain_id AND completion.workspace_id=source.workspace_id AND completion.id=source.id
   WHERE source.isolation_domain_id=NEW.isolation_domain_id AND NEW.workspace_source=jsonb_build_object(
    'schemaVersion','dataground.workspace-source/v1','isolationDomainId',source.isolation_domain_id,
    'workspaceId',source.workspace_id,'id',source.id,'archiveDigest',source.archive_digest,
    'storageProfileDigest',source.storage_profile_digest,'archiveBytes',source.archive_bytes,
    'entryCount',source.entry_count,'sourceBytes',source.source_bytes)
  ) THEN RAISE EXCEPTION 'source plan requires an exact completed source'; END IF;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER execution_plan_source_binding BEFORE INSERT OR UPDATE OR DELETE ON service_revision_execution_plans FOR EACH ROW EXECUTE FUNCTION enforce_execution_plan_source();
ALTER TABLE workspace_source_use_decisions DROP CONSTRAINT workspace_source_claim_binding;
ALTER TABLE workspace_source_use_decisions ADD CONSTRAINT workspace_source_claim_binding CHECK (
 (claim_contract IS NULL AND invocation_id IS NULL AND effect_id IS NULL AND claim_owner IS NULL AND claim_token IS NULL AND claim_expires_at IS NULL)
 OR (claim_contract IS NOT NULL AND claim_contract IN ('dataground.invocation-source-read/v1','dataground.invocation-source-read/v2')
 AND invocation_id IS NOT NULL AND invocation_id ~ '^inv_[0-9a-z]{20,32}$'
 AND effect_id IS NOT NULL AND effect_id ~ '^eff_[0-9a-z]{20,32}$'
 AND claim_owner IS NOT NULL AND length(claim_owner)>0 AND claim_token IS NOT NULL AND claim_token>0
 AND claim_expires_at IS NOT NULL AND isfinite(claim_expires_at) AND (outcome<>'allowed' OR claim_expires_at>occurred_at))
);

-- dataground:down
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM service_revision_execution_plans WHERE schema_version='dataground.execution-plan/v2') OR EXISTS (SELECT 1 FROM workspace_source_use_decisions WHERE claim_contract='dataground.invocation-source-read/v2') THEN RAISE EXCEPTION 'cannot remove execution source binding evidence'; END IF;
END; $$;
DROP TRIGGER execution_plan_source_binding ON service_revision_execution_plans;
DROP FUNCTION enforce_execution_plan_source();
ALTER TABLE service_revision_execution_plans DROP CONSTRAINT execution_plan_source_version, DROP COLUMN workspace_source,
 ADD CONSTRAINT service_revision_execution_plans_schema_version_check CHECK (schema_version='dataground.execution-plan/v1');
ALTER TABLE workspace_source_use_decisions DROP CONSTRAINT workspace_source_claim_binding;
ALTER TABLE workspace_source_use_decisions ADD CONSTRAINT workspace_source_claim_binding CHECK (
 (claim_contract IS NULL AND invocation_id IS NULL AND effect_id IS NULL AND claim_owner IS NULL AND claim_token IS NULL AND claim_expires_at IS NULL)
 OR (claim_contract IS NOT NULL AND claim_contract='dataground.invocation-source-read/v1'
 AND invocation_id IS NOT NULL AND invocation_id ~ '^inv_[0-9a-z]{20,32}$'
 AND effect_id IS NOT NULL AND effect_id ~ '^eff_[0-9a-z]{20,32}$'
 AND claim_owner IS NOT NULL AND length(claim_owner)>0 AND claim_token IS NOT NULL AND claim_token>0
 AND claim_expires_at IS NOT NULL AND isfinite(claim_expires_at) AND (outcome<>'allowed' OR claim_expires_at>occurred_at))
);
