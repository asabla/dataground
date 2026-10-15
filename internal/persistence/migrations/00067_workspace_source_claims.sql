-- dataground:up
ALTER TABLE workspace_source_use_decisions
 ADD COLUMN claim_contract text,
 ADD COLUMN invocation_id text,
 ADD COLUMN effect_id text,
 ADD COLUMN claim_owner text,
 ADD COLUMN claim_token bigint,
 ADD COLUMN claim_expires_at timestamptz,
 ADD CONSTRAINT workspace_source_claim_binding CHECK (
  (claim_contract IS NULL AND invocation_id IS NULL AND effect_id IS NULL AND claim_owner IS NULL AND claim_token IS NULL AND claim_expires_at IS NULL)
  OR
  (claim_contract IS NOT NULL AND claim_contract='dataground.invocation-source-read/v1'
   AND invocation_id IS NOT NULL AND invocation_id ~ '^inv_[0-9a-z]{20,32}$'
   AND effect_id IS NOT NULL AND effect_id ~ '^eff_[0-9a-z]{20,32}$'
   AND claim_owner IS NOT NULL AND length(claim_owner)>0
   AND claim_token IS NOT NULL AND claim_token>0
   AND claim_expires_at IS NOT NULL AND isfinite(claim_expires_at)
   AND (outcome<>'allowed' OR claim_expires_at>occurred_at))
 );

-- dataground:down
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM workspace_source_use_decisions WHERE claim_contract IS NOT NULL) THEN RAISE EXCEPTION 'cannot remove invocation source claim evidence'; END IF;
END; $$;
ALTER TABLE workspace_source_use_decisions
 DROP CONSTRAINT workspace_source_claim_binding,
 DROP COLUMN claim_contract,
 DROP COLUMN invocation_id,
 DROP COLUMN effect_id,
 DROP COLUMN claim_owner,
 DROP COLUMN claim_token,
 DROP COLUMN claim_expires_at;
