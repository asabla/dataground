package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"

	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	InvocationAuthorizationEntityGenerationContract = "dataground.invocation-authorization-entity-generation/v1"
	InvocationAuthorizationEntityActivationContract = "dataground.invocation-authorization-entity-activation/v1"
)

var (
	ErrInvocationAuthorizationEntityRefreshInvalid = errors.New(
		"invocation authorization entity refresh is invalid",
	)
	ErrInvocationAuthorizationEntityRefreshConflict = errors.New(
		"invocation authorization entity refresh conflicts with durable state",
	)
	ErrInvocationAuthorizationEntityRefreshUnavailable = errors.New(
		"invocation authorization entity refresh is unavailable",
	)
)

type InvocationAuthorizationEntityGeneration struct {
	Contract          string
	IsolationDomainID string
	ServiceID         string
	RevisionID        string
	Generation        int64
	EntityDigest      []byte
	Entities          []byte
	PublishedBy       string
	CorrelationID     string
	ReasonDigest      []byte
}

func (generation InvocationAuthorizationEntityGeneration) Valid() bool {
	if generation.Contract != InvocationAuthorizationEntityGenerationContract ||
		!invocationPolicyWithdrawalDomainPattern.MatchString(generation.IsolationDomainID) ||
		!invocationPolicyWithdrawalServicePattern.MatchString(generation.ServiceID) ||
		!invocationPolicyWithdrawalRevisionPattern.MatchString(generation.RevisionID) ||
		generation.Generation < 1 || generation.Generation > math.MaxInt64 ||
		len(generation.EntityDigest) != sha256.Size ||
		!validInvocationAuthorizationEntityBytes(generation.Entities) ||
		!invocationPolicyWithdrawalActorPattern.MatchString(generation.PublishedBy) ||
		!invocationPolicyWithdrawalCorrelationPattern.MatchString(generation.CorrelationID) ||
		len(generation.ReasonDigest) != sha256.Size {
		return false
	}
	digest := sha256.Sum256(generation.Entities)
	return bytes.Equal(generation.EntityDigest, digest[:])
}

type InvocationAuthorizationEntityActivation struct {
	Contract              string
	IsolationDomainID     string
	ServiceID             string
	RevisionID            string
	Generation            int64
	InstalledPolicyDigest []byte
	EffectivePolicyDigest []byte
	ActivatedBy           string
	CorrelationID         string
	ReasonDigest          []byte
}

func (activation InvocationAuthorizationEntityActivation) Valid() bool {
	return activation.Contract == InvocationAuthorizationEntityActivationContract &&
		invocationPolicyWithdrawalDomainPattern.MatchString(activation.IsolationDomainID) &&
		invocationPolicyWithdrawalServicePattern.MatchString(activation.ServiceID) &&
		invocationPolicyWithdrawalRevisionPattern.MatchString(activation.RevisionID) &&
		activation.Generation > 0 && activation.Generation <= math.MaxInt64 &&
		len(activation.InstalledPolicyDigest) == sha256.Size &&
		(len(activation.EffectivePolicyDigest) == 0 ||
			len(activation.EffectivePolicyDigest) == sha256.Size) &&
		invocationPolicyWithdrawalActorPattern.MatchString(activation.ActivatedBy) &&
		invocationPolicyWithdrawalCorrelationPattern.MatchString(activation.CorrelationID) &&
		len(activation.ReasonDigest) == sha256.Size
}

func (repository *Repository) PublishInvocationAuthorizationEntityGeneration(
	ctx context.Context,
	generation InvocationAuthorizationEntityGeneration,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !generation.Valid() {
		return ErrInvocationAuthorizationEntityRefreshInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	generation = cloneInvocationAuthorizationEntityGeneration(generation)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin invocation authorization entity publication: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockInvocationAuthorizationPolicyScope(
		ctx, tx, generation.IsolationDomainID, generation.ServiceID, generation.RevisionID,
	); err != nil {
		return err
	}
	policy, err := requireRefreshableInvocationAuthorizationPolicy(
		ctx, tx, generation.IsolationDomainID, generation.ServiceID, generation.RevisionID,
	)
	if err != nil {
		return err
	}
	if policy.Contract != "dataground.invocation-authorization-policy/v2" {
		return ErrInvocationAuthorizationEntityRefreshUnavailable
	}
	existing, exists, err := readInvocationAuthorizationEntityGeneration(
		ctx, tx, generation.IsolationDomainID, generation.ServiceID,
		generation.RevisionID, generation.Generation,
	)
	if err != nil {
		return err
	}
	if exists {
		if !sameInvocationAuthorizationEntityGeneration(existing, generation) {
			return ErrInvocationAuthorizationEntityRefreshConflict
		}
		return tx.Commit(ctx)
	}
	var currentGeneration int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(generation), 0)
		FROM invocation_authorization_entity_generations
		WHERE isolation_domain_id = $1 AND service_id = $2 AND revision_id = $3
	`, generation.IsolationDomainID, generation.ServiceID, generation.RevisionID).Scan(
		&currentGeneration,
	); err != nil {
		return fmt.Errorf("read invocation authorization entity generation: %w", err)
	}
	if generation.Generation != currentGeneration+1 {
		return ErrInvocationAuthorizationEntityRefreshConflict
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invocation_authorization_entity_generations (
			contract, isolation_domain_id, service_id, revision_id, generation,
			entity_digest, cedar_entities, published_by,
			publication_correlation_id, reason_digest
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, generation.Contract, generation.IsolationDomainID, generation.ServiceID,
		generation.RevisionID, generation.Generation, generation.EntityDigest,
		generation.Entities, generation.PublishedBy, generation.CorrelationID,
		generation.ReasonDigest); err != nil {
		return mapInvocationAuthorizationEntityRefreshWriteError(
			"publish invocation authorization entity generation", err,
		)
	}
	if err := auditInvocationAuthorizationEntityRefresh(
		ctx, tx, generation.IsolationDomainID, generation.PublishedBy,
		"invocation-authorization-entities.publish", generation.RevisionID,
		generation.CorrelationID, generation.Generation, generation.EntityDigest,
		generation.ReasonDigest, nil,
	); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit invocation authorization entity publication: %w", err)
	}
	return nil
}

func (repository *Repository) ActivateInvocationAuthorizationEntityGeneration(
	ctx context.Context,
	activation InvocationAuthorizationEntityActivation,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !activation.Valid() {
		return ErrInvocationAuthorizationEntityRefreshInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	activation = cloneInvocationAuthorizationEntityActivation(activation)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin invocation authorization entity activation: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockInvocationAuthorizationPolicyScope(
		ctx, tx, activation.IsolationDomainID, activation.ServiceID, activation.RevisionID,
	); err != nil {
		return err
	}
	policy, err := requireRefreshableInvocationAuthorizationPolicy(
		ctx, tx, activation.IsolationDomainID, activation.ServiceID, activation.RevisionID,
	)
	if err != nil {
		return err
	}
	if policy.Contract != "dataground.invocation-authorization-policy/v2" ||
		!bytes.Equal(policy.PolicyDigest, activation.InstalledPolicyDigest) {
		return ErrInvocationAuthorizationEntityRefreshConflict
	}
	generation, exists, err := readInvocationAuthorizationEntityGeneration(
		ctx, tx, activation.IsolationDomainID, activation.ServiceID,
		activation.RevisionID, activation.Generation,
	)
	if err != nil {
		return err
	}
	if !exists {
		return ErrInvocationAuthorizationEntityRefreshUnavailable
	}
	effectiveDigest := authz.InvocationAuthorizationPolicyV2Digest(
		policy.Schema, policy.Policies, generation.Entities,
	)
	if len(activation.EffectivePolicyDigest) != 0 &&
		!bytes.Equal(activation.EffectivePolicyDigest, effectiveDigest[:]) {
		return ErrInvocationAuthorizationEntityRefreshConflict
	}
	activation.EffectivePolicyDigest = append([]byte(nil), effectiveDigest[:]...)
	existing, exists, err := readInvocationAuthorizationEntityActivation(
		ctx, tx, activation.IsolationDomainID, activation.ServiceID,
		activation.RevisionID, activation.Generation,
	)
	if err != nil {
		return err
	}
	if exists {
		if !sameInvocationAuthorizationEntityActivation(existing, activation) {
			return ErrInvocationAuthorizationEntityRefreshConflict
		}
		return tx.Commit(ctx)
	}
	var currentGeneration int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(generation), 0)
		FROM invocation_authorization_entity_activations
		WHERE isolation_domain_id = $1 AND service_id = $2 AND revision_id = $3
	`, activation.IsolationDomainID, activation.ServiceID, activation.RevisionID).Scan(
		&currentGeneration,
	); err != nil {
		return fmt.Errorf("read invocation authorization entity activation: %w", err)
	}
	if activation.Generation != currentGeneration+1 {
		return ErrInvocationAuthorizationEntityRefreshConflict
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invocation_authorization_entity_activations (
			contract, isolation_domain_id, service_id, revision_id, generation,
			installed_policy_digest, effective_policy_digest, activated_by,
			activation_correlation_id, reason_digest
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, activation.Contract, activation.IsolationDomainID, activation.ServiceID,
		activation.RevisionID, activation.Generation, activation.InstalledPolicyDigest,
		activation.EffectivePolicyDigest, activation.ActivatedBy,
		activation.CorrelationID, activation.ReasonDigest); err != nil {
		return mapInvocationAuthorizationEntityRefreshWriteError(
			"activate invocation authorization entity generation", err,
		)
	}
	if err := auditInvocationAuthorizationEntityRefresh(
		ctx, tx, activation.IsolationDomainID, activation.ActivatedBy,
		"invocation-authorization-entities.activate", activation.RevisionID,
		activation.CorrelationID, activation.Generation, generation.EntityDigest,
		activation.ReasonDigest, activation.EffectivePolicyDigest,
	); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit invocation authorization entity activation: %w", err)
	}
	return nil
}

func lockInvocationAuthorizationPolicyScope(
	ctx context.Context,
	tx pgx.Tx,
	isolationDomainID string,
	serviceID string,
	revisionID string,
) error {
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended($1, 0))
	`, "invocation-authorization-policy-scope\n"+
		isolationDomainID+"\n"+serviceID+"\n"+revisionID); err != nil {
		return fmt.Errorf("lock invocation authorization policy scope: %w", err)
	}
	return nil
}

func requireRefreshableInvocationAuthorizationPolicy(
	ctx context.Context,
	querier invocationAuthorizationPolicyQuerier,
	isolationDomainID string,
	serviceID string,
	revisionID string,
) (InvocationAuthorizationPolicyRecord, error) {
	policy, err := getInvocationAuthorizationPolicyRecord(
		ctx, querier, isolationDomainID, serviceID, revisionID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvocationAuthorizationPolicyRecord{}, ErrInvocationAuthorizationEntityRefreshUnavailable
	}
	if err != nil {
		return InvocationAuthorizationPolicyRecord{}, fmt.Errorf(
			"read invocation authorization policy for entity refresh: %w", err,
		)
	}
	_, withdrawn, err := readInvocationAuthorizationPolicyWithdrawal(
		ctx, querier, isolationDomainID, serviceID, revisionID,
	)
	if err != nil {
		return InvocationAuthorizationPolicyRecord{}, err
	}
	if withdrawn {
		return InvocationAuthorizationPolicyRecord{}, ErrInvocationAuthorizationEntityRefreshUnavailable
	}
	return policy, nil
}

func readInvocationAuthorizationEntityGeneration(
	ctx context.Context,
	querier invocationAuthorizationPolicyQuerier,
	isolationDomainID string,
	serviceID string,
	revisionID string,
	generation int64,
) (InvocationAuthorizationEntityGeneration, bool, error) {
	var value InvocationAuthorizationEntityGeneration
	err := querier.QueryRow(ctx, `
		SELECT contract, isolation_domain_id, service_id, revision_id, generation,
		       entity_digest, cedar_entities, published_by,
		       publication_correlation_id, reason_digest
		FROM invocation_authorization_entity_generations
		WHERE isolation_domain_id = $1 AND service_id = $2
		  AND revision_id = $3 AND generation = $4
	`, isolationDomainID, serviceID, revisionID, generation).Scan(
		&value.Contract, &value.IsolationDomainID, &value.ServiceID, &value.RevisionID,
		&value.Generation, &value.EntityDigest, &value.Entities, &value.PublishedBy,
		&value.CorrelationID, &value.ReasonDigest,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvocationAuthorizationEntityGeneration{}, false, nil
	}
	if err != nil {
		return InvocationAuthorizationEntityGeneration{}, false,
			fmt.Errorf("read invocation authorization entity generation: %w", err)
	}
	if !value.Valid() {
		return InvocationAuthorizationEntityGeneration{}, false,
			ErrInvocationAuthorizationEntityRefreshConflict
	}
	return cloneInvocationAuthorizationEntityGeneration(value), true, nil
}

func readInvocationAuthorizationEntityActivation(
	ctx context.Context,
	querier invocationAuthorizationPolicyQuerier,
	isolationDomainID string,
	serviceID string,
	revisionID string,
	generation int64,
) (InvocationAuthorizationEntityActivation, bool, error) {
	var value InvocationAuthorizationEntityActivation
	err := querier.QueryRow(ctx, `
		SELECT contract, isolation_domain_id, service_id, revision_id, generation,
		       installed_policy_digest, effective_policy_digest, activated_by,
		       activation_correlation_id, reason_digest
		FROM invocation_authorization_entity_activations
		WHERE isolation_domain_id = $1 AND service_id = $2
		  AND revision_id = $3 AND generation = $4
	`, isolationDomainID, serviceID, revisionID, generation).Scan(
		&value.Contract, &value.IsolationDomainID, &value.ServiceID, &value.RevisionID,
		&value.Generation, &value.InstalledPolicyDigest, &value.EffectivePolicyDigest,
		&value.ActivatedBy, &value.CorrelationID, &value.ReasonDigest,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvocationAuthorizationEntityActivation{}, false, nil
	}
	if err != nil {
		return InvocationAuthorizationEntityActivation{}, false,
			fmt.Errorf("read invocation authorization entity activation: %w", err)
	}
	if !value.Valid() || len(value.EffectivePolicyDigest) != sha256.Size {
		return InvocationAuthorizationEntityActivation{}, false,
			ErrInvocationAuthorizationEntityRefreshConflict
	}
	return cloneInvocationAuthorizationEntityActivation(value), true, nil
}

func auditInvocationAuthorizationEntityRefresh(
	ctx context.Context,
	tx pgx.Tx,
	isolationDomainID string,
	actorID string,
	action string,
	revisionID string,
	correlationID string,
	generation int64,
	entityDigest []byte,
	reasonDigest []byte,
	effectivePolicyDigest []byte,
) error {
	metadata := map[string]any{
		"generation":   generation,
		"entityDigest": "sha256:" + hex.EncodeToString(entityDigest),
		"reasonDigest": "sha256:" + hex.EncodeToString(reasonDigest),
	}
	if len(effectivePolicyDigest) != 0 {
		metadata["effectivePolicyDigest"] = "sha256:" + hex.EncodeToString(effectivePolicyDigest)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES ($1, $2, $3, $4, 'service-revision', $5,
		          'accepted', $6, $7, clock_timestamp())
	`, identity.New("aud"), isolationDomainID, actorID, action, revisionID,
		correlationID, metadata); err != nil {
		return fmt.Errorf("audit invocation authorization entity refresh: %w", err)
	}
	return nil
}

func cloneInvocationAuthorizationEntityGeneration(
	value InvocationAuthorizationEntityGeneration,
) InvocationAuthorizationEntityGeneration {
	value.EntityDigest = append([]byte(nil), value.EntityDigest...)
	value.Entities = append([]byte(nil), value.Entities...)
	value.ReasonDigest = append([]byte(nil), value.ReasonDigest...)
	return value
}

func cloneInvocationAuthorizationEntityActivation(
	value InvocationAuthorizationEntityActivation,
) InvocationAuthorizationEntityActivation {
	value.InstalledPolicyDigest = append([]byte(nil), value.InstalledPolicyDigest...)
	value.EffectivePolicyDigest = append([]byte(nil), value.EffectivePolicyDigest...)
	value.ReasonDigest = append([]byte(nil), value.ReasonDigest...)
	return value
}

func sameInvocationAuthorizationEntityGeneration(
	left InvocationAuthorizationEntityGeneration,
	right InvocationAuthorizationEntityGeneration,
) bool {
	return left.Contract == right.Contract &&
		left.IsolationDomainID == right.IsolationDomainID &&
		left.ServiceID == right.ServiceID && left.RevisionID == right.RevisionID &&
		left.Generation == right.Generation && bytes.Equal(left.EntityDigest, right.EntityDigest) &&
		bytes.Equal(left.Entities, right.Entities) && left.PublishedBy == right.PublishedBy &&
		left.CorrelationID == right.CorrelationID && bytes.Equal(left.ReasonDigest, right.ReasonDigest)
}

func sameInvocationAuthorizationEntityActivation(
	left InvocationAuthorizationEntityActivation,
	right InvocationAuthorizationEntityActivation,
) bool {
	return left.Contract == right.Contract &&
		left.IsolationDomainID == right.IsolationDomainID &&
		left.ServiceID == right.ServiceID && left.RevisionID == right.RevisionID &&
		left.Generation == right.Generation &&
		bytes.Equal(left.InstalledPolicyDigest, right.InstalledPolicyDigest) &&
		bytes.Equal(left.EffectivePolicyDigest, right.EffectivePolicyDigest) &&
		left.ActivatedBy == right.ActivatedBy && left.CorrelationID == right.CorrelationID &&
		bytes.Equal(left.ReasonDigest, right.ReasonDigest)
}

func mapInvocationAuthorizationEntityRefreshWriteError(action string, err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) &&
		(databaseError.Code == "23503" || databaseError.Code == "23505" ||
			databaseError.Code == "23514" || databaseError.Code == "P0001") {
		return ErrInvocationAuthorizationEntityRefreshConflict
	}
	return fmt.Errorf("%s: %w", action, err)
}
