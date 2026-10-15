-- dataground:up
CREATE TABLE workspace_source_grants (
 contract text NOT NULL CHECK (contract = 'dataground.workspace-source-grant/v1'),
 isolation_domain_id text NOT NULL,
 workspace_id text NOT NULL,
 source_id text NOT NULL,
 revision_id text NOT NULL,
 archive_digest text NOT NULL CHECK (archive_digest ~ '^sha256:[0-9a-f]{64}$'),
 storage_profile_digest text NOT NULL CHECK (storage_profile_digest ~ '^sha256:[0-9a-f]{64}$'),
 generation bigint NOT NULL CHECK (generation > 0),
 operation text NOT NULL CHECK (operation IN ('activate','revoke')),
 activated_at timestamptz,
 expires_at timestamptz,
 actor_id text NOT NULL CHECK (octet_length(actor_id) BETWEEN 1 AND 256 AND actor_id !~ '[[:cntrl:]]'),
 reason_digest text NOT NULL CHECK (reason_digest ~ '^sha256:[0-9a-f]{64}$'),
 correlation_id text NOT NULL CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
 occurred_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(occurred_at)),
 PRIMARY KEY (isolation_domain_id,workspace_id,source_id,revision_id,generation),
 UNIQUE (isolation_domain_id,correlation_id),
 FOREIGN KEY (isolation_domain_id,workspace_id,source_id) REFERENCES workspace_source_completions (isolation_domain_id,workspace_id,id),
 FOREIGN KEY (isolation_domain_id,revision_id) REFERENCES service_revisions (isolation_domain_id,id),
 CHECK ((operation='activate' AND activated_at IS NOT NULL AND expires_at IS NOT NULL AND isfinite(activated_at) AND isfinite(expires_at) AND expires_at>activated_at AND expires_at-activated_at<=interval '1 hour') OR (operation='revoke' AND activated_at IS NULL AND expires_at IS NULL))
);
CREATE FUNCTION enforce_workspace_source_grant_sequence() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 previous_generation bigint;
 previous_operation text;
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('workspace-source-grant' || E'\n' || NEW.isolation_domain_id || E'\n' || NEW.workspace_id || E'\n' || NEW.source_id || E'\n' || NEW.revision_id,0));
 SELECT generation,operation INTO previous_generation,previous_operation FROM workspace_source_grants
 WHERE isolation_domain_id=NEW.isolation_domain_id AND workspace_id=NEW.workspace_id AND source_id=NEW.source_id AND revision_id=NEW.revision_id ORDER BY generation DESC LIMIT 1;
 IF NOT FOUND THEN
  IF NEW.generation<>1 OR NEW.operation<>'activate' THEN RAISE EXCEPTION 'source grant must begin with generation 1 activation'; END IF;
 ELSIF previous_generation=9223372036854775807 OR NEW.generation<>previous_generation+1 THEN
  RAISE EXCEPTION 'source grant generations must be sequential';
 ELSIF NEW.operation='revoke' AND previous_operation<>'activate' THEN
  RAISE EXCEPTION 'source grant is not active';
 END IF;
 IF NOT EXISTS (SELECT 1 FROM workspace_source_installations WHERE isolation_domain_id=NEW.isolation_domain_id AND workspace_id=NEW.workspace_id AND id=NEW.source_id AND archive_digest=NEW.archive_digest AND storage_profile_digest=NEW.storage_profile_digest) THEN
  RAISE EXCEPTION 'source grant does not match immutable source';
 END IF;
 IF NEW.operation='activate' THEN
  PERFORM id FROM service_revisions WHERE isolation_domain_id=NEW.isolation_domain_id AND id=NEW.revision_id AND state='published' FOR SHARE;
  IF NOT FOUND OR NEW.activated_at>clock_timestamp() OR NEW.expires_at<=clock_timestamp() THEN
   RAISE EXCEPTION 'source grant activation is not currently valid';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER workspace_source_grants_sequence BEFORE INSERT ON workspace_source_grants FOR EACH ROW EXECUTE FUNCTION enforce_workspace_source_grant_sequence();
CREATE TRIGGER workspace_source_grants_append_only BEFORE UPDATE OR DELETE ON workspace_source_grants FOR EACH ROW EXECUTE FUNCTION reject_workspace_source_mutation();

CREATE TABLE workspace_source_use_decisions (
 isolation_domain_id text NOT NULL CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
 id text NOT NULL CHECK (id ~ '^sud_[0-9a-z]{20,32}$'),
 contract text NOT NULL CHECK (contract = 'dataground.workspace-source-use/v1'),
 workspace_id text NOT NULL CHECK (workspace_id ~ '^wsp_[0-9a-z]{20,32}$'),
 source_id text NOT NULL CHECK (source_id ~ '^src_[0-9a-z]{20,32}$'),
 revision_id text NOT NULL CHECK (revision_id ~ '^rev_[0-9a-z]{20,32}$'),
 operation_id text NOT NULL CHECK (operation_id ~ '^op_[0-9a-z]{20,32}$'),
 archive_digest text NOT NULL CHECK (archive_digest ~ '^sha256:[0-9a-f]{64}$'),
 storage_profile_digest text NOT NULL CHECK (storage_profile_digest ~ '^sha256:[0-9a-f]{64}$'),
 requested_generation bigint NOT NULL CHECK (requested_generation>0),
 observed_generation bigint CHECK (observed_generation>0),
 expires_at timestamptz,
 phase text NOT NULL CHECK (phase IN ('read-admission','read-delivery')),
 outcome text NOT NULL CHECK (outcome IN ('allowed','denied')),
 actor_id text NOT NULL CHECK (octet_length(actor_id) BETWEEN 1 AND 256 AND actor_id !~ '[[:cntrl:]]'),
 correlation_id text NOT NULL CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
 occurred_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(occurred_at)),
 PRIMARY KEY (isolation_domain_id,id),
 CHECK (expires_at IS NULL OR isfinite(expires_at)),
 CHECK (outcome<>'allowed' OR (observed_generation IS NOT NULL AND requested_generation=observed_generation AND expires_at IS NOT NULL AND expires_at>occurred_at))
);
CREATE INDEX workspace_source_use_decisions_scope ON workspace_source_use_decisions (isolation_domain_id,workspace_id,source_id,revision_id,operation_id,occurred_at,id);
CREATE TRIGGER workspace_source_use_decisions_append_only BEFORE UPDATE OR DELETE ON workspace_source_use_decisions FOR EACH ROW EXECUTE FUNCTION reject_workspace_source_mutation();

-- dataground:down
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM workspace_source_grants) OR EXISTS (SELECT 1 FROM workspace_source_use_decisions) THEN RAISE EXCEPTION 'cannot remove workspace source grant evidence'; END IF;
END; $$;
DROP TABLE workspace_source_use_decisions;
DROP TABLE workspace_source_grants;
DROP FUNCTION enforce_workspace_source_grant_sequence();
