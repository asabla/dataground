-- dataground:up

CREATE TABLE audit_export_workload_identity_events (
    isolation_domain_id text NOT NULL,
    workload_id text NOT NULL,
    generation bigint NOT NULL,
    authorization_contract text NOT NULL,
    operation text NOT NULL,
    grant_contract text,
    grant_sha256 text NOT NULL,
    audience text,
    client_certificate_sha256 text NOT NULL,
    authority_id text,
    issuer_trust_profile_sha256 text,
    issuer_signing_key_id text,
    issued_at timestamptz,
    not_before timestamptz,
    expires_at timestamptz,
    actor_id text NOT NULL,
    reason_digest bytea NOT NULL,
    correlation_id text NOT NULL UNIQUE,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (isolation_domain_id, workload_id, generation),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (workload_id ~ '^[a-z][a-z0-9._-]{0,127}$'),
    CHECK (generation > 0),
    CHECK (authorization_contract = 'dataground.audit-export-workload-identity-authorization/v1'),
    CHECK (operation IN ('activate', 'revoke')),
    CHECK (grant_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (client_certificate_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (
        (operation = 'activate' AND
         grant_contract IS NOT NULL AND audience IS NOT NULL AND authority_id IS NOT NULL AND
         issuer_trust_profile_sha256 IS NOT NULL AND issuer_signing_key_id IS NOT NULL AND
         issued_at IS NOT NULL AND not_before IS NOT NULL AND expires_at IS NOT NULL AND
         grant_contract = 'dataground.audit-export-workload-identity-grant/ed25519/v1' AND
         audience = 'dataground.audit-export-transport' AND
         authority_id ~ '^[a-z][a-z0-9._-]{0,127}$' AND
         issuer_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
         issuer_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
         isfinite(issued_at) AND isfinite(not_before) AND isfinite(expires_at) AND
         date_trunc('microseconds', issued_at) = issued_at AND
         date_trunc('microseconds', not_before) = not_before AND
         date_trunc('microseconds', expires_at) = expires_at AND
         issued_at <= not_before AND not_before < expires_at) OR
        (operation = 'revoke' AND grant_contract IS NULL AND audience IS NULL AND
         authority_id IS NULL AND issuer_trust_profile_sha256 IS NULL AND
         issuer_signing_key_id IS NULL AND issued_at IS NULL AND not_before IS NULL AND
         expires_at IS NULL)
    ),
    CHECK (actor_id <> '' AND length(actor_id) <= 256 AND actor_id !~ '[[:cntrl:]]'),
    CHECK (octet_length(reason_digest) = 32),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (isfinite(recorded_at))
);

CREATE FUNCTION enforce_audit_export_workload_identity_sequence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    latest_generation bigint;
    latest_operation text;
    latest_grant_sha256 text;
    latest_certificate_sha256 text;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-workload-identity' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.workload_id,
        0
    ));
    SELECT generation, operation, grant_sha256, client_certificate_sha256
    INTO latest_generation, latest_operation, latest_grant_sha256, latest_certificate_sha256
    FROM audit_export_workload_identity_events
    WHERE isolation_domain_id = NEW.isolation_domain_id AND workload_id = NEW.workload_id
    ORDER BY generation DESC
    LIMIT 1;
    IF NOT FOUND THEN
        IF NEW.generation <> 1 OR NEW.operation <> 'activate' THEN
            RAISE EXCEPTION 'audit export workload identity must begin with generation 1 activation';
        END IF;
    ELSIF latest_generation = 9223372036854775807 OR NEW.generation <> latest_generation + 1 THEN
        RAISE EXCEPTION 'audit export workload identity generations must be sequential';
    ELSIF NEW.operation = 'revoke' AND
          (latest_operation <> 'activate' OR NEW.grant_sha256 <> latest_grant_sha256 OR
           NEW.client_certificate_sha256 <> latest_certificate_sha256) THEN
        RAISE EXCEPTION 'audit export workload identity revocation must match the active grant';
    ELSIF NEW.operation = 'activate' AND latest_operation = 'activate' AND
          NEW.grant_sha256 = latest_grant_sha256 THEN
        RAISE EXCEPTION 'audit export workload identity rotation must change the active grant';
    END IF;
    IF NEW.operation = 'activate' AND
       (NEW.issued_at > clock_timestamp() + interval '5 minutes' OR
        NEW.not_before > clock_timestamp() OR NEW.expires_at <= clock_timestamp()) THEN
        RAISE EXCEPTION 'audit export workload identity grant is outside its validity interval';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_workload_identity_events_controlled_insert
BEFORE INSERT ON audit_export_workload_identity_events
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_workload_identity_sequence();

CREATE FUNCTION reject_audit_export_workload_identity_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit export workload identity events are append-only';
END;
$$;

CREATE TRIGGER audit_export_workload_identity_events_append_only
BEFORE UPDATE OR DELETE ON audit_export_workload_identity_events
FOR EACH ROW EXECUTE FUNCTION reject_audit_export_workload_identity_mutation();

CREATE FUNCTION audit_export_workload_identity_is_authorized(
    requested_domain text,
    requested_workload text,
    requested_generation bigint,
    requested_grant text,
    requested_certificate text
)
RETURNS boolean
LANGUAGE sql
VOLATILE
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM audit_export_workload_identity_events AS identity_event
        WHERE identity_event.isolation_domain_id = requested_domain
          AND identity_event.workload_id = requested_workload
          AND identity_event.generation = requested_generation
          AND identity_event.authorization_contract =
              'dataground.audit-export-workload-identity-authorization/v1'
          AND identity_event.operation = 'activate'
          AND identity_event.grant_contract =
              'dataground.audit-export-workload-identity-grant/ed25519/v1'
          AND identity_event.grant_sha256 = requested_grant
          AND identity_event.audience = 'dataground.audit-export-transport'
          AND identity_event.client_certificate_sha256 = requested_certificate
          AND identity_event.not_before <= clock_timestamp()
          AND identity_event.expires_at > clock_timestamp()
          AND identity_event.generation = (
              SELECT max(latest.generation)
              FROM audit_export_workload_identity_events AS latest
              WHERE latest.isolation_domain_id = requested_domain
                AND latest.workload_id = requested_workload
          )
    );
$$;

DROP TRIGGER audit_export_deliveries_controlled_insert ON audit_export_deliveries;
DROP FUNCTION enforce_audit_export_transport_delivery_prepare();
DROP TRIGGER audit_export_deliveries_controlled_update ON audit_export_deliveries;

ALTER TABLE audit_export_deliveries
    DROP CONSTRAINT audit_export_deliveries_verification_check,
    DROP CONSTRAINT audit_export_deliveries_contract_check,
    ADD CONSTRAINT audit_export_deliveries_contract_check
        CHECK (contract IN (
            'dataground.audit-export-delivery/v1',
            'dataground.audit-export-delivery/v2',
            'dataground.audit-export-delivery/v3',
            'dataground.audit-export-delivery/v4',
            'dataground.audit-export-delivery/v5',
            'dataground.audit-export-delivery/v6'
        )),
    ADD CONSTRAINT audit_export_deliveries_verification_check
        CHECK (
            (contract = 'dataground.audit-export-delivery/v1' AND status = 'acknowledged' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation IS NULL AND encrypted_package_digest IS NULL AND
             recipient_encryption_key_id IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v2' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v1' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation IS NULL AND
             encrypted_package_digest IS NULL AND recipient_encryption_key_id IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v3' AND status = 'prepared' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation IS NULL AND encrypted_package_digest IS NULL AND
             recipient_encryption_key_id IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v3' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v2' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation > 0 AND
             encrypted_package_digest IS NULL AND recipient_encryption_key_id IS NULL) OR
            (contract IN ('dataground.audit-export-delivery/v4', 'dataground.audit-export-delivery/v5',
                          'dataground.audit-export-delivery/v6') AND status = 'prepared' AND
             acknowledgement_contract IS NULL AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation > 0 AND octet_length(encrypted_package_digest) = 32 AND
             recipient_encryption_key_id ~ '^[a-z][a-z0-9_-]{2,63}$') OR
            (contract = 'dataground.audit-export-delivery/v4' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v3' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation > 0 AND
             octet_length(encrypted_package_digest) = 32 AND
             recipient_encryption_key_id ~ '^[a-z][a-z0-9_-]{2,63}$') OR
            (contract = 'dataground.audit-export-delivery/v5' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v4' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation > 0 AND
             octet_length(encrypted_package_digest) = 32 AND
             recipient_encryption_key_id ~ '^[a-z][a-z0-9_-]{2,63}$') OR
            (contract = 'dataground.audit-export-delivery/v6' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v5' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation > 0 AND
             octet_length(encrypted_package_digest) = 32 AND
             recipient_encryption_key_id ~ '^[a-z][a-z0-9_-]{2,63}$')
        );

CREATE FUNCTION enforce_audit_export_workload_delivery_prepare()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.contract <> 'dataground.audit-export-delivery/v6' THEN
        RAISE EXCEPTION 'new audit export deliveries require workload-authorized transport evidence';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-proof-revocation' || E'\n' || NEW.isolation_domain_id, 0
    ));
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id, 0
    ));
    IF NOT audit_export_recipient_encryption_is_authorized(
        NEW.isolation_domain_id, NEW.recipient_id, NEW.recipient_trust_generation,
        NEW.recipient_trust_profile_sha256, NEW.recipient_encryption_key_id, NULL
    ) THEN
        RAISE EXCEPTION 'audit export delivery encryption is not authorized';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_deliveries_controlled_insert
BEFORE INSERT ON audit_export_deliveries
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_workload_delivery_prepare();

DROP TRIGGER audit_export_delivery_transports_controlled_update ON audit_export_delivery_transports;
DROP FUNCTION enforce_audit_export_delivery_transport_transition();
DROP TRIGGER audit_export_delivery_transports_controlled_insert ON audit_export_delivery_transports;
DROP FUNCTION enforce_audit_export_delivery_transport_insert();

ALTER TABLE audit_export_delivery_transports
    ADD COLUMN workload_id text,
    ADD COLUMN workload_identity_grant_sha256 text,
    ADD COLUMN workload_identity_generation bigint,
    ADD COLUMN client_certificate_sha256 text,
    DROP CONSTRAINT audit_export_delivery_transports_transport_contract_check,
    ADD CONSTRAINT audit_export_delivery_transports_transport_contract_check
        CHECK (transport_contract IN (
            'dataground.audit-export-transport/s3-immutable/v1',
            'dataground.audit-export-transport/s3-immutable-mtls/v2',
            'dataground.audit-export-transport/s3-immutable-mtls-workload/v3'
        )),
    ADD CONSTRAINT audit_export_delivery_transports_workload_identity_check
        CHECK (
            (transport_contract IN (
                'dataground.audit-export-transport/s3-immutable/v1',
                'dataground.audit-export-transport/s3-immutable-mtls/v2'
             ) AND workload_id IS NULL AND workload_identity_grant_sha256 IS NULL AND
             workload_identity_generation IS NULL AND client_certificate_sha256 IS NULL) OR
            (transport_contract = 'dataground.audit-export-transport/s3-immutable-mtls-workload/v3' AND
             workload_id IS NOT NULL AND workload_identity_grant_sha256 IS NOT NULL AND
             workload_identity_generation IS NOT NULL AND client_certificate_sha256 IS NOT NULL AND
             workload_id ~ '^[a-z][a-z0-9._-]{0,127}$' AND
             workload_identity_grant_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             workload_identity_generation > 0 AND
             client_certificate_sha256 ~ '^sha256:[0-9a-f]{64}$')
        );

CREATE FUNCTION enforce_audit_export_delivery_transport_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    delivery audit_export_deliveries%ROWTYPE;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(NEW.delivery_id, 0));
    SELECT * INTO delivery FROM audit_export_deliveries
    WHERE delivery_id = NEW.delivery_id FOR UPDATE;
    IF FOUND THEN
        PERFORM pg_advisory_xact_lock(hashtextextended(
            'audit-export-recipient-proof-revocation' || E'\n' || delivery.isolation_domain_id, 0
        ));
        PERFORM pg_advisory_xact_lock(hashtextextended(
            'audit-export-recipient-trust' || E'\n' || delivery.isolation_domain_id || E'\n' ||
            delivery.recipient_id, 0
        ));
        IF delivery.contract = 'dataground.audit-export-delivery/v6' THEN
            PERFORM pg_advisory_xact_lock(hashtextextended(
                'audit-export-workload-identity' || E'\n' || delivery.isolation_domain_id || E'\n' ||
                NEW.workload_id, 0
            ));
        END IF;
    END IF;
    IF NOT FOUND OR delivery.status <> 'prepared' OR
       delivery.isolation_domain_id <> NEW.isolation_domain_id OR
       delivery.destination_digest <> NEW.destination_digest OR
       delivery.encrypted_package_digest <> NEW.encrypted_package_digest OR
       NOT EXISTS (
           SELECT 1 FROM audit_export_delivery_operations AS operation
           WHERE operation.delivery_id = NEW.delivery_id
             AND operation.isolation_domain_id = NEW.isolation_domain_id
             AND operation.operation = 'transport'
             AND operation.evidence_digest = NEW.encrypted_package_digest
       ) OR NOT audit_export_recipient_encryption_is_authorized(
           delivery.isolation_domain_id, delivery.recipient_id, delivery.recipient_trust_generation,
           delivery.recipient_trust_profile_sha256, delivery.recipient_encryption_key_id, NULL
       ) OR
       (delivery.contract = 'dataground.audit-export-delivery/v5' AND
        NEW.transport_contract NOT IN (
            'dataground.audit-export-transport/s3-immutable/v1',
            'dataground.audit-export-transport/s3-immutable-mtls/v2'
        )) OR
       (delivery.contract = 'dataground.audit-export-delivery/v6' AND
        (NEW.transport_contract <> 'dataground.audit-export-transport/s3-immutable-mtls-workload/v3' OR
         NOT audit_export_workload_identity_is_authorized(
             delivery.isolation_domain_id, NEW.workload_id, NEW.workload_identity_generation,
             NEW.workload_identity_grant_sha256, NEW.client_certificate_sha256
         ))) OR delivery.contract NOT IN (
            'dataground.audit-export-delivery/v5', 'dataground.audit-export-delivery/v6'
       ) THEN
        RAISE EXCEPTION 'audit export delivery transport reservation is invalid';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_delivery_transports_controlled_insert
BEFORE INSERT ON audit_export_delivery_transports
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transport_insert();

CREATE FUNCTION enforce_audit_export_delivery_transport_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.state <> 'reserved' OR NEW.state <> 'completed' OR
       OLD.delivery_id <> NEW.delivery_id OR OLD.isolation_domain_id <> NEW.isolation_domain_id OR
       OLD.transport_contract <> NEW.transport_contract OR
       OLD.destination_digest <> NEW.destination_digest OR
       OLD.encrypted_package_digest <> NEW.encrypted_package_digest OR
       OLD.workload_id IS DISTINCT FROM NEW.workload_id OR
       OLD.workload_identity_grant_sha256 IS DISTINCT FROM NEW.workload_identity_grant_sha256 OR
       OLD.workload_identity_generation IS DISTINCT FROM NEW.workload_identity_generation OR
       OLD.client_certificate_sha256 IS DISTINCT FROM NEW.client_certificate_sha256 OR
       OLD.reserved_at <> NEW.reserved_at OR NEW.completed_at IS NULL THEN
        RAISE EXCEPTION 'audit export delivery transport transition is invalid';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_delivery_transports_controlled_update
BEFORE UPDATE ON audit_export_delivery_transports
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transport_transition();

CREATE OR REPLACE FUNCTION enforce_audit_export_delivery_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-proof-revocation' || E'\n' || NEW.isolation_domain_id, 0
    ));
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id, 0
    ));
    IF OLD.status <> 'prepared' OR NEW.status <> 'acknowledged' OR
       OLD.delivery_id <> NEW.delivery_id OR OLD.isolation_domain_id <> NEW.isolation_domain_id OR
       OLD.contract <> NEW.contract OR OLD.export_kind <> NEW.export_kind OR OLD.export_id <> NEW.export_id OR
       OLD.envelope_digest <> NEW.envelope_digest OR OLD.export_sha256 <> NEW.export_sha256 OR
       OLD.trust_profile_sha256 <> NEW.trust_profile_sha256 OR OLD.signing_key_id <> NEW.signing_key_id OR
       OLD.recipient_id <> NEW.recipient_id OR OLD.destination_digest <> NEW.destination_digest OR
       OLD.prepared_at <> NEW.prepared_at OR
       OLD.encrypted_package_digest IS DISTINCT FROM NEW.encrypted_package_digest OR
       OLD.recipient_encryption_key_id IS DISTINCT FROM NEW.recipient_encryption_key_id OR
       NEW.acknowledgement_digest IS NULL OR NEW.acknowledged_at IS NULL OR
       NOT EXISTS (
           SELECT 1 FROM audit_export_delivery_operations AS operation
           WHERE operation.delivery_id = NEW.delivery_id
             AND operation.isolation_domain_id = NEW.isolation_domain_id
             AND operation.operation = 'acknowledge'
             AND operation.evidence_digest = NEW.acknowledgement_digest
       ) THEN
        RAISE EXCEPTION 'audit export delivery transition is invalid';
    END IF;
    IF NEW.contract IN (
        'dataground.audit-export-delivery/v4',
        'dataground.audit-export-delivery/v5',
        'dataground.audit-export-delivery/v6'
    ) THEN
        IF OLD.recipient_trust_profile_sha256 IS DISTINCT FROM NEW.recipient_trust_profile_sha256 OR
           OLD.recipient_trust_generation IS DISTINCT FROM NEW.recipient_trust_generation OR
           (NEW.contract = 'dataground.audit-export-delivery/v4' AND
            NEW.acknowledgement_contract <> 'dataground.audit-export-delivery-receipt/ed25519/v3') OR
           (NEW.contract = 'dataground.audit-export-delivery/v5' AND
            NEW.acknowledgement_contract <> 'dataground.audit-export-delivery-receipt/ed25519/v4') OR
           (NEW.contract = 'dataground.audit-export-delivery/v6' AND
            NEW.acknowledgement_contract <> 'dataground.audit-export-delivery-receipt/ed25519/v5') OR
           (NEW.contract IN ('dataground.audit-export-delivery/v5', 'dataground.audit-export-delivery/v6') AND
            NOT EXISTS (
                SELECT 1 FROM audit_export_delivery_transports AS transport
                WHERE transport.delivery_id = NEW.delivery_id
                  AND transport.isolation_domain_id = NEW.isolation_domain_id
                  AND transport.state = 'completed'
                  AND transport.destination_digest = NEW.destination_digest
                  AND transport.encrypted_package_digest = NEW.encrypted_package_digest
                  AND (
                      (NEW.contract = 'dataground.audit-export-delivery/v5' AND
                       transport.transport_contract IN (
                           'dataground.audit-export-transport/s3-immutable/v1',
                           'dataground.audit-export-transport/s3-immutable-mtls/v2'
                       )) OR
                      (NEW.contract = 'dataground.audit-export-delivery/v6' AND
                       transport.transport_contract =
                           'dataground.audit-export-transport/s3-immutable-mtls-workload/v3')
                  )
            )) OR NOT audit_export_recipient_encryption_is_authorized(
                NEW.isolation_domain_id, NEW.recipient_id, NEW.recipient_trust_generation,
                NEW.recipient_trust_profile_sha256, NEW.recipient_encryption_key_id,
                NEW.recipient_signing_key_id
           ) THEN
            RAISE EXCEPTION 'encrypted audit export acknowledgement is not authorized';
        END IF;
    ELSIF NEW.contract = 'dataground.audit-export-delivery/v3' THEN
        IF NEW.acknowledgement_contract <>
               'dataground.audit-export-delivery-receipt/ed25519/v2' OR
           NEW.recipient_trust_generation IS NULL OR
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
                 AND trust_event.authorization_contract =
                     'dataground.audit-export-recipient-trust-authorization/v2'
                 AND trust_event.operation = 'activate'
                 AND trust_event.trust_profile_sha256 = NEW.recipient_trust_profile_sha256
                 AND isfinite(trust_event.identity_proof_expires_at)
                 AND trust_event.identity_proof_expires_at > clock_timestamp()
                 AND trust_key.key_id = NEW.recipient_signing_key_id
                 AND trust_event.generation = (
                     SELECT max(latest.generation)
                     FROM audit_export_recipient_trust_events AS latest
                     WHERE latest.isolation_domain_id = NEW.isolation_domain_id
                       AND latest.recipient_id = NEW.recipient_id
                 )
                 AND NOT EXISTS (
                     SELECT 1 FROM audit_export_recipient_proof_revocations AS revocation
                     WHERE revocation.isolation_domain_id = trust_event.isolation_domain_id
                       AND revocation.proofing_authority_id = trust_event.proofing_authority_id
                       AND revocation.proofing_trust_profile_sha256 = trust_event.proofing_trust_profile_sha256
                       AND revocation.effective_at <= clock_timestamp()
                       AND (revocation.scope = 'profile' OR
                            (revocation.scope = 'key' AND
                             revocation.proofing_signing_key_id = trust_event.proofing_signing_key_id))
                 )
           ) THEN
            RAISE EXCEPTION 'legacy audit export acknowledgement is not authorized';
        END IF;
    ELSE
        RAISE EXCEPTION 'audit export delivery contract cannot transition';
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
    IF EXISTS (SELECT 1 FROM audit_export_workload_identity_events) OR EXISTS (
        SELECT 1 FROM audit_export_deliveries
        WHERE contract = 'dataground.audit-export-delivery/v6'
    ) OR EXISTS (
        SELECT 1 FROM audit_export_delivery_transports
        WHERE transport_contract = 'dataground.audit-export-transport/s3-immutable-mtls-workload/v3'
    ) THEN
        RAISE EXCEPTION 'schema 30 contains workload identity evidence and cannot be downgraded safely';
    END IF;
END;
$$;

DROP TRIGGER audit_export_deliveries_controlled_update ON audit_export_deliveries;
DROP TRIGGER audit_export_delivery_transports_controlled_update ON audit_export_delivery_transports;
DROP FUNCTION enforce_audit_export_delivery_transport_transition();
DROP TRIGGER audit_export_delivery_transports_controlled_insert ON audit_export_delivery_transports;
DROP FUNCTION enforce_audit_export_delivery_transport_insert();
DROP TRIGGER audit_export_deliveries_controlled_insert ON audit_export_deliveries;
DROP FUNCTION enforce_audit_export_workload_delivery_prepare();

ALTER TABLE audit_export_delivery_transports
    DROP CONSTRAINT audit_export_delivery_transports_workload_identity_check,
    DROP COLUMN client_certificate_sha256,
    DROP COLUMN workload_identity_generation,
    DROP COLUMN workload_identity_grant_sha256,
    DROP COLUMN workload_id,
    DROP CONSTRAINT audit_export_delivery_transports_transport_contract_check,
    ADD CONSTRAINT audit_export_delivery_transports_transport_contract_check
        CHECK (transport_contract IN (
            'dataground.audit-export-transport/s3-immutable/v1',
            'dataground.audit-export-transport/s3-immutable-mtls/v2'
        ));

DROP FUNCTION audit_export_workload_identity_is_authorized(text, text, bigint, text, text);
DROP TRIGGER audit_export_workload_identity_events_append_only ON audit_export_workload_identity_events;
DROP FUNCTION reject_audit_export_workload_identity_mutation();
DROP TRIGGER audit_export_workload_identity_events_controlled_insert ON audit_export_workload_identity_events;
DROP FUNCTION enforce_audit_export_workload_identity_sequence();
DROP TABLE audit_export_workload_identity_events;

ALTER TABLE audit_export_deliveries
    DROP CONSTRAINT audit_export_deliveries_verification_check,
    DROP CONSTRAINT audit_export_deliveries_contract_check,
    ADD CONSTRAINT audit_export_deliveries_contract_check
        CHECK (contract IN (
            'dataground.audit-export-delivery/v1',
            'dataground.audit-export-delivery/v2',
            'dataground.audit-export-delivery/v3',
            'dataground.audit-export-delivery/v4',
            'dataground.audit-export-delivery/v5'
        )),
    ADD CONSTRAINT audit_export_deliveries_verification_check
        CHECK (
            (contract = 'dataground.audit-export-delivery/v1' AND status = 'acknowledged' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation IS NULL AND encrypted_package_digest IS NULL AND
             recipient_encryption_key_id IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v2' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v1' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation IS NULL AND
             encrypted_package_digest IS NULL AND recipient_encryption_key_id IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v3' AND status = 'prepared' AND
             acknowledgement_contract IS NULL AND recipient_trust_profile_sha256 IS NULL AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation IS NULL AND encrypted_package_digest IS NULL AND
             recipient_encryption_key_id IS NULL) OR
            (contract = 'dataground.audit-export-delivery/v3' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v2' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation > 0 AND
             encrypted_package_digest IS NULL AND recipient_encryption_key_id IS NULL) OR
            (contract IN ('dataground.audit-export-delivery/v4', 'dataground.audit-export-delivery/v5') AND
             status = 'prepared' AND acknowledgement_contract IS NULL AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id IS NULL AND recipient_accepted_at IS NULL AND
             recipient_trust_generation > 0 AND octet_length(encrypted_package_digest) = 32 AND
             recipient_encryption_key_id ~ '^[a-z][a-z0-9_-]{2,63}$') OR
            (contract = 'dataground.audit-export-delivery/v4' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v3' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation > 0 AND
             octet_length(encrypted_package_digest) = 32 AND
             recipient_encryption_key_id ~ '^[a-z][a-z0-9_-]{2,63}$') OR
            (contract = 'dataground.audit-export-delivery/v5' AND status = 'acknowledged' AND
             acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v4' AND
             recipient_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$' AND
             recipient_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$' AND
             recipient_accepted_at IS NOT NULL AND recipient_trust_generation > 0 AND
             octet_length(encrypted_package_digest) = 32 AND
             recipient_encryption_key_id ~ '^[a-z][a-z0-9_-]{2,63}$')
        );

CREATE FUNCTION enforce_audit_export_transport_delivery_prepare()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.contract <> 'dataground.audit-export-delivery/v5' THEN
        RAISE EXCEPTION 'new audit export deliveries require durable transport evidence';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-proof-revocation' || E'\n' || NEW.isolation_domain_id, 0
    ));
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id, 0
    ));
    IF NOT audit_export_recipient_encryption_is_authorized(
        NEW.isolation_domain_id, NEW.recipient_id, NEW.recipient_trust_generation,
        NEW.recipient_trust_profile_sha256, NEW.recipient_encryption_key_id, NULL
    ) THEN
        RAISE EXCEPTION 'audit export delivery encryption is not authorized';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_deliveries_controlled_insert
BEFORE INSERT ON audit_export_deliveries
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_transport_delivery_prepare();

CREATE FUNCTION enforce_audit_export_delivery_transport_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    delivery audit_export_deliveries%ROWTYPE;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(NEW.delivery_id, 0));
    SELECT * INTO delivery FROM audit_export_deliveries
    WHERE delivery_id = NEW.delivery_id FOR UPDATE;
    IF FOUND THEN
        PERFORM pg_advisory_xact_lock(hashtextextended(
            'audit-export-recipient-proof-revocation' || E'\n' || delivery.isolation_domain_id, 0
        ));
        PERFORM pg_advisory_xact_lock(hashtextextended(
            'audit-export-recipient-trust' || E'\n' || delivery.isolation_domain_id || E'\n' ||
            delivery.recipient_id, 0
        ));
    END IF;
    IF NOT FOUND OR delivery.contract <> 'dataground.audit-export-delivery/v5' OR
       delivery.status <> 'prepared' OR delivery.isolation_domain_id <> NEW.isolation_domain_id OR
       delivery.destination_digest <> NEW.destination_digest OR
       delivery.encrypted_package_digest <> NEW.encrypted_package_digest OR
       NOT EXISTS (
           SELECT 1 FROM audit_export_delivery_operations AS operation
           WHERE operation.delivery_id = NEW.delivery_id
             AND operation.isolation_domain_id = NEW.isolation_domain_id
             AND operation.operation = 'transport'
             AND operation.evidence_digest = NEW.encrypted_package_digest
       ) OR NOT audit_export_recipient_encryption_is_authorized(
           delivery.isolation_domain_id, delivery.recipient_id, delivery.recipient_trust_generation,
           delivery.recipient_trust_profile_sha256, delivery.recipient_encryption_key_id, NULL
       ) THEN
        RAISE EXCEPTION 'audit export delivery transport reservation is invalid';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_delivery_transports_controlled_insert
BEFORE INSERT ON audit_export_delivery_transports
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transport_insert();

CREATE FUNCTION enforce_audit_export_delivery_transport_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.state <> 'reserved' OR NEW.state <> 'completed' OR
       OLD.delivery_id <> NEW.delivery_id OR OLD.isolation_domain_id <> NEW.isolation_domain_id OR
       OLD.transport_contract <> NEW.transport_contract OR
       OLD.destination_digest <> NEW.destination_digest OR
       OLD.encrypted_package_digest <> NEW.encrypted_package_digest OR
       OLD.reserved_at <> NEW.reserved_at OR NEW.completed_at IS NULL THEN
        RAISE EXCEPTION 'audit export delivery transport transition is invalid';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_delivery_transports_controlled_update
BEFORE UPDATE ON audit_export_delivery_transports
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transport_transition();

CREATE OR REPLACE FUNCTION enforce_audit_export_delivery_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-proof-revocation' || E'\n' || NEW.isolation_domain_id, 0
    ));
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-recipient-trust' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.recipient_id, 0
    ));
    IF OLD.status <> 'prepared' OR NEW.status <> 'acknowledged' OR
       OLD.delivery_id <> NEW.delivery_id OR OLD.isolation_domain_id <> NEW.isolation_domain_id OR
       OLD.contract <> NEW.contract OR OLD.export_kind <> NEW.export_kind OR OLD.export_id <> NEW.export_id OR
       OLD.envelope_digest <> NEW.envelope_digest OR OLD.export_sha256 <> NEW.export_sha256 OR
       OLD.trust_profile_sha256 <> NEW.trust_profile_sha256 OR OLD.signing_key_id <> NEW.signing_key_id OR
       OLD.recipient_id <> NEW.recipient_id OR OLD.destination_digest <> NEW.destination_digest OR
       OLD.prepared_at <> NEW.prepared_at OR
       OLD.encrypted_package_digest IS DISTINCT FROM NEW.encrypted_package_digest OR
       OLD.recipient_encryption_key_id IS DISTINCT FROM NEW.recipient_encryption_key_id OR
       NEW.acknowledgement_digest IS NULL OR NEW.acknowledged_at IS NULL OR
       NOT EXISTS (
           SELECT 1 FROM audit_export_delivery_operations AS operation
           WHERE operation.delivery_id = NEW.delivery_id
             AND operation.isolation_domain_id = NEW.isolation_domain_id
             AND operation.operation = 'acknowledge'
             AND operation.evidence_digest = NEW.acknowledgement_digest
       ) THEN
        RAISE EXCEPTION 'audit export delivery transition is invalid';
    END IF;
    IF NEW.contract IN ('dataground.audit-export-delivery/v4', 'dataground.audit-export-delivery/v5') THEN
        IF OLD.recipient_trust_profile_sha256 IS DISTINCT FROM NEW.recipient_trust_profile_sha256 OR
           OLD.recipient_trust_generation IS DISTINCT FROM NEW.recipient_trust_generation OR
           (NEW.contract = 'dataground.audit-export-delivery/v4' AND
            NEW.acknowledgement_contract <> 'dataground.audit-export-delivery-receipt/ed25519/v3') OR
           (NEW.contract = 'dataground.audit-export-delivery/v5' AND
            NEW.acknowledgement_contract <> 'dataground.audit-export-delivery-receipt/ed25519/v4') OR
           (NEW.contract = 'dataground.audit-export-delivery/v5' AND NOT EXISTS (
               SELECT 1 FROM audit_export_delivery_transports AS transport
               WHERE transport.delivery_id = NEW.delivery_id
                 AND transport.isolation_domain_id = NEW.isolation_domain_id
                 AND transport.state = 'completed'
                 AND transport.destination_digest = NEW.destination_digest
                 AND transport.encrypted_package_digest = NEW.encrypted_package_digest
           )) OR NOT audit_export_recipient_encryption_is_authorized(
               NEW.isolation_domain_id, NEW.recipient_id, NEW.recipient_trust_generation,
               NEW.recipient_trust_profile_sha256, NEW.recipient_encryption_key_id,
               NEW.recipient_signing_key_id
           ) THEN
            RAISE EXCEPTION 'encrypted audit export acknowledgement is not authorized';
        END IF;
    ELSIF NEW.contract = 'dataground.audit-export-delivery/v3' THEN
        IF NEW.acknowledgement_contract <>
               'dataground.audit-export-delivery-receipt/ed25519/v2' OR
           NEW.recipient_trust_generation IS NULL OR
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
                 AND trust_event.authorization_contract =
                     'dataground.audit-export-recipient-trust-authorization/v2'
                 AND trust_event.operation = 'activate'
                 AND trust_event.trust_profile_sha256 = NEW.recipient_trust_profile_sha256
                 AND isfinite(trust_event.identity_proof_expires_at)
                 AND trust_event.identity_proof_expires_at > clock_timestamp()
                 AND trust_key.key_id = NEW.recipient_signing_key_id
                 AND trust_event.generation = (
                     SELECT max(latest.generation)
                     FROM audit_export_recipient_trust_events AS latest
                     WHERE latest.isolation_domain_id = NEW.isolation_domain_id
                       AND latest.recipient_id = NEW.recipient_id
                 )
                 AND NOT EXISTS (
                     SELECT 1 FROM audit_export_recipient_proof_revocations AS revocation
                     WHERE revocation.isolation_domain_id = trust_event.isolation_domain_id
                       AND revocation.proofing_authority_id = trust_event.proofing_authority_id
                       AND revocation.proofing_trust_profile_sha256 = trust_event.proofing_trust_profile_sha256
                       AND revocation.effective_at <= clock_timestamp()
                       AND (revocation.scope = 'profile' OR
                            (revocation.scope = 'key' AND
                             revocation.proofing_signing_key_id = trust_event.proofing_signing_key_id))
                 )
           ) THEN
            RAISE EXCEPTION 'legacy audit export acknowledgement is not authorized';
        END IF;
    ELSE
        RAISE EXCEPTION 'audit export delivery contract cannot transition';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_deliveries_controlled_update
BEFORE UPDATE ON audit_export_deliveries
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transition();
