-- dataground:up

CREATE TABLE oidc_dpop_nonces (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    isolation_domain_id text NOT NULL,
    key_thumbprint_digest bytea NOT NULL,
    nonce_digest bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    issued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT oidc_dpop_nonces_value_key
        UNIQUE (isolation_domain_id, key_thumbprint_digest, nonce_digest),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (octet_length(key_thumbprint_digest) = 32),
    CHECK (octet_length(nonce_digest) = 32),
    CHECK (expires_at > issued_at)
);

CREATE INDEX oidc_dpop_nonces_expiry_idx
    ON oidc_dpop_nonces (expires_at, sequence);

CREATE INDEX oidc_dpop_nonces_binding_recency_idx
    ON oidc_dpop_nonces (
        isolation_domain_id,
        key_thumbprint_digest,
        issued_at DESC,
        sequence DESC
    );

CREATE FUNCTION protect_oidc_dpop_nonce()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.expires_at > NEW.issued_at + interval '5 minutes' THEN
            RAISE EXCEPTION 'DPoP nonce lifetime is invalid';
        END IF;
        RETURN NEW;
    ELSIF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'DPoP nonces are immutable';
    END IF;
    RETURN OLD;
END;
$$;

CREATE TRIGGER oidc_dpop_nonces_protected
BEFORE INSERT OR UPDATE ON oidc_dpop_nonces
FOR EACH ROW EXECUTE FUNCTION protect_oidc_dpop_nonce();

-- dataground:down

DROP TABLE oidc_dpop_nonces;
DROP FUNCTION protect_oidc_dpop_nonce();
