-- dataground:up

CREATE TABLE oidc_identity_bindings (
    isolation_domain_id text NOT NULL,
    issuer text NOT NULL,
    subject text NOT NULL,
    principal_id text NOT NULL,
    principal_kind text NOT NULL,
    registered_by text NOT NULL,
    registration_correlation_id text NOT NULL,
    reason_digest bytea NOT NULL,
    registered_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (isolation_domain_id, issuer, subject),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (octet_length(issuer) BETWEEN 1 AND 512),
    CHECK (issuer LIKE 'https://%'),
    CHECK (issuer = btrim(issuer)),
    CHECK (issuer !~ '[[:cntrl:]]'),
    CHECK (octet_length(subject) BETWEEN 1 AND 512),
    CHECK (subject = btrim(subject)),
    CHECK (subject !~ '[[:cntrl:]]'),
    CHECK (principal_id ~ '^[a-z][a-z0-9_-]{2,127}$'),
    CHECK (principal_kind IN ('human', 'service')),
    CHECK (registered_by ~ '^[a-z][a-z0-9_-]{2,127}$'),
    CHECK (registration_correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (octet_length(reason_digest) = 32)
);

CREATE INDEX oidc_identity_bindings_identity_idx
    ON oidc_identity_bindings (issuer, subject, isolation_domain_id);

CREATE FUNCTION enforce_oidc_identity_principal_consistency()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(NEW.issuer || E'\n' || NEW.subject, 0));
    IF EXISTS (
        SELECT 1
        FROM oidc_identity_bindings
        WHERE issuer = NEW.issuer
          AND subject = NEW.subject
          AND (
              principal_id <> NEW.principal_id
              OR principal_kind <> NEW.principal_kind
          )
    ) THEN
        RAISE EXCEPTION 'OIDC identity is already bound to another principal';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER oidc_identity_bindings_principal_consistency
BEFORE INSERT ON oidc_identity_bindings
FOR EACH ROW EXECUTE FUNCTION enforce_oidc_identity_principal_consistency();

CREATE TABLE oidc_identity_revocations (
    isolation_domain_id text NOT NULL,
    issuer text NOT NULL,
    subject text NOT NULL,
    principal_id text NOT NULL,
    revoked_by text NOT NULL,
    revocation_correlation_id text NOT NULL,
    reason_digest bytea NOT NULL,
    revoked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (isolation_domain_id, issuer, subject),
    FOREIGN KEY (isolation_domain_id, issuer, subject)
        REFERENCES oidc_identity_bindings (isolation_domain_id, issuer, subject),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (octet_length(issuer) BETWEEN 1 AND 512),
    CHECK (octet_length(subject) BETWEEN 1 AND 512),
    CHECK (principal_id ~ '^[a-z][a-z0-9_-]{2,127}$'),
    CHECK (revoked_by ~ '^[a-z][a-z0-9_-]{2,127}$'),
    CHECK (revocation_correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (octet_length(reason_digest) = 32)
);

CREATE FUNCTION enforce_oidc_identity_revocation_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM oidc_identity_bindings
        WHERE isolation_domain_id = NEW.isolation_domain_id
          AND issuer = NEW.issuer
          AND subject = NEW.subject
          AND principal_id = NEW.principal_id
    ) THEN
        RAISE EXCEPTION 'OIDC identity revocation does not match its binding';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER oidc_identity_revocations_binding
BEFORE INSERT ON oidc_identity_revocations
FOR EACH ROW EXECUTE FUNCTION enforce_oidc_identity_revocation_binding();

CREATE FUNCTION reject_oidc_identity_registry_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'OIDC identity registry records are append-only';
END;
$$;

CREATE TRIGGER oidc_identity_bindings_append_only
BEFORE UPDATE OR DELETE ON oidc_identity_bindings
FOR EACH ROW EXECUTE FUNCTION reject_oidc_identity_registry_mutation();

CREATE TRIGGER oidc_identity_revocations_append_only
BEFORE UPDATE OR DELETE ON oidc_identity_revocations
FOR EACH ROW EXECUTE FUNCTION reject_oidc_identity_registry_mutation();

-- dataground:down

DROP TABLE oidc_identity_revocations;
DROP TABLE oidc_identity_bindings;
DROP FUNCTION reject_oidc_identity_registry_mutation();
DROP FUNCTION enforce_oidc_identity_revocation_binding();
DROP FUNCTION enforce_oidc_identity_principal_consistency();
