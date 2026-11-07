-- dataground:up
-- A terminal transport observation survives cancellation of the execution
-- lease. It does not complete quiescence, authorize export or permit deletion.
CREATE TABLE invocation_workspace_owner_stop_receipts (
 isolation_domain_id text NOT NULL,
 operation_id text NOT NULL,
 contract text NOT NULL CHECK (contract='dataground.invocation-workspace-owner-stop/v1'),
 owner_binding text NOT NULL,
 runtime_token bigint NOT NULL,
 actor_id text NOT NULL,
 correlation_id text NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(recorded_at)),
 PRIMARY KEY (isolation_domain_id,operation_id),
 FOREIGN KEY (isolation_domain_id,operation_id) REFERENCES invocation_workspace_quiescence_intents(isolation_domain_id,operation_id),
 CHECK (owner_binding ~ '^sha256:[0-9a-f]{64}$'),
 CHECK (runtime_token>0)
);
CREATE FUNCTION enforce_workspace_owner_stop_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS (
  SELECT 1 FROM invocation_workspace_quiescence_intents intent
  WHERE intent.isolation_domain_id=NEW.isolation_domain_id AND intent.operation_id=NEW.operation_id
  AND intent.record->>'ownerBinding'=NEW.owner_binding AND intent.runtime_token=NEW.runtime_token
  AND intent.actor_id=NEW.actor_id AND intent.correlation_id=NEW.correlation_id
 ) THEN RAISE EXCEPTION 'owner stop receipt requires its original retained binding'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER workspace_owner_stop_binding BEFORE INSERT ON invocation_workspace_owner_stop_receipts FOR EACH ROW EXECUTE FUNCTION enforce_workspace_owner_stop_receipt();
CREATE TRIGGER workspace_owner_stop_append_only BEFORE UPDATE OR DELETE ON invocation_workspace_owner_stop_receipts FOR EACH ROW EXECUTE FUNCTION reject_workspace_source_mutation();

-- dataground:down
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM invocation_workspace_owner_stop_receipts) THEN
  RAISE EXCEPTION 'cannot remove workspace owner stop evidence';
 END IF;
END; $$;
DROP TABLE invocation_workspace_owner_stop_receipts;
DROP FUNCTION enforce_workspace_owner_stop_receipt();
