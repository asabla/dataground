-- dataground:up

CREATE TABLE audit_export_deliveries (
    delivery_id text PRIMARY KEY,
    isolation_domain_id text NOT NULL,
    contract text NOT NULL,
    export_kind text NOT NULL,
    export_id text NOT NULL,
    envelope_digest bytea NOT NULL,
    export_sha256 text NOT NULL,
    trust_profile_sha256 text NOT NULL,
    signing_key_id text NOT NULL,
    recipient_id text NOT NULL,
    destination_digest bytea NOT NULL,
    status text NOT NULL DEFAULT 'prepared',
    prepared_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    acknowledgement_digest bytea,
    acknowledged_at timestamptz,
    CHECK (delivery_id ~ '^adl_[0-9a-z]{20,32}$'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (contract = 'dataground.audit-export-delivery/v1'),
    CHECK (export_kind IN ('authorization', 'operator')),
    UNIQUE (delivery_id, isolation_domain_id),
    CHECK (
        (export_kind = 'authorization' AND export_id ~ '^aex_[0-9a-z]{20,32}$') OR
        (export_kind = 'operator' AND export_id ~ '^oax_[0-9a-z]{20,32}$')
    ),
    CHECK (octet_length(envelope_digest) = 32),
    CHECK (export_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (trust_profile_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (signing_key_id ~ '^[a-z][a-z0-9_-]{2,63}$'),
    CHECK (recipient_id ~ '^[a-z][a-z0-9._-]{0,127}$'),
    CHECK (octet_length(destination_digest) = 32),
    CHECK (status IN ('prepared', 'acknowledged')),
    CHECK (
        (status = 'prepared' AND acknowledgement_digest IS NULL AND acknowledged_at IS NULL) OR
        (status = 'acknowledged' AND octet_length(acknowledgement_digest) = 32 AND acknowledged_at IS NOT NULL)
    )
);

CREATE INDEX audit_export_deliveries_pending_idx
    ON audit_export_deliveries (prepared_at, isolation_domain_id, delivery_id)
    WHERE status = 'prepared';

CREATE TABLE audit_export_delivery_operations (
    delivery_id text NOT NULL,
    operation text NOT NULL,
    isolation_domain_id text NOT NULL,
    actor_id text NOT NULL,
    correlation_id text NOT NULL UNIQUE,
    reason_digest bytea NOT NULL,
    evidence_digest bytea NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (delivery_id, operation),
    FOREIGN KEY (delivery_id, isolation_domain_id)
        REFERENCES audit_export_deliveries(delivery_id, isolation_domain_id),
    CHECK (operation IN ('prepare', 'acknowledge')),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (actor_id <> '' AND length(actor_id) <= 256),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (octet_length(reason_digest) = 32),
    CHECK (octet_length(evidence_digest) = 32)
);

CREATE FUNCTION enforce_audit_export_delivery_transition()
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
       NEW.acknowledged_at IS NULL THEN
        RAISE EXCEPTION 'audit export deliveries permit only prepared-to-acknowledged transitions';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_export_deliveries_controlled_update
BEFORE UPDATE ON audit_export_deliveries
FOR EACH ROW EXECUTE FUNCTION enforce_audit_export_delivery_transition();

CREATE FUNCTION reject_audit_export_delivery_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit export deliveries cannot be deleted';
END;
$$;

CREATE TRIGGER audit_export_deliveries_no_delete
BEFORE DELETE ON audit_export_deliveries
FOR EACH ROW EXECUTE FUNCTION reject_audit_export_delivery_delete();

CREATE FUNCTION reject_audit_export_delivery_operation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit export delivery operations are append-only';
END;
$$;

CREATE TRIGGER audit_export_delivery_operations_append_only
BEFORE UPDATE OR DELETE ON audit_export_delivery_operations
FOR EACH ROW EXECUTE FUNCTION reject_audit_export_delivery_operation_mutation();

-- dataground:down

DROP TRIGGER audit_export_delivery_operations_append_only ON audit_export_delivery_operations;
DROP FUNCTION reject_audit_export_delivery_operation_mutation();
DROP TRIGGER audit_export_deliveries_no_delete ON audit_export_deliveries;
DROP FUNCTION reject_audit_export_delivery_delete();
DROP TRIGGER audit_export_deliveries_controlled_update ON audit_export_deliveries;
DROP FUNCTION enforce_audit_export_delivery_transition();
DROP TABLE audit_export_delivery_operations;
DROP TABLE audit_export_deliveries;
