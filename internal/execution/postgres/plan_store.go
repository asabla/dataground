package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (store *Store) BindExecutionPlan(
	ctx context.Context,
	binding execution.ExecutionPlanBinding,
) (execution.ExecutionPlan, error) {
	normalized, err := execution.NormalizeExecutionPlanBinding(binding)
	if err != nil {
		return execution.ExecutionPlan{}, err
	}
	planDigest, err := execution.DigestExecutionPlan(normalized.Plan)
	if err != nil {
		return execution.ExecutionPlan{}, err
	}
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return execution.ExecutionPlan{}, fmt.Errorf("begin execution plan binding: %w", ErrPersistence)
	}
	defer transaction.Rollback(ctx)
	var revisionRuntimeProfile string
	var revisionCapabilities []string
	err = transaction.QueryRow(ctx, `
		SELECT runtime_profile, required_capabilities
		FROM service_revisions
		WHERE isolation_domain_id = $1 AND id = $2
		FOR SHARE
	`, normalized.Plan.IsolationDomainID, normalized.Plan.RevisionID).Scan(
		&revisionRuntimeProfile,
		&revisionCapabilities,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.ExecutionPlan{}, execution.ErrExecutionPlanRevisionMissing
	}
	if err != nil {
		return execution.ExecutionPlan{}, fmt.Errorf("read execution plan revision: %w", ErrPersistence)
	}
	revisionCapabilities, err = execution.NormalizeCapabilities(revisionCapabilities)
	if err != nil {
		return execution.ExecutionPlan{}, execution.ErrExecutionPlanRevisionMismatch
	}
	if revisionRuntimeProfile != normalized.Plan.RuntimeProfile ||
		!slices.Equal(revisionCapabilities, normalized.Plan.RequiredCapabilities) {
		return execution.ExecutionPlan{}, execution.ErrExecutionPlanRevisionMismatch
	}
	now := store.now()
	result, err := transaction.Exec(ctx, `
		INSERT INTO service_revision_execution_plans (
			isolation_domain_id, revision_id, schema_version, runtime_profile,
			environment_revision_id, image_reference, environment_manifest_digest,
			enforcement_bundle_id, enforcement_bundle_digest,
			runtime_matrix_id, runtime_matrix_digest, provider_profiles,
			required_capabilities, plan_digest, bound_by, correlation_id, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17
		)
		ON CONFLICT (isolation_domain_id, revision_id) DO NOTHING
	`,
		normalized.Plan.IsolationDomainID,
		normalized.Plan.RevisionID,
		normalized.Plan.SchemaVersion,
		normalized.Plan.RuntimeProfile,
		normalized.Plan.EnvironmentRevisionID,
		normalized.Plan.ImageReference,
		normalized.Plan.EnvironmentManifestDigest,
		normalized.Plan.EnforcementBundleID,
		normalized.Plan.EnforcementBundleDigest,
		normalized.Plan.RuntimeMatrixID,
		normalized.Plan.RuntimeMatrixDigest,
		normalized.Plan.ProviderProfiles,
		normalized.Plan.RequiredCapabilities,
		planDigest,
		normalized.ActorID,
		normalized.CorrelationID,
		now,
	)
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == "23503" {
			return execution.ExecutionPlan{}, execution.ErrExecutionPlanRevisionMissing
		}
		return execution.ExecutionPlan{}, fmt.Errorf("bind execution plan: %w", ErrPersistence)
	}
	existing, err := getExecutionPlan(ctx, transaction, normalized.Plan.IsolationDomainID, normalized.Plan.RevisionID)
	if err != nil {
		return execution.ExecutionPlan{}, err
	}
	if !execution.EqualExecutionPlans(existing, normalized.Plan) {
		return execution.ExecutionPlan{}, execution.ErrExecutionPlanConflict
	}
	if result.RowsAffected() == 1 {
		metadata, err := json.Marshal(map[string]string{"planDigest": planDigest})
		if err != nil {
			return execution.ExecutionPlan{}, fmt.Errorf("encode execution plan audit metadata: %w", ErrPersistence)
		}
		_, err = transaction.Exec(ctx, `
			INSERT INTO audit_records (
				id, isolation_domain_id, actor_id, action, resource_type,
				resource_id, outcome, correlation_id, safe_metadata, occurred_at
			) VALUES ($1, $2, $3, 'execution-plan.bind', 'service-revision', $4,
			          'succeeded', $5, $6, $7)
		`,
			identity.Derived("aud", normalized.Plan.IsolationDomainID+":"+normalized.Plan.RevisionID+":execution-plan"),
			normalized.Plan.IsolationDomainID,
			normalized.ActorID,
			normalized.Plan.RevisionID,
			normalized.CorrelationID,
			metadata,
			now,
		)
		if err != nil {
			return execution.ExecutionPlan{}, fmt.Errorf("audit execution plan binding: %w", ErrPersistence)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return execution.ExecutionPlan{}, fmt.Errorf("commit execution plan binding: %w", ErrPersistence)
	}
	return existing, nil
}

func (store *Store) GetExecutionPlan(
	ctx context.Context,
	isolationDomainID string,
	revisionID string,
) (execution.ExecutionPlan, error) {
	return getExecutionPlan(ctx, store.pool, isolationDomainID, revisionID)
}

func getExecutionPlan(
	ctx context.Context,
	querier interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	isolationDomainID string,
	revisionID string,
) (execution.ExecutionPlan, error) {
	var plan execution.ExecutionPlan
	err := querier.QueryRow(ctx, `
		SELECT schema_version, isolation_domain_id, revision_id, runtime_profile,
		       environment_revision_id, image_reference, environment_manifest_digest,
		       enforcement_bundle_id, enforcement_bundle_digest,
		       runtime_matrix_id, runtime_matrix_digest, provider_profiles,
		       required_capabilities
		FROM service_revision_execution_plans
		WHERE isolation_domain_id = $1 AND revision_id = $2
	`, isolationDomainID, revisionID).Scan(
		&plan.SchemaVersion,
		&plan.IsolationDomainID,
		&plan.RevisionID,
		&plan.RuntimeProfile,
		&plan.EnvironmentRevisionID,
		&plan.ImageReference,
		&plan.EnvironmentManifestDigest,
		&plan.EnforcementBundleID,
		&plan.EnforcementBundleDigest,
		&plan.RuntimeMatrixID,
		&plan.RuntimeMatrixDigest,
		&plan.ProviderProfiles,
		&plan.RequiredCapabilities,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.ExecutionPlan{}, execution.ErrExecutionPlanMissing
	}
	if err != nil {
		return execution.ExecutionPlan{}, fmt.Errorf("read execution plan: %w", ErrPersistence)
	}
	normalized, err := execution.NormalizeExecutionPlan(plan)
	if err != nil {
		return execution.ExecutionPlan{}, fmt.Errorf("validate persisted execution plan: %w", ErrPersistence)
	}
	return normalized, nil
}
