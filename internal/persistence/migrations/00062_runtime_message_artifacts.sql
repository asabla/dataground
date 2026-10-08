-- dataground:up

ALTER TABLE invocation_runtime_attempts
    DROP CONSTRAINT invocation_runtime_attempts_terminal_check,
    ADD CONSTRAINT invocation_runtime_attempts_terminal_check CHECK (
        (status = 'reserved' AND result IS NULL AND completed_at IS NULL)
        OR (status = 'output_invalid' AND (result IS NOT NULL AND completed_at IS NULL
            AND jsonb_typeof(result) = 'object'
            AND result ?& ARRAY['code', 'status', 'sourceSequence', 'artifactId', 'artifactDigest', 'sizeBytes']
            AND result - ARRAY['code', 'status', 'sourceSequence', 'artifactId', 'artifactDigest', 'sizeBytes'] = '{}'::jsonb
            AND result->>'code' = 'RUNTIME_OUTPUT_INVALID' AND result->>'status' = 'failed'
            AND jsonb_typeof(result->'sourceSequence') = 'string'
            AND result->>'sourceSequence' ~ '^[1-9][0-9]{0,18}$'
            AND (result->>'sourceSequence')::numeric <= 9223372036854775807
            AND result->>'artifactId' ~ '^art_[0-9a-z]{20,32}$'
            AND result->>'artifactDigest' ~ '^sha256:[0-9a-f]{64}$'
            AND jsonb_typeof(result->'sizeBytes') = 'number'
            AND result->>'sizeBytes' ~ '^(0|[1-9][0-9]{0,6})$'
            AND (result->>'sizeBytes')::numeric <= 1048576) IS TRUE)
        OR (status IN ('succeeded', 'failed') AND result IS NOT NULL AND completed_at IS NOT NULL)
    );

-- dataground:down

ALTER TABLE invocation_runtime_attempts
    DROP CONSTRAINT invocation_runtime_attempts_terminal_check,
    ADD CONSTRAINT invocation_runtime_attempts_terminal_check CHECK (
        (status = 'reserved' AND result IS NULL AND completed_at IS NULL)
        OR (status = 'output_invalid' AND (result IS NOT NULL AND completed_at IS NULL
            AND jsonb_typeof(result) = 'object'
            AND result ?& ARRAY['code', 'status', 'sourceSequence', 'artifactId', 'artifactDigest', 'sizeBytes']
            AND result - ARRAY['code', 'status', 'sourceSequence', 'artifactId', 'artifactDigest', 'sizeBytes'] = '{}'::jsonb
            AND result->>'code' = 'RUNTIME_OUTPUT_INVALID' AND result->>'status' = 'failed'
            AND jsonb_typeof(result->'sourceSequence') = 'string'
            AND result->>'sourceSequence' ~ '^[1-9][0-9]{0,18}$'
            AND (result->>'sourceSequence')::numeric <= 9223372036854775807
            AND result->>'artifactId' ~ '^art_[0-9a-z]{20,32}$'
            AND result->>'artifactDigest' ~ '^sha256:[0-9a-f]{64}$'
            AND jsonb_typeof(result->'sizeBytes') = 'number'
            AND result->>'sizeBytes' ~ '^(0|[1-9][0-9]{0,5})$'
            AND (result->>'sizeBytes')::numeric <= 65536) IS TRUE)
        OR (status IN ('succeeded', 'failed') AND result IS NOT NULL AND completed_at IS NOT NULL)
    );
