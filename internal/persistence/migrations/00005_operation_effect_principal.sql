-- dataground:up

ALTER TABLE service_publication_operations
    ADD COLUMN effect_actor_id text,
    ADD COLUMN effect_correlation_id text,
    ADD CONSTRAINT service_publication_effect_principal_pair
        CHECK ((effect_actor_id IS NULL) = (effect_correlation_id IS NULL));

ALTER TABLE invocation_execution_operations
    ADD COLUMN effect_actor_id text,
    ADD COLUMN effect_correlation_id text,
    ADD CONSTRAINT invocation_execution_effect_principal_pair
        CHECK ((effect_actor_id IS NULL) = (effect_correlation_id IS NULL));

ALTER TABLE inbox_records
    ADD COLUMN actor_id text;

WITH latest_repair AS (
    SELECT DISTINCT ON (isolation_domain_id, operation_id)
           isolation_domain_id, operation_id, actor_id, correlation_id
    FROM audit_records
    WHERE action = 'service-publication.repair-accepted'
    ORDER BY isolation_domain_id, operation_id, occurred_at DESC, id DESC
)
UPDATE service_publication_operations AS operation
SET effect_actor_id = repair.actor_id,
    effect_correlation_id = repair.correlation_id
FROM latest_repair AS repair
WHERE operation.command = 'repair'
  AND repair.isolation_domain_id = operation.isolation_domain_id
  AND repair.operation_id = operation.id;

WITH latest_repair AS (
    SELECT DISTINCT ON (isolation_domain_id, operation_id)
           isolation_domain_id, operation_id, actor_id, correlation_id
    FROM audit_records
    WHERE action = 'invocation-execution.repair-accepted'
    ORDER BY isolation_domain_id, operation_id, occurred_at DESC, id DESC
)
UPDATE invocation_execution_operations AS operation
SET effect_actor_id = repair.actor_id,
    effect_correlation_id = repair.correlation_id
FROM latest_repair AS repair
WHERE operation.command = 'repair'
  AND repair.isolation_domain_id = operation.isolation_domain_id
  AND repair.operation_id = operation.id;

WITH repair_actor AS (
    SELECT DISTINCT ON (isolation_domain_id, correlation_id)
           isolation_domain_id, correlation_id, actor_id
    FROM audit_records
    WHERE action IN (
        'service-publication.repair-accepted',
        'invocation-execution.repair-accepted'
    )
    ORDER BY isolation_domain_id, correlation_id, occurred_at DESC, id DESC
)
UPDATE inbox_records AS inbox
SET actor_id = repair.actor_id
FROM repair_actor AS repair
WHERE inbox.source_kind = 'command'
  AND repair.isolation_domain_id = inbox.isolation_domain_id
  AND repair.correlation_id = inbox.deduplication_id;

-- dataground:down

ALTER TABLE inbox_records
    DROP COLUMN actor_id;

ALTER TABLE invocation_execution_operations
    DROP CONSTRAINT invocation_execution_effect_principal_pair,
    DROP COLUMN effect_correlation_id,
    DROP COLUMN effect_actor_id;

ALTER TABLE service_publication_operations
    DROP CONSTRAINT service_publication_effect_principal_pair,
    DROP COLUMN effect_correlation_id,
    DROP COLUMN effect_actor_id;
