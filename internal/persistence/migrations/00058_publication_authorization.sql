-- dataground:up

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

CREATE TABLE publication_authorization_decisions (
 sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 contract text NOT NULL CHECK (contract='dataground.publication-authorization-decision/v1'),
 isolation_domain_id text NOT NULL CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
 service_id text NOT NULL CHECK (service_id ~ '^svc_[0-9a-z]{20,32}$'),
 revision_id text NOT NULL CHECK (revision_id ~ '^rev_[0-9a-z]{20,32}$'),
 operation_id text NOT NULL CHECK (operation_id ~ '^op_[0-9a-z]{20,32}$'),
 actor_id text NOT NULL CHECK (length(actor_id) BETWEEN 1 AND 256 AND actor_id !~ '[[:cntrl:]]'),
 correlation_id text NOT NULL CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
 phase text NOT NULL CHECK (phase IN ('entry','effect')),
 expected_version bigint NOT NULL CHECK (expected_version BETWEEN 1 AND 9007199254740991),
 fencing_token bigint NOT NULL CHECK ((phase='entry' AND fencing_token=0) OR (phase='effect' AND fencing_token BETWEEN 1 AND 9007199254740991)),
 plan_digest text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
 verification_digest text NOT NULL CHECK (verification_digest ~ '^sha256:[0-9a-f]{64}$'),
 policy_contract text NOT NULL CHECK (policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4','dataground.invocation-authorization-policy/v5')),
 policy_set_id text NOT NULL CHECK (policy_set_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
 policy_digest text NOT NULL CHECK (policy_digest ~ '^sha256:[0-9a-f]{64}$'),
 outcome text NOT NULL CHECK (outcome IN ('allowed','denied','unavailable')),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(recorded_at)),
 CHECK (outcome<>'allowed' OR policy_contract='dataground.invocation-authorization-policy/v5')
);
CREATE INDEX publication_decisions_scope_sequence_idx ON publication_authorization_decisions(isolation_domain_id,operation_id,sequence);
CREATE TRIGGER publication_decisions_append_only BEFORE UPDATE OR DELETE ON publication_authorization_decisions
 FOR EACH ROW EXECUTE FUNCTION reject_invocation_authorization_decision_mutation();

-- dataground:down
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM publication_authorization_decisions)
 OR EXISTS (SELECT 1 FROM invocation_authorization_policies WHERE contract='dataground.invocation-authorization-policy/v5')
 OR EXISTS (SELECT 1 FROM invocation_question_authorization_decisions WHERE policy_contract='dataground.invocation-authorization-policy/v5') THEN
  RAISE EXCEPTION 'publication authorization evidence prevents downgrade';
 END IF;
END; $$;
DROP TABLE publication_authorization_decisions;
ALTER TABLE invocation_authorization_policies
 DROP CONSTRAINT invocation_authorization_policies_contract_check,
 DROP CONSTRAINT invocation_authorization_policies_entities_check,
 ADD CONSTRAINT invocation_authorization_policies_contract_check CHECK (contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4')),
 ADD CONSTRAINT invocation_authorization_policies_entities_check CHECK (
  (contract='dataground.invocation-authorization-policy/v1' AND cedar_entities IS NULL) OR
  (contract IN ('dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4') AND octet_length(cedar_entities) BETWEEN 1 AND 1048576)
 );
ALTER TABLE invocation_question_authorization_decisions
 DROP CONSTRAINT invocation_question_authorization_decisio_policy_contract_check,
 DROP CONSTRAINT invocation_question_authorization_decisions_check2,
 ADD CONSTRAINT invocation_question_authorization_decisio_policy_contract_check CHECK (policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4')),
 ADD CONSTRAINT invocation_question_authorization_decisions_check2 CHECK (outcome<>'allowed' OR policy_contract IN ('dataground.invocation-authorization-policy/v4'));
