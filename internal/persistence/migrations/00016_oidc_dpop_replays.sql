-- dataground:up

CREATE TABLE oidc_dpop_replays (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    isolation_domain_id text NOT NULL,
    key_thumbprint_digest bytea NOT NULL,
    proof_id_digest bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    reserved_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT oidc_dpop_replays_proof_key
        UNIQUE (key_thumbprint_digest, proof_id_digest),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (octet_length(key_thumbprint_digest) = 32),
    CHECK (octet_length(proof_id_digest) = 32),
    CHECK (expires_at > reserved_at)
);

CREATE INDEX oidc_dpop_replays_scope_expiry_idx
    ON oidc_dpop_replays (isolation_domain_id, expires_at, sequence);

CREATE FUNCTION protect_oidc_dpop_replay()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.expires_at > clock_timestamp() + interval '7 minutes' THEN
            RAISE EXCEPTION 'DPoP replay reservation lifetime is invalid';
        END IF;
        RETURN NEW;
    ELSIF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'DPoP replay reservations are immutable';
    ELSE
        IF OLD.expires_at > clock_timestamp() THEN
            RAISE EXCEPTION 'active DPoP replay reservations cannot be deleted';
        END IF;
        RETURN OLD;
    END IF;
END;
$$;

CREATE TRIGGER oidc_dpop_replays_protected
BEFORE INSERT OR UPDATE OR DELETE ON oidc_dpop_replays
FOR EACH ROW EXECUTE FUNCTION protect_oidc_dpop_replay();

-- dataground:down

DROP TABLE oidc_dpop_replays;
DROP FUNCTION protect_oidc_dpop_replay();
