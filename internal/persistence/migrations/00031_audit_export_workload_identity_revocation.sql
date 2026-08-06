-- dataground:up

CREATE TABLE audit_export_workload_identity_revocations (
    record_contract text NOT NULL,
    revocation_contract text NOT NULL,
    revocation_sha256 text PRIMARY KEY,
    isolation_domain_id text NOT NULL,
    scope text NOT NULL,
    workload_identity_authority_id text NOT NULL,
    workload_identity_trust_profile_sha256 text NOT NULL,
    workload_identity_signing_key_id text,
    external_reason_sha256 text NOT NULL,
    revocation_authority_id text NOT NULL,
    revocation_trust_profile_sha256 text NOT NULL,
    revocation_signing_key_id text NOT NULL,
    issued_at timestamptz NOT NULL,
    effective_at timestamptz NOT NULL,
    actor_id text NOT NULL,
    reason_digest bytea NOT NULL,
    correlation_id text NOT NULL UNIQUE,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (record_contract = 'dataground.audit-export-workload-identity-revocation-record/v1'),
    CHECK (revocation_contract = 'dataground.audit-export-workload-identity-revocation/ed25519/v1'),
    CHECK (revocation_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (scope IN ('profile', 'key')),
    CHECK (workload_identity_authority_id ~ '^[a-z][a-z0-9._-]{0,127}$'),
    CHECK (workload_identity_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (
        (scope = 'profile' AND workload_identity_signing_key_id IS NULL) OR
        (scope = 'key' AND workload_identity_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$')
    ),
    CHECK (external_reason_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (revocation_authority_id ~ '^[a-z][a-z0-9._-]{0,127}$'),
    CHECK (revocation_authority_id <> workload_identity_authority_id),
    CHECK (revocation_trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (revocation_signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$'),
    CHECK (
        isfinite(issued_at) AND isfinite(effective_at) AND isfinite(recorded_at) AND
        date_trunc('microseconds', issued_at) = issued_at AND
        date_trunc('microseconds', effective_at) = effective_at
    ),
    CHECK (actor_id <> '' AND length(actor_id) <= 256 AND actor_id !~ '[[:cntrl:]]'),
    CHECK (octet_length(reason_digest) = 32),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$')
);

CREATE FUNCTION enforce_audit_export_workload_identity_revocation_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-workload-identity-revocation' || E'\n' || NEW.isolation_domain_id, 0
    ));
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_workload_identity_revocations_controlled_insert
BEFORE INSERT ON audit_export_workload_identity_revocations
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_workload_identity_revocation_insert();

CREATE FUNCTION reject_audit_export_workload_identity_revocation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit export workload identity revocations are append-only';
END;
$$;

CREATE TRIGGER audit_export_workload_identity_revocations_append_only
BEFORE UPDATE OR DELETE ON audit_export_workload_identity_revocations
FOR EACH ROW EXECUTE FUNCTION reject_audit_export_workload_identity_revocation_mutation();

CREATE FUNCTION audit_export_workload_identity_is_revoked(
    requested_domain text,
    requested_authority text,
    requested_trust_profile text,
    requested_signing_key text,
    requested_at timestamptz
)
RETURNS boolean
LANGUAGE sql
VOLATILE
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM audit_export_workload_identity_revocations AS revocation
        WHERE revocation.isolation_domain_id = requested_domain
          AND revocation.workload_identity_authority_id = requested_authority
          AND revocation.workload_identity_trust_profile_sha256 = requested_trust_profile
          AND revocation.effective_at <= requested_at
          AND (
              revocation.scope = 'profile' OR
              (revocation.scope = 'key' AND
               revocation.workload_identity_signing_key_id = requested_signing_key)
          )
    );
$$;

CREATE OR REPLACE FUNCTION enforce_audit_export_workload_identity_sequence()
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
        'audit-export-workload-identity-revocation' || E'\n' || NEW.isolation_domain_id, 0
    ));
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
        NEW.not_before > clock_timestamp() OR NEW.expires_at <= clock_timestamp() OR
        audit_export_workload_identity_is_revoked(
            NEW.isolation_domain_id, NEW.authority_id, NEW.issuer_trust_profile_sha256,
            NEW.issuer_signing_key_id, clock_timestamp()
        )) THEN
        RAISE EXCEPTION 'audit export workload identity grant is not authorized';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION audit_export_workload_identity_is_authorized(
    requested_domain text,
    requested_workload text,
    requested_generation bigint,
    requested_grant text,
    requested_certificate text
)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
AS $$
DECLARE
    authorized boolean;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'audit-export-workload-identity-revocation' || E'\n' || requested_domain, 0
    ));
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
          AND NOT audit_export_workload_identity_is_revoked(
              identity_event.isolation_domain_id, identity_event.authority_id,
              identity_event.issuer_trust_profile_sha256, identity_event.issuer_signing_key_id,
              clock_timestamp()
          )
    ) INTO authorized;
    RETURN authorized;
END;
$$;

CREATE OR REPLACE FUNCTION enforce_audit_export_delivery_transport_insert()
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
                'audit-export-workload-identity-revocation' || E'\n' || delivery.isolation_domain_id, 0
            ));
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

-- dataground:down

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM audit_export_workload_identity_revocations) THEN
        RAISE EXCEPTION 'cannot roll back workload identity revocation evidence';
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION enforce_audit_export_delivery_transport_insert()
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

CREATE OR REPLACE FUNCTION audit_export_workload_identity_is_authorized(
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

CREATE OR REPLACE FUNCTION enforce_audit_export_workload_identity_sequence()
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

DROP FUNCTION audit_export_workload_identity_is_revoked(text, text, text, text, timestamptz);
DROP TRIGGER audit_export_workload_identity_revocations_append_only
    ON audit_export_workload_identity_revocations;
DROP FUNCTION reject_audit_export_workload_identity_revocation_mutation();
DROP TRIGGER audit_export_workload_identity_revocations_controlled_insert
    ON audit_export_workload_identity_revocations;
DROP FUNCTION enforce_audit_export_workload_identity_revocation_insert();
DROP TABLE audit_export_workload_identity_revocations;
