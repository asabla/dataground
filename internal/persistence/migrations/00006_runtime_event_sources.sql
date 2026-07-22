-- dataground:up

ALTER TABLE invocation_events
    ADD COLUMN source_kind text,
    ADD COLUMN source_sequence bigint,
    ADD CONSTRAINT invocation_events_source_pair_check CHECK (
        (source_kind IS NULL AND source_sequence IS NULL)
        OR (source_kind = 'runtime' AND source_sequence >= 1)
    );

CREATE UNIQUE INDEX invocation_events_runtime_source_idx
    ON invocation_events (isolation_domain_id, invocation_id, source_kind, source_sequence)
    WHERE source_kind = 'runtime';

-- dataground:down

DROP INDEX invocation_events_runtime_source_idx;

ALTER TABLE invocation_events
    DROP CONSTRAINT invocation_events_source_pair_check,
    DROP COLUMN source_sequence,
    DROP COLUMN source_kind;
