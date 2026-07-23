package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
)

func (repository *Repository) BindInvocationArtifact(
	ctx context.Context,
	binding artifact.Binding,
) (artifact.Record, error) {
	normalized, err := artifact.NormalizeBinding(binding)
	if err != nil {
		return artifact.Record{}, err
	}
	if normalized.Record.EffectID != identity.Derived(
		"eff",
		normalized.Record.IsolationDomainID+":"+
			OperationKindInvocation+":"+
			normalized.Record.OperationID+
			":run-invocation",
	) {
		return artifact.Record{}, artifact.ErrInvocationArtifactInvalid
	}

	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return artifact.Record{}, fmt.Errorf("begin invocation artifact binding: %w", err)
	}
	defer transaction.Rollback(ctx)

	existing, found, err := findInvocationArtifactRecord(
		ctx,
		transaction,
		normalized.Record.IsolationDomainID,
		normalized.Record.ID,
	)
	if err != nil {
		return artifact.Record{}, err
	}
	if found {
		if !artifact.EqualRecords(existing, normalized.Record) {
			return artifact.Record{}, artifact.ErrInvocationArtifactConflict
		}
		if err := transaction.Commit(ctx); err != nil {
			return artifact.Record{}, fmt.Errorf("commit invocation artifact replay: %w", err)
		}
		return existing, nil
	}

	var invocationID string
	err = transaction.QueryRow(ctx, `
		SELECT invocation.id
		FROM invocation_execution_operations AS operation
		JOIN invocations AS invocation
		  ON invocation.isolation_domain_id = operation.isolation_domain_id
		 AND invocation.id = operation.invocation_id
		JOIN external_effects AS effect
		  ON effect.isolation_domain_id = operation.isolation_domain_id
		 AND effect.operation_kind = 'invocation-execution'
		 AND effect.operation_id = operation.id
		 AND effect.effect_id = $8
		 AND effect.phase = 'run-invocation'
		 AND effect.status IN ('prepared', 'failed', 'unknown')
		JOIN invocation_runtime_attempts AS attempt
		  ON attempt.isolation_domain_id = operation.isolation_domain_id
		 AND attempt.operation_id = operation.id
		 AND attempt.effect_id = effect.effect_id
		 AND attempt.lease_owner = operation.lease_owner
		 AND attempt.fencing_token = operation.lease_token
		 AND attempt.status = 'reserved'
		WHERE operation.isolation_domain_id = $1
		  AND operation.id = $2
		  AND invocation.id = $3
		  AND operation.state_machine_version = $4
		  AND operation.command IN ('invoke', 'repair')
		  AND operation.observed_state = 'running'
		  AND operation.lease_owner = $5
		  AND operation.lease_token = $6
		  AND COALESCE(operation.effect_actor_id, operation.actor_id) = $7
		  AND COALESCE(operation.effect_correlation_id, operation.correlation_id) = $9
		  AND operation.lease_expires_at > clock_timestamp()
		  AND operation.deadline_at > clock_timestamp()
		FOR UPDATE OF operation
	`,
		normalized.Record.IsolationDomainID,
		normalized.Record.OperationID,
		normalized.Record.InvocationID,
		normalized.StateMachineVersion,
		normalized.LeaseOwner,
		normalized.FencingToken,
		normalized.ActorID,
		normalized.Record.EffectID,
		normalized.CorrelationID,
	).Scan(&invocationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifact.Record{}, ErrLeaseLost
	}
	if err != nil {
		return artifact.Record{}, fmt.Errorf("lock invocation artifact claim: %w", err)
	}

	existing, found, err = findInvocationArtifactRecord(
		ctx,
		transaction,
		normalized.Record.IsolationDomainID,
		normalized.Record.ID,
	)
	if err != nil {
		return artifact.Record{}, err
	}
	if found {
		if !artifact.EqualRecords(existing, normalized.Record) {
			return artifact.Record{}, artifact.ErrInvocationArtifactConflict
		}
		if err := transaction.Commit(ctx); err != nil {
			return artifact.Record{}, fmt.Errorf("commit invocation artifact replay: %w", err)
		}
		return existing, nil
	}

	var publicRecordExists bool
	if err := transaction.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM artifacts
			WHERE isolation_domain_id = $1 AND id = $2
		)
	`, normalized.Record.IsolationDomainID, normalized.Record.ID).Scan(&publicRecordExists); err != nil {
		return artifact.Record{}, fmt.Errorf("inspect public invocation artifact: %w", err)
	}
	if publicRecordExists {
		return artifact.Record{}, artifact.ErrInvocationArtifactConflict
	}

	now := repository.now()
	_, err = transaction.Exec(ctx, `
		INSERT INTO artifacts (
			isolation_domain_id, id, invocation_id, name, kind, media_type,
			size_bytes, digest, state, sensitive, generation, version,
			created_at, updated_at, created_by
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, 'available', $9, 1, 1,
			$10, $10, $11
		)
	`,
		normalized.Record.IsolationDomainID,
		normalized.Record.ID,
		normalized.Record.InvocationID,
		normalized.Record.Name,
		normalized.Record.Kind,
		normalized.Record.MediaType,
		normalized.Record.SizeBytes,
		normalized.Record.Digest,
		normalized.Record.Sensitive,
		now,
		normalized.ActorID,
	)
	if err != nil {
		return artifact.Record{}, fmt.Errorf("publish invocation artifact descriptor: %w", err)
	}

	_, err = transaction.Exec(ctx, `
		INSERT INTO invocation_artifact_objects (
			isolation_domain_id, id, invocation_id, operation_id, effect_id,
			schema_version, name, kind, media_type, size_bytes, artifact_digest,
			sensitive, object_key, lease_owner, fencing_token, bound_by,
			correlation_id, created_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10, $11,
			$12, $13, $14, $15, $16,
			$17, $18
		)
	`,
		normalized.Record.IsolationDomainID,
		normalized.Record.ID,
		normalized.Record.InvocationID,
		normalized.Record.OperationID,
		normalized.Record.EffectID,
		normalized.Record.SchemaVersion,
		normalized.Record.Name,
		normalized.Record.Kind,
		normalized.Record.MediaType,
		normalized.Record.SizeBytes,
		normalized.Record.Digest,
		normalized.Record.Sensitive,
		normalized.Record.ObjectKey,
		normalized.LeaseOwner,
		normalized.FencingToken,
		normalized.ActorID,
		normalized.CorrelationID,
		now,
	)
	if err != nil {
		return artifact.Record{}, fmt.Errorf("bind invocation artifact object: %w", err)
	}

	safeMetadata, err := json.Marshal(map[string]any{
		"artifactDigest": normalized.Record.Digest,
		"artifactKind":   normalized.Record.Kind,
		"sensitive":      normalized.Record.Sensitive,
		"sizeBytes":      normalized.Record.SizeBytes,
	})
	if err != nil {
		return artifact.Record{}, fmt.Errorf("encode invocation artifact audit metadata: %w", err)
	}
	_, err = transaction.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type,
			resource_id, outcome, correlation_id, operation_id,
			safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, 'invocation-artifact.bind', 'invocation-artifact',
			$4, 'succeeded', $5, $6,
			$7, $8
		)
	`,
		identity.Derived(
			"aud",
			normalized.Record.IsolationDomainID+":"+
				normalized.Record.ID+
				":invocation-artifact.bind",
		),
		normalized.Record.IsolationDomainID,
		normalized.ActorID,
		normalized.Record.ID,
		normalized.CorrelationID,
		normalized.Record.OperationID,
		safeMetadata,
		now,
	)
	if err != nil {
		return artifact.Record{}, fmt.Errorf("audit invocation artifact binding: %w", err)
	}

	bound, found, err := findInvocationArtifactRecord(
		ctx,
		transaction,
		normalized.Record.IsolationDomainID,
		normalized.Record.ID,
	)
	if err != nil {
		return artifact.Record{}, err
	}
	if !found || !artifact.EqualRecords(bound, normalized.Record) {
		return artifact.Record{}, artifact.ErrInvocationArtifactConflict
	}
	if err := transaction.Commit(ctx); err != nil {
		return artifact.Record{}, fmt.Errorf("commit invocation artifact binding: %w", err)
	}
	return bound, nil
}

func (repository *Repository) GetInvocationArtifactRecord(
	ctx context.Context,
	isolationDomainID string,
	artifactID string,
) (artifact.Record, error) {
	record, found, err := findInvocationArtifactRecord(
		ctx,
		repository.pool,
		isolationDomainID,
		artifactID,
	)
	if err != nil {
		return artifact.Record{}, err
	}
	if !found {
		return artifact.Record{}, artifact.ErrInvocationArtifactMissing
	}
	return record, nil
}

func findInvocationArtifactRecord(
	ctx context.Context,
	querier operationQuerier,
	isolationDomainID string,
	artifactID string,
) (artifact.Record, bool, error) {
	var record artifact.Record
	var publicRecordMatches bool
	err := querier.QueryRow(ctx, `
		SELECT object.schema_version, object.isolation_domain_id, object.id,
		       object.invocation_id, object.operation_id, object.effect_id,
		       object.name, object.kind, object.media_type, object.size_bytes,
		       object.artifact_digest, object.sensitive, object.object_key,
		       EXISTS (
		           SELECT 1
		           FROM artifacts AS public
		           WHERE public.isolation_domain_id = object.isolation_domain_id
		             AND public.id = object.id
		             AND public.invocation_id = object.invocation_id
		             AND public.name = object.name
		             AND public.kind = object.kind
		             AND public.media_type = object.media_type
		             AND public.size_bytes = object.size_bytes
		             AND public.digest = object.artifact_digest
		             AND public.state = 'available'
		             AND public.sensitive = object.sensitive
		       )
		FROM invocation_artifact_objects AS object
		WHERE object.isolation_domain_id = $1 AND object.id = $2
	`, isolationDomainID, artifactID).Scan(
		&record.SchemaVersion,
		&record.IsolationDomainID,
		&record.ID,
		&record.InvocationID,
		&record.OperationID,
		&record.EffectID,
		&record.Name,
		&record.Kind,
		&record.MediaType,
		&record.SizeBytes,
		&record.Digest,
		&record.Sensitive,
		&record.ObjectKey,
		&publicRecordMatches,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifact.Record{}, false, nil
	}
	if err != nil {
		return artifact.Record{}, false, fmt.Errorf("read invocation artifact record: %w", err)
	}
	normalized, err := artifact.NormalizeRecord(record)
	if err != nil || !publicRecordMatches {
		return artifact.Record{}, false, artifact.ErrInvocationArtifactConflict
	}
	return normalized, true, nil
}

var _ artifact.Catalog = (*Repository)(nil)
