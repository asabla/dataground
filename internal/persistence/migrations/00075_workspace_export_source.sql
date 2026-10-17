-- dataground:up
ALTER TABLE workspace_source_use_decisions
 ADD COLUMN cleanup_operation_id text,
 ADD CONSTRAINT workspace_source_cleanup_reference FOREIGN KEY(isolation_domain_id,cleanup_operation_id) REFERENCES invocation_workspace_quiescence_completions(isolation_domain_id,operation_id),
 ADD CONSTRAINT workspace_source_cleanup_binding CHECK (
  (claim_contract IS DISTINCT FROM 'dataground.invocation-source-read/v5' AND cleanup_operation_id IS NULL) OR
  (claim_contract IS NOT DISTINCT FROM 'dataground.invocation-source-read/v5' AND cleanup_operation_id IS NOT NULL AND cleanup_operation_id=operation_id
   AND (policy_outcome<>'allowed' OR policy_contract IS NOT DISTINCT FROM 'dataground.invocation-authorization-policy/v7'))
 );
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
-- dataground:down
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM workspace_source_use_decisions WHERE claim_contract='dataground.invocation-source-read/v5') THEN
  RAISE EXCEPTION 'cannot remove export source read evidence';
 END IF;
END; $$;
ALTER TABLE workspace_source_use_decisions DROP CONSTRAINT workspace_source_cleanup_binding, DROP CONSTRAINT workspace_source_cleanup_reference, DROP COLUMN cleanup_operation_id;
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
   AND policy_contract IS NOT NULL AND policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6','dataground.invocation-authorization-policy/v7')
   AND policy_set_id IS NOT NULL AND policy_set_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
   AND policy_digest IS NOT NULL AND policy_digest ~ '^sha256:[0-9a-f]{64}$'
   AND plan_digest IS NOT NULL AND plan_digest ~ '^sha256:[0-9a-f]{64}$'
   AND policy_outcome IS NOT NULL AND policy_outcome IN ('allowed','denied','unavailable')
   AND (policy_outcome<>'allowed' OR policy_contract IN ('dataground.invocation-authorization-policy/v6','dataground.invocation-authorization-policy/v7'))
   AND (outcome<>'allowed' OR policy_outcome='allowed'))
 );
