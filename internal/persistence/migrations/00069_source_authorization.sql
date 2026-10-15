-- dataground:up
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
 DROP CONSTRAINT publication_authorization_decisions_check,
 ADD CONSTRAINT publication_authorization_decisions_policy_contract_check CHECK (policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6')),
 ADD CONSTRAINT publication_authorization_decisions_check CHECK (outcome<>'allowed' OR policy_contract IN ('dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6'));
ALTER TABLE workspace_source_use_decisions DROP CONSTRAINT workspace_source_claim_binding;

ALTER TABLE workspace_source_use_decisions ADD CONSTRAINT workspace_source_claim_binding CHECK (
 (claim_contract IS NULL AND invocation_id IS NULL AND effect_id IS NULL AND claim_owner IS NULL AND claim_token IS NULL AND claim_expires_at IS NULL)
 OR (claim_contract IS NOT NULL AND claim_contract IN ('dataground.invocation-source-read/v1','dataground.invocation-source-read/v2','dataground.invocation-source-read/v3')
 AND invocation_id IS NOT NULL AND invocation_id ~ '^inv_[0-9a-z]{20,32}$'
 AND effect_id IS NOT NULL AND effect_id ~ '^eff_[0-9a-z]{20,32}$'
 AND claim_owner IS NOT NULL AND length(claim_owner)>0 AND claim_token IS NOT NULL AND claim_token>0
 AND claim_expires_at IS NOT NULL AND isfinite(claim_expires_at) AND (outcome<>'allowed' OR claim_expires_at>occurred_at))
);

ALTER TABLE workspace_source_use_decisions
 ADD COLUMN policy_contract text,
 ADD COLUMN policy_set_id text,
 ADD COLUMN policy_digest text,
 ADD COLUMN policy_outcome text,
 ADD COLUMN plan_digest text,
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
CREATE FUNCTION lock_invocation_policy_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('invocation-authorization-policy-scope' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.service_id || E'\n' || NEW.revision_id,0));
 RETURN NEW;
END;
$$;
CREATE TRIGGER source_read_policy_withdrawal_lock BEFORE INSERT ON invocation_authorization_policy_withdrawals FOR EACH ROW EXECUTE FUNCTION lock_invocation_policy_change();
CREATE TRIGGER source_read_entity_activation_lock BEFORE INSERT ON invocation_authorization_entity_activations FOR EACH ROW EXECUTE FUNCTION lock_invocation_policy_change();

-- dataground:down
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM invocation_authorization_policies WHERE contract='dataground.invocation-authorization-policy/v6')
 OR EXISTS (SELECT 1 FROM invocation_question_authorization_decisions WHERE policy_contract='dataground.invocation-authorization-policy/v6')
 OR EXISTS (SELECT 1 FROM publication_authorization_decisions WHERE policy_contract='dataground.invocation-authorization-policy/v6')
 OR EXISTS (SELECT 1 FROM workspace_source_use_decisions WHERE claim_contract='dataground.invocation-source-read/v3') THEN RAISE EXCEPTION 'cannot remove source authorization evidence'; END IF;
END; $$;
DROP TRIGGER source_read_policy_withdrawal_lock ON invocation_authorization_policy_withdrawals;
DROP TRIGGER source_read_entity_activation_lock ON invocation_authorization_entity_activations;
DROP FUNCTION lock_invocation_policy_change();
ALTER TABLE workspace_source_use_decisions DROP CONSTRAINT workspace_source_policy_binding, DROP COLUMN policy_contract, DROP COLUMN policy_set_id, DROP COLUMN policy_digest, DROP COLUMN policy_outcome, DROP COLUMN plan_digest;
ALTER TABLE workspace_source_use_decisions DROP CONSTRAINT workspace_source_claim_binding;

ALTER TABLE workspace_source_use_decisions ADD CONSTRAINT workspace_source_claim_binding CHECK (
 (claim_contract IS NULL AND invocation_id IS NULL AND effect_id IS NULL AND claim_owner IS NULL AND claim_token IS NULL AND claim_expires_at IS NULL)
 OR (claim_contract IS NOT NULL AND claim_contract IN ('dataground.invocation-source-read/v1','dataground.invocation-source-read/v2')
 AND invocation_id IS NOT NULL AND invocation_id ~ '^inv_[0-9a-z]{20,32}$'
 AND effect_id IS NOT NULL AND effect_id ~ '^eff_[0-9a-z]{20,32}$'
 AND claim_owner IS NOT NULL AND length(claim_owner)>0 AND claim_token IS NOT NULL AND claim_token>0
 AND claim_expires_at IS NOT NULL AND isfinite(claim_expires_at) AND (outcome<>'allowed' OR claim_expires_at>occurred_at))
);

ALTER TABLE invocation_authorization_policies
 DROP CONSTRAINT invocation_authorization_policies_contract_check,
 DROP CONSTRAINT invocation_authorization_policies_entities_check,
 ADD CONSTRAINT invocation_authorization_policies_contract_check CHECK (contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5')),
 ADD CONSTRAINT invocation_authorization_policies_entities_check CHECK (
  (contract='dataground.invocation-authorization-policy/v1' AND cedar_entities IS NULL) OR
  (contract IN ('dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5') AND octet_length(cedar_entities) BETWEEN 1 AND 1048576)
 );
ALTER TABLE invocation_question_authorization_decisions
 DROP CONSTRAINT invocation_question_authorization_decisio_policy_contract_check,
 DROP CONSTRAINT invocation_question_authorization_decisions_check2,
 ADD CONSTRAINT invocation_question_authorization_decisio_policy_contract_check CHECK (policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5')),
 ADD CONSTRAINT invocation_question_authorization_decisions_check2 CHECK (outcome<>'allowed' OR policy_contract IN ('dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5'));
ALTER TABLE publication_authorization_decisions
 DROP CONSTRAINT publication_authorization_decisions_policy_contract_check,
 DROP CONSTRAINT publication_authorization_decisions_check,
 ADD CONSTRAINT publication_authorization_decisions_policy_contract_check CHECK (policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5')),
 ADD CONSTRAINT publication_authorization_decisions_check CHECK (outcome<>'allowed' OR policy_contract IN ('dataground.invocation-authorization-policy/v5'));
