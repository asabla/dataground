-- dataground:up
CREATE TABLE workspace_source_installations (
 isolation_domain_id text NOT NULL CHECK (isolation_domain_id ~ '^iso_[0-9a-z]{20,32}$'),
 workspace_id text NOT NULL CHECK (workspace_id ~ '^wsp_[0-9a-z]{20,32}$'),
 id text NOT NULL CHECK (id ~ '^src_[0-9a-z]{20,32}$'),
 schema_version text NOT NULL CHECK (schema_version = 'dataground.workspace-source/v1'),
 archive_digest text NOT NULL CHECK (archive_digest ~ '^sha256:[0-9a-f]{64}$'),
 storage_profile_digest text NOT NULL CHECK (storage_profile_digest ~ '^sha256:[0-9a-f]{64}$'),
 archive_bytes bigint NOT NULL CHECK (archive_bytes BETWEEN 1024 AND 33554432 AND archive_bytes % 512 = 0),
 entry_count integer NOT NULL CHECK (entry_count BETWEEN 0 AND 4096),
 source_bytes bigint NOT NULL CHECK (source_bytes BETWEEN 0 AND 25165824),
 actor_id text NOT NULL CHECK (octet_length(actor_id) BETWEEN 1 AND 256 AND actor_id !~ '[[:cntrl:]]'),
 correlation_id text NOT NULL CHECK (correlation_id ~ '^cor_[0-9a-z]{20,32}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(created_at)),
 PRIMARY KEY (isolation_domain_id, workspace_id, id),
 FOREIGN KEY (isolation_domain_id, workspace_id) REFERENCES workspaces (isolation_domain_id, id)
);
CREATE TABLE workspace_source_completions (
 isolation_domain_id text NOT NULL,
 workspace_id text NOT NULL,
 id text NOT NULL,
 completed_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(completed_at)),
 PRIMARY KEY (isolation_domain_id, workspace_id, id),
 FOREIGN KEY (isolation_domain_id, workspace_id, id) REFERENCES workspace_source_installations (isolation_domain_id, workspace_id, id)
);
CREATE FUNCTION reject_workspace_source_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'workspace source records are append-only';
END;
$$;
CREATE TRIGGER workspace_source_installations_append_only BEFORE UPDATE OR DELETE ON workspace_source_installations FOR EACH ROW EXECUTE FUNCTION reject_workspace_source_mutation();
CREATE TRIGGER workspace_source_completions_append_only BEFORE UPDATE OR DELETE ON workspace_source_completions FOR EACH ROW EXECUTE FUNCTION reject_workspace_source_mutation();

-- dataground:down
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM workspace_source_installations) OR EXISTS (SELECT 1 FROM workspace_source_completions) THEN
  RAISE EXCEPTION 'cannot remove workspace source records or audit evidence';
 END IF;
END; $$;
DROP TABLE workspace_source_completions;
DROP TABLE workspace_source_installations;
DROP FUNCTION reject_workspace_source_mutation();
