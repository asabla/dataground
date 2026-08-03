-- dataground:up

DROP TRIGGER audit_export_deliveries_controlled_update ON audit_export_deliveries;

ALTER TABLE audit_export_deliveries
    DROP CONSTRAINT audit_export_deliveries_verification_check,
    DROP CONSTRAINT audit_export_deliveries_contract_check,
    ADD COLUMN recipient_trust_generation bigint;

UPDATE audit_export_deliveries
SET contract = 'dataground.audit-export-delivery/v3'
WHERE status = 'prepared';

ALTER TABLE audit_export_deliveries
    ADD CONSTRAINT audit_export_deliveries_contract_check
        CHECK (contract IN (
            'dataground.audit-export-delivery/v1',
            'dataground.audit-export-delivery/v2',
            'dataground.audit-export-delivery/v3'
        )),
    ADD CONSTRAINT audit_export_deliveries_verification_check
        CHECK (
            (contract = 'dataground.audit-export-delivery/v1' AND status = 'acknowledged' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v2' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v1' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v3' AND status = 'prepared' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v3' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v2' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation > 0)
        );

CREATE TABLE audit_export_recipient_trust_events (
    isolation_domain_id text NOT NULL,
    recipient_id text NOT NULL,
    generation bigint NOT NULL,
    operation text NOT NULL,
    trust_contract text NOT NULL,
    trust_profile_sha256 text NOT NULL,
    actor_id text NOT NULL,
    reason_digest bytea NOT NULL,
    correlation_id text NOT NULL UNIQUE,
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (isolation_domain_id, recipient_id, generation),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (recipient_id ~ '^[a-z][a-z0-9._-]{0,127}$'),
    CHECK (generation > 0),
    CHECK (operation IN ('activate', 'revoke')),
    CHECK (trust_contract = 'dataground.audit-export-recipient-trust/ed25519/v1'),
    CHECK (trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (actor_id <> '' AND length(actor_id) <= 256 AND actor_id !~ '[[:cntrl:]]'),
    CHECK (octet_length(reason_digest) = 32),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$')
);

CREATE TABLE audit_export_recipient_trust_keys (
    isolation_domain_id text NOT NULL,
    recipient_id text NOT NULL,
    generation bigint NOT NULL,
    key_id text NOT NULL,
    PRIMARY KEY (isolation_domain_id, recipient_id, generation, key_id),
    FOREIGN KEY (isolation_domain_id, recipient_id, generation)
        REFERENCES audit_export_recipient_trust_events (isolation_domain_id, recipient_id, generation),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (recipient_id ~ '^[a-z][a-z0-9._-]{0,127}$'),
    CHECK (generation > 0),
    CHECK (key_id ~ '^[a-z][a-z0-9_-]{2,63}$')
);

CREATE FUNCTION enforce_audit_export_recipient_trust_sequence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    latest_generation bigint;
    latest_operation text;
    latest_profile_sha256 text;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id,
        0
    ));
    SELECT generation, operation, trust_profile_sha256
    INTO latest_generation, latest_operation, latest_profile_sha256
    FROM audit_export_recipient_trust_events
    WHERE isolation_domain_id = NEW.isolation_domain_id
      AND recipient_id = NEW.recipient_id
    ORDER BY generation DESC
    LIMIT 1;

    IF NOT FOUND THEN
        IF NEW.generation <> 1 OR NEW.operation <> 'activate' THEN
            RAISE EXCEPTION 'audit export recipient trust must begin with generation 1 activation';
        END IF;
    ELSIF latest_generation = 9223372036854775807 OR NEW.generation <> latest_generation + 1 THEN
        RAISE EXCEPTION 'audit export recipient trust generations must be sequential';
    ELSIF NEW.operation = 'revoke' AND
          (latest_operation <> 'activate' OR NEW.trust_profile_sha256 <> latest_profile_sha256) THEN
        RAISE EXCEPTION 'audit export recipient trust revocation must match the active profile';
    ELSIF NEW.operation = 'activate' AND latest_operation = 'activate' AND
          NEW.trust_profile_sha256 = latest_profile_sha256 THEN
        RAISE EXCEPTION 'audit export recipient trust activation must change the active profile';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_recipient_trust_events_sequence
BEFORE INSERT ON audit_export_recipient_trust_events
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_recipient_trust_sequence();

CREATE FUNCTION enforce_audit_export_recipient_trust_key_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM audit_export_recipient_trust_events AS trust_event
        WHERE trust_event.isolation_domain_id = NEW.isolation_domain_id
          AND trust_event.recipient_id = NEW.recipient_id
          AND trust_event.generation = NEW.generation
          AND trust_event.operation = 'activate'
    ) THEN
        RAISE EXCEPTION 'audit export recipient trust keys require an activation event';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_recipient_trust_keys_binding
BEFORE INSERT ON audit_export_recipient_trust_keys
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_recipient_trust_key_binding();

CREATE FUNCTION enforce_audit_export_recipient_trust_key_count()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    key_count bigint;
BEGIN
    SELECT count(*)
    INTO key_count
    FROM audit_export_recipient_trust_keys
    WHERE isolation_domain_id = NEW.isolation_domain_id
      AND recipient_id = NEW.recipient_id
      AND generation = NEW.generation;
    IF (NEW.operation = 'activate' AND (key_count < 1 OR key_count > 8)) OR
       (NEW.operation = 'revoke' AND key_count <> 0) THEN
        RAISE EXCEPTION 'audit export recipient trust event has an invalid key set';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER audit_export_recipient_trust_events_key_count
AFTER INSERT ON audit_export_recipient_trust_events
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_recipient_trust_key_count();

CREATE FUNCTION reject_audit_export_recipient_trust_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit export recipient trust events are append-only';
END;
$$;

CREATE TRIGGER audit_export_recipient_trust_events_append_only
BEFORE UPDATE OR DELETE ON audit_export_recipient_trust_events
FOR EACH ROW EXECUTE FUNCTION reject_audit_export_recipient_trust_mutation();

CREATE TRIGGER audit_export_recipient_trust_keys_append_only
BEFORE UPDATE OR DELETE ON audit_export_recipient_trust_keys
FOR EACH ROW EXECUTE FUNCTION reject_audit_export_recipient_trust_mutation();

CREATE OR REPLACE FUNCTION enforce_audit_export_delivery_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id,
        0
    ));
    IF OLD.status <> 'prepared' OR NEW.status <> 'acknowledged' OR
       OLD.delivery_id <> NEW.delivery_id OR
       OLD.isolation_domain_id <> NEW.isolation_domain_id OR
       OLD.contract <> NEW.contract OR
       OLD.export_kind <> NEW.export_kind OR
       OLD.export_id <> NEW.export_id OR
       OLD.envelope_digest <> NEW.envelope_digest OR
       OLD.export_sha256 <> NEW.export_sha256 OR
       OLD.trust_profile_sha256 <> NEW.trust_profile_sha256 OR
       OLD.signing_key_id <> NEW.signing_key_id OR
       OLD.recipient_id <> NEW.recipient_id OR
       OLD.destination_digest <> NEW.destination_digest OR
       OLD.prepared_at <> NEW.prepared_at OR
       NEW.contract <> 'dataground.audit-export-delivery/v3' OR
       NEW.acknowledgement_digest IS NULL OR
       NEW.acknowledged_at IS NULL OR
       NEW.recipient_trust_generation IS NULL OR
       NOT EXISTS (
           SELECT 1
           FROM audit_export_delivery_operations AS operation
           WHERE operation.delivery_id = NEW.delivery_id
             AND operation.isolation_domain_id = NEW.isolation_domain_id
             AND operation.operation = 'acknowledge'
             AND operation.evidence_digest = NEW.acknowledgement_digest
       ) OR
       NOT EXISTS (
           SELECT 1
           FROM audit_export_recipient_trust_events AS trust_event
           JOIN audit_export_recipient_trust_keys AS trust_key
             ON trust_key.isolation_domain_id = trust_event.isolation_domain_id
            AND trust_key.recipient_id = trust_event.recipient_id
            AND trust_key.generation = trust_event.generation
           WHERE trust_event.isolation_domain_id = NEW.isolation_domain_id
             AND trust_event.recipient_id = NEW.recipient_id
             AND trust_event.generation = NEW.recipient_trust_generation
             AND trust_event.operation = 'activate'
             AND trust_event.trust_profile_sha256 = NEW.recipient_trust_profile_sha256
             AND trust_key.key_id = NEW.recipient_signing_key_id
             AND trust_event.generation = (
                 SELECT max(latest.generation)
                 FROM audit_export_recipient_trust_events AS latest
                 WHERE latest.isolation_domain_id = NEW.isolation_domain_id
                   AND latest.recipient_id = NEW.recipient_id
             )
       ) THEN
        RAISE EXCEPTION 'audit export deliveries permit only authorized operation-bound acknowledgements';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_deliveries_controlled_update
BEFORE UPDATE ON audit_export_deliveries
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transition();

-- dataground:down

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM audit_export_recipient_trust_events) OR
       EXISTS (
           SELECT 1
           FROM audit_export_deliveries
           WHERE contract = 'dataground.audit-export-delivery/v3' AND status = 'acknowledged'
       ) THEN
        RAISE EXCEPTION 'schema 24 contains recipient trust evidence and cannot be downgraded safely';
    END IF;
END;
$$;

DROP TRIGGER audit_export_deliveries_controlled_update ON audit_export_deliveries;
DROP TRIGGER audit_export_recipient_trust_keys_append_only ON audit_export_recipient_trust_keys;
DROP TRIGGER audit_export_recipient_trust_events_append_only ON audit_export_recipient_trust_events;
DROP FUNCTION reject_audit_export_recipient_trust_mutation();
DROP TRIGGER audit_export_recipient_trust_events_key_count ON audit_export_recipient_trust_events;
DROP FUNCTION enforce_audit_export_recipient_trust_key_count();
DROP TRIGGER audit_export_recipient_trust_keys_binding ON audit_export_recipient_trust_keys;
DROP FUNCTION enforce_audit_export_recipient_trust_key_binding();
DROP TRIGGER audit_export_recipient_trust_events_sequence ON audit_export_recipient_trust_events;
DROP FUNCTION enforce_audit_export_recipient_trust_sequence();
DROP TABLE audit_export_recipient_trust_keys;
DROP TABLE audit_export_recipient_trust_events;

ALTER TABLE audit_export_deliveries
    DROP CONSTRAINT audit_export_deliveries_verification_check,
    DROP CONSTRAINT audit_export_deliveries_contract_check;

UPDATE audit_export_deliveries
SET contract = 'dataground.audit-export-delivery/v2'
WHERE status = 'prepared';

ALTER TABLE audit_export_deliveries
    DROP COLUMN recipient_trust_generation,
    ADD CONSTRAINT audit_export_deliveries_contract_check
        CHECK (contract IN ('dataground.audit-export-delivery/v1', 'dataground.audit-export-delivery/v2')),
    ADD CONSTRAINT audit_export_deliveries_verification_check
        CHECK (
            (contract = 'dataground.audit-export-delivery/v1' AND status = 'acknowledged' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v2' AND status = 'prepared' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v2' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v1' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL)
        );

CREATE OR REPLACE FUNCTION enforce_audit_export_delivery_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.status <> 'prepared' OR NEW.status <> 'acknowledged' OR
       OLD.delivery_id <> NEW.delivery_id OR
       OLD.isolation_domain_id <> NEW.isolation_domain_id OR
       OLD.contract <> NEW.contract OR
       OLD.export_kind <> NEW.export_kind OR
       OLD.export_id <> NEW.export_id OR
       OLD.envelope_digest <> NEW.envelope_digest OR
       OLD.export_sha256 <> NEW.export_sha256 OR
       OLD.trust_profile_sha256 <> NEW.trust_profile_sha256 OR
       OLD.signing_key_id <> NEW.signing_key_id OR
       OLD.recipient_id <> NEW.recipient_id OR
       OLD.destination_digest <> NEW.destination_digest OR
       OLD.prepared_at <> NEW.prepared_at OR
       NEW.acknowledgement_digest IS NULL OR
       NEW.acknowledged_at IS NULL OR
       NOT EXISTS (
           SELECT 1
           FROM audit_export_delivery_operations AS operation
           WHERE operation.delivery_id = NEW.delivery_id
             AND operation.isolation_domain_id = NEW.isolation_domain_id
             AND operation.operation = 'acknowledge'
             AND operation.evidence_digest = NEW.acknowledgement_digest
       ) THEN
        RAISE EXCEPTION 'audit export deliveries permit only operation-bound prepared-to-acknowledged transitions';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_deliveries_controlled_update
BEFORE UPDATE ON audit_export_deliveries
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transition();
