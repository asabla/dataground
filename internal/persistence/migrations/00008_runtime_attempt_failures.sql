-- dataground:up

ALTER TABLE invocation_runtime_attempts
    DROP CONSTRAINT invocation_runtime_attempts_status_check,
    DROP CONSTRAINT invocation_runtime_attempts_check,
    ADD CONSTRAINT invocation_runtime_attempts_status_check
        CHECK (status IN ('reserved', 'succeeded', 'failed')),
    ADD CONSTRAINT invocation_runtime_attempts_terminal_check
        CHECK (
            (status = 'reserved' AND result IS NULL AND completed_at IS NULL)
            OR (status IN ('succeeded', 'failed') AND result IS NOT NULL AND completed_at IS NOT NULL)
        );

-- dataground:down

DELETE FROM invocation_runtime_attempts
WHERE status = 'failed';

ALTER TABLE invocation_runtime_attempts
    DROP CONSTRAINT invocation_runtime_attempts_status_check,
    DROP CONSTRAINT invocation_runtime_attempts_terminal_check,
    ADD CONSTRAINT invocation_runtime_attempts_status_check
        CHECK (status IN ('reserved', 'succeeded')),
    ADD CONSTRAINT invocation_runtime_attempts_check
        CHECK (
            (status = 'reserved' AND result IS NULL AND completed_at IS NULL)
            OR (status = 'succeeded' AND result IS NOT NULL AND completed_at IS NOT NULL)
        );
