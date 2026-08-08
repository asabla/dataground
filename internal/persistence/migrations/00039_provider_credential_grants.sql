-- dataground:up

CREATE TABLE provider_credential_grant_events (
    contract text NOT NULL,
    isolation_domain_id text NOT NULL,
    revision_id text NOT NULL,
    provider_profile text NOT NULL,
    purpose text NOT NULL,
    generation bigint NOT NULL,
    operation text NOT NULL,
    activated_at timestamptz,
    expires_at timestamptz,
    actor_id text NOT NULL,
    reason_digest bytea NOT NULL,
    correlation_id text NOT NULL UNIQUE,
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
    PRIMARY KEY (
        isolation_domain_id,
        revision_id,
        provider_profile,
        purpose,
        generation
    ),
    CHECK (contract = 'dataground.provider-credential-grant/v1'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (revision_id ~ '^rev_[0-9a-z]{20,32}$'),
    CHECK (provider_profile ~ '^[a-z][a-z0-9]*([._-][a-z0-9]+)*$'
        AND length(provider_profile) <= 64),
    CHECK (purpose = 'agent-inference'),
    CHECK (generation > 0),
    CHECK (operation IN ('activate', 'revoke')),
    CHECK (
        (operation = 'activate'
            AND activated_at IS NOT NULL
            AND expires_at IS NOT NULL
            AND isfinite(activated_at)
            AND isfinite(expires_at)
            AND expires_at > activated_at
            AND expires_at - activated_at <= interval '24 hours')
        OR
        (operation = 'revoke'
            AND activated_at IS NULL
            AND expires_at IS NULL)
    ),
    CHECK (actor_id <> '' AND length(actor_id) <= 256 AND actor_id !~ '[[:cntrl:]]'),
    CHECK (octet_length(reason_digest) = 32),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (isfinite(occurred_at))
);

CREATE FUNCTION enforce_provider_credential_grant_sequence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    latest_generation bigint;
    latest_operation text;
BEGIN
    IF NEW.creation_transaction_id <> txid_current() THEN
        RAISE EXCEPTION 'provider credential grant transaction binding is invalid';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'provider-credential-grant' || E'\n' ||
        NEW.isolation_domain_id || E'\n' ||
        NEW.revision_id || E'\n' ||
        NEW.provider_profile || E'\n' ||
        NEW.purpose,
        0
    ));
    SELECT generation, operation
    INTO latest_generation, latest_operation
    FROM provider_credential_grant_events
    WHERE isolation_domain_id = NEW.isolation_domain_id
      AND revision_id = NEW.revision_id
      AND provider_profile = NEW.provider_profile
      AND purpose = NEW.purpose
    ORDER BY generation DESC
    LIMIT 1;
    IF NOT FOUND THEN
        IF NEW.generation <> 1 OR NEW.operation <> 'activate' THEN
            RAISE EXCEPTION 'provider credential grant must begin with generation 1 activation';
        END IF;
    ELSIF latest_generation = 9223372036854775807 OR
          NEW.generation <> latest_generation + 1 THEN
        RAISE EXCEPTION 'provider credential grant generations must be sequential';
    ELSIF NEW.operation = 'revoke' AND latest_operation <> 'activate' THEN
        RAISE EXCEPTION 'provider credential grant is not active';
    END IF;
    IF NEW.operation = 'activate' AND
       (NEW.activated_at > clock_timestamp() OR NEW.expires_at <= clock_timestamp()) THEN
        RAISE EXCEPTION 'provider credential grant is not currently valid';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER provider_credential_grant_events_sequence
BEFORE INSERT ON provider_credential_grant_events
FOR EACH ROW EXECUTE FUNCTION enforce_provider_credential_grant_sequence();

CREATE FUNCTION reject_provider_credential_grant_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'provider credential grants are append-only';
END;
$$;

CREATE TRIGGER provider_credential_grant_events_append_only
BEFORE UPDATE OR DELETE ON provider_credential_grant_events
FOR EACH ROW EXECUTE FUNCTION reject_provider_credential_grant_mutation();

CREATE TABLE provider_credential_authorization_decisions (
    id text PRIMARY KEY,
    contract text NOT NULL,
    isolation_domain_id text NOT NULL,
    revision_id text NOT NULL,
    operation_id text NOT NULL,
    provider_profile text NOT NULL,
    purpose text NOT NULL,
    phase text NOT NULL,
    grant_generation bigint,
    outcome text NOT NULL,
    actor_id text NOT NULL,
    correlation_id text NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (id ~ '^pcd_[0-9a-z]{20,32}$'),
    CHECK (contract = 'dataground.provider-credential-authorization/v1'),
    CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
    CHECK (revision_id ~ '^rev_[0-9a-z]{20,32}$'),
    CHECK (operation_id ~ '^op_[0-9a-z]{20,32}$'),
    CHECK (provider_profile ~ '^[a-z][a-z0-9]*([._-][a-z0-9]+)*$'
        AND length(provider_profile) <= 64),
    CHECK (purpose = 'agent-inference'),
    CHECK (phase IN ('admission', 'effect')),
    CHECK (grant_generation IS NULL OR grant_generation > 0),
    CHECK (outcome IN ('allowed', 'denied')),
    CHECK (outcome <> 'allowed' OR grant_generation IS NOT NULL),
    CHECK (actor_id <> '' AND length(actor_id) <= 256 AND actor_id !~ '[[:cntrl:]]'),
    CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
    CHECK (isfinite(occurred_at))
);

CREATE INDEX provider_credential_authorization_decisions_scope
ON provider_credential_authorization_decisions (
    isolation_domain_id,
    revision_id,
    operation_id,
    occurred_at,
    id
);

CREATE FUNCTION reject_provider_credential_authorization_decision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'provider credential authorization decisions are append-only';
END;
$$;

CREATE TRIGGER provider_credential_authorization_decisions_append_only
BEFORE UPDATE OR DELETE ON provider_credential_authorization_decisions
FOR EACH ROW EXECUTE FUNCTION reject_provider_credential_authorization_decision_mutation();

-- dataground:down

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM provider_credential_grant_events)
       OR EXISTS (SELECT 1 FROM provider_credential_authorization_decisions) THEN
        RAISE EXCEPTION 'cannot remove provider credential authorization evidence';
    END IF;
END;
$$;

DROP TRIGGER provider_credential_authorization_decisions_append_only
    ON provider_credential_authorization_decisions;
DROP FUNCTION reject_provider_credential_authorization_decision_mutation();
DROP TABLE provider_credential_authorization_decisions;

DROP TRIGGER provider_credential_grant_events_append_only
    ON provider_credential_grant_events;
DROP FUNCTION reject_provider_credential_grant_mutation();
DROP TRIGGER provider_credential_grant_events_sequence
    ON provider_credential_grant_events;
DROP FUNCTION enforce_provider_credential_grant_sequence();
DROP TABLE provider_credential_grant_events;
