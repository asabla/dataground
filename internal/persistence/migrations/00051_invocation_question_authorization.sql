-- dataground:up

ALTER TABLE invocation_authorization_policies
    DROP CONSTRAINT invocation_authorization_policies_contract_check,
    DROP CONSTRAINT invocation_authorization_policies_entities_check,
    ADD CONSTRAINT invocation_authorization_policies_contract_check
        CHECK (contract IN (
            'dataground.invocation-authorization-policy/v1',
            'dataground.invocation-authorization-policy/v2',
            'dataground.invocation-authorization-policy/v3',
            'dataground.invocation-authorization-policy/v4'
        )),
    ADD CONSTRAINT invocation_authorization_policies_entities_check
        CHECK (
            (contract = 'dataground.invocation-authorization-policy/v1' AND cedar_entities IS NULL)
            OR
            (contract IN (
                'dataground.invocation-authorization-policy/v2',
                'dataground.invocation-authorization-policy/v3',
                'dataground.invocation-authorization-policy/v4'
            ) AND octet_length(cedar_entities) BETWEEN 1 AND 1048576)
        );

CREATE TABLE invocation_question_authorization_decisions (
 sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 contract text NOT NULL CHECK (contract='dataground.invocation-question-authorization-decision/v1'),
 isolation_domain_id text NOT NULL CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
 operation_id text NOT NULL CHECK (operation_id ~ '^op_[0-9a-z]{20,32}$'),
 invocation_id text NOT NULL CHECK (invocation_id ~ '^inv_[0-9a-z]{20,32}$'),
 service_id text NOT NULL CHECK (service_id ~ '^svc_[0-9a-z]{20,32}$'),
 revision_id text NOT NULL CHECK (revision_id ~ '^rev_[0-9a-z]{20,32}$'),
 question_id text NOT NULL CHECK (question_id ~ '^qst_[0-9a-z]{20,32}$'),
 question_version bigint NOT NULL CHECK (question_version>0),
 phase text NOT NULL CHECK (phase IN ('entry','effect')),
 actor_id text NOT NULL CHECK (length(actor_id) BETWEEN 1 AND 256 AND actor_id !~ '[[:cntrl:]]'),
 action text NOT NULL CHECK (action='answer'),
 outcome text NOT NULL CHECK (outcome IN ('allowed','denied','unavailable')),
 policy_contract text NOT NULL CHECK (policy_contract IN ('dataground.invocation-authorization-policy/v1','dataground.invocation-authorization-policy/v2','dataground.invocation-authorization-policy/v3','dataground.invocation-authorization-policy/v4')),
 policy_set_id text NOT NULL CHECK (policy_set_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
 policy_digest text NOT NULL CHECK (policy_digest ~ '^sha256:[0-9a-f]{64}$'),
 correlation_id text NOT NULL CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
 question_count integer NOT NULL CHECK (question_count BETWEEN 1 AND 3),
 free_text_count integer NOT NULL CHECK (free_text_count BETWEEN 0 AND question_count),
 selected_option_count integer NOT NULL CHECK (selected_option_count BETWEEN question_count-free_text_count AND 4*(question_count-free_text_count)),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(recorded_at)),
 CHECK (outcome<>'allowed' OR policy_contract='dataground.invocation-authorization-policy/v4')
);
CREATE INDEX invocation_question_decisions_scope_sequence_idx
 ON invocation_question_authorization_decisions(isolation_domain_id,question_id,sequence);
CREATE TRIGGER invocation_question_decisions_append_only
 BEFORE UPDATE OR DELETE ON invocation_question_authorization_decisions
 FOR EACH ROW EXECUTE FUNCTION reject_invocation_authorization_decision_mutation();

-- dataground:down
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM invocation_question_authorization_decisions)
 OR EXISTS (SELECT 1 FROM invocation_authorization_policies WHERE contract='dataground.invocation-authorization-policy/v4') THEN
  RAISE EXCEPTION 'question authorization evidence prevents downgrade';
 END IF;
END; $$;
DROP TABLE invocation_question_authorization_decisions;
ALTER TABLE invocation_authorization_policies
    DROP CONSTRAINT invocation_authorization_policies_contract_check,
    DROP CONSTRAINT invocation_authorization_policies_entities_check,
    ADD CONSTRAINT invocation_authorization_policies_contract_check
        CHECK (contract IN (
            'dataground.invocation-authorization-policy/v1',
            'dataground.invocation-authorization-policy/v2',
            'dataground.invocation-authorization-policy/v3'
        )),
    ADD CONSTRAINT invocation_authorization_policies_entities_check
        CHECK (
            (contract = 'dataground.invocation-authorization-policy/v1' AND cedar_entities IS NULL)
            OR
            (contract IN (
                'dataground.invocation-authorization-policy/v2',
                'dataground.invocation-authorization-policy/v3'
            ) AND octet_length(cedar_entities) BETWEEN 1 AND 1048576)
        );

