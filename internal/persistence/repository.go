package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/lifecycle/invocation"
	"github.com/asabla/dataground/internal/lifecycle/publication"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const idempotencyRetention = 24 * time.Hour

type Repository struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

type Idempotency struct {
	IsolationDomainID string
	Method            string
	Path              string
	Key               string
	RequestDigest     [sha256.Size]byte
}

type CommandResult struct {
	Status   int
	Body     []byte
	Replayed bool
}

type DomainError struct {
	Code      string
	Message   string
	Retryable bool
}

func (problem *DomainError) Error() string {
	return problem.Code + ": " + problem.Message
}

type CreateServiceInput struct {
	ID            string
	Name          string
	Description   string
	ActorID       string
	CorrelationID string
}

type ServiceListPage struct {
	Items   []domain.AgentService
	HasMore bool
}

type ServiceRevisionListPage struct {
	Items   []domain.ServiceRevision
	HasMore bool
}

type CreateRevisionInput struct {
	ID                   string
	ServiceID            string
	RuntimeProfile       string
	RequiredCapabilities []string
	InputSchema          map[string]any
	OutputSchema         map[string]any
	ActorID              string
	CorrelationID        string
}

type AssignAliasInput struct {
	ID              string
	ServiceID       string
	Name            string
	RevisionID      string
	ExpectedVersion *int
	ActorID         string
	CorrelationID   string
}

type AcceptPublicationInput struct {
	RevisionID      string
	ExpectedVersion int
	ActorID         string
	CorrelationID   string
	Deadline        time.Time
}

type AcceptInvocationInput struct {
	ID             string
	ServiceID      string
	Alias          string
	Input          map[string]any
	ActorID        string
	CorrelationID  string
	Deadline       time.Time
	DispatchTarget *InvocationDispatchTarget
}

type AcceptCancellationInput struct {
	InvocationID  string
	ActorID       string
	CorrelationID string
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

func (repository *Repository) Configured() bool {
	return repository != nil && repository.pool != nil
}

func (repository *Repository) Ready(ctx context.Context) error {
	return repository.pool.Ping(ctx)
}

func (repository *Repository) CreateService(
	ctx context.Context,
	idempotency Idempotency,
	input CreateServiceInput,
) (CommandResult, error) {
	return repository.execute(ctx, idempotency, func(tx pgx.Tx, now time.Time) (int, any, error) {
		service := domain.AgentService{
			Metadata:    metadata(input.ID, idempotency.IsolationDomainID, input.ActorID, now),
			Name:        input.Name,
			Description: input.Description,
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO agent_services (
				isolation_domain_id, id, name, description, generation, version,
				created_at, updated_at, created_by
			) VALUES ($1, $2, $3, $4, 1, 1, $5, $5, $6)
		`, idempotency.IsolationDomainID, input.ID, input.Name, input.Description, now, input.ActorID)
		if err != nil {
			return 0, nil, fmt.Errorf("insert agent service: %w", err)
		}
		if err := writeOutboxAndAudit(
			ctx,
			tx,
			idempotency.IsolationDomainID,
			"agent-service",
			input.ID,
			"agent-service.created",
			input.ActorID,
			input.CorrelationID,
			"accepted",
			"",
			now,
		); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, service, nil
	})
}

func (repository *Repository) ListServices(
	ctx context.Context,
	isolationDomainID string,
	beforeCreatedAt *time.Time,
	beforeID string,
	limit int,
) (ServiceListPage, error) {
	if !repository.Configured() || isolationDomainID == "" || limit < 1 || limit > 100 ||
		(beforeCreatedAt == nil) != (beforeID == "") {
		return ServiceListPage{}, errors.New("service list request is invalid")
	}
	rows, err := repository.pool.Query(ctx, `
		SELECT id, name, description, generation, version, created_at, updated_at, created_by
		FROM agent_services
		WHERE isolation_domain_id = $1
		  AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3))
		ORDER BY created_at DESC, id DESC
		LIMIT $4
	`, isolationDomainID, beforeCreatedAt, beforeID, limit+1)
	if err != nil {
		return ServiceListPage{}, fmt.Errorf("list agent services: %w", err)
	}
	defer rows.Close()
	services := make([]domain.AgentService, 0, limit+1)
	for rows.Next() {
		var service domain.AgentService
		service.Metadata.IsolationDomainID = isolationDomainID
		if err := rows.Scan(
			&service.Metadata.ID,
			&service.Name,
			&service.Description,
			&service.Metadata.Generation,
			&service.Metadata.Version,
			&service.Metadata.CreatedAt,
			&service.Metadata.UpdatedAt,
			&service.Metadata.CreatedBy,
		); err != nil {
			return ServiceListPage{}, fmt.Errorf("scan agent service: %w", err)
		}
		services = append(services, service)
	}
	if err := rows.Err(); err != nil {
		return ServiceListPage{}, fmt.Errorf("iterate agent services: %w", err)
	}
	hasMore := len(services) > limit
	if hasMore {
		services = services[:limit]
	}
	return ServiceListPage{Items: services, HasMore: hasMore}, nil
}

func (repository *Repository) ListServiceRevisions(
	ctx context.Context,
	isolationDomainID string,
	serviceID string,
	beforeRevisionNumber *int,
	beforeID string,
	limit int,
) (ServiceRevisionListPage, error) {
	if !repository.Configured() || isolationDomainID == "" || serviceID == "" ||
		limit < 1 || limit > 100 || (beforeRevisionNumber == nil) != (beforeID == "") {
		return ServiceRevisionListPage{}, errors.New("service revision list request is invalid")
	}
	var serviceExists bool
	if err := repository.pool.QueryRow(ctx, `
		SELECT true
		FROM agent_services
		WHERE isolation_domain_id = $1 AND id = $2
	`, isolationDomainID, serviceID).Scan(&serviceExists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ServiceRevisionListPage{}, &DomainError{
				Code: "RESOURCE_NOT_FOUND", Message: "Agent service was not found.",
			}
		}
		return ServiceRevisionListPage{}, fmt.Errorf("read agent service for revision list: %w", err)
	}
	rows, err := repository.pool.Query(ctx, `
		SELECT id, revision_number, state, runtime_profile, required_capabilities,
		       input_schema, output_schema, published_at, generation, version,
		       created_at, updated_at, created_by
		FROM service_revisions
		WHERE isolation_domain_id = $1
		  AND service_id = $2
		  AND ($3::bigint IS NULL OR (revision_number, id) < ($3, $4))
		ORDER BY revision_number DESC, id DESC
		LIMIT $5
	`, isolationDomainID, serviceID, beforeRevisionNumber, beforeID, limit+1)
	if err != nil {
		return ServiceRevisionListPage{}, fmt.Errorf("list service revisions: %w", err)
	}
	defer rows.Close()
	revisions := make([]domain.ServiceRevision, 0, limit+1)
	for rows.Next() {
		var revision domain.ServiceRevision
		var inputSchema, outputSchema []byte
		revision.Metadata.IsolationDomainID = isolationDomainID
		revision.ServiceID = serviceID
		if err := rows.Scan(
			&revision.Metadata.ID,
			&revision.RevisionNumber,
			&revision.State,
			&revision.RuntimeProfile,
			&revision.RequiredCapabilities,
			&inputSchema,
			&outputSchema,
			&revision.PublishedAt,
			&revision.Metadata.Generation,
			&revision.Metadata.Version,
			&revision.Metadata.CreatedAt,
			&revision.Metadata.UpdatedAt,
			&revision.Metadata.CreatedBy,
		); err != nil {
			return ServiceRevisionListPage{}, fmt.Errorf("scan service revision: %w", err)
		}
		if len(inputSchema) > 0 {
			if err := json.Unmarshal(inputSchema, &revision.InputSchema); err != nil {
				return ServiceRevisionListPage{}, fmt.Errorf("decode service revision input schema: %w", err)
			}
		}
		if len(outputSchema) > 0 {
			if err := json.Unmarshal(outputSchema, &revision.OutputSchema); err != nil {
				return ServiceRevisionListPage{}, fmt.Errorf("decode service revision output schema: %w", err)
			}
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return ServiceRevisionListPage{}, fmt.Errorf("iterate service revisions: %w", err)
	}
	hasMore := len(revisions) > limit
	if hasMore {
		revisions = revisions[:limit]
	}
	return ServiceRevisionListPage{Items: revisions, HasMore: hasMore}, nil
}

func (repository *Repository) CreateRevision(
	ctx context.Context,
	idempotency Idempotency,
	input CreateRevisionInput,
) (CommandResult, error) {
	return repository.execute(ctx, idempotency, func(tx pgx.Tx, now time.Time) (int, any, error) {
		var serviceExists bool
		if err := tx.QueryRow(ctx, `
			SELECT true
			FROM agent_services
			WHERE isolation_domain_id = $1 AND id = $2
			FOR UPDATE
		`, idempotency.IsolationDomainID, input.ServiceID).Scan(&serviceExists); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, nil, &DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Agent service was not found."}
			}
			return 0, nil, fmt.Errorf("lock agent service: %w", err)
		}
		var revisionNumber int
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(max(revision_number), 0) + 1
			FROM service_revisions
			WHERE isolation_domain_id = $1 AND service_id = $2
		`, idempotency.IsolationDomainID, input.ServiceID).Scan(&revisionNumber); err != nil {
			return 0, nil, fmt.Errorf("allocate service revision: %w", err)
		}
		inputSchema, err := marshalNullable(input.InputSchema)
		if err != nil {
			return 0, nil, fmt.Errorf("encode input schema: %w", err)
		}
		outputSchema, err := marshalNullable(input.OutputSchema)
		if err != nil {
			return 0, nil, fmt.Errorf("encode output schema: %w", err)
		}
		requiredCapabilities := make([]string, len(input.RequiredCapabilities))
		copy(requiredCapabilities, input.RequiredCapabilities)
		_, err = tx.Exec(ctx, `
			INSERT INTO service_revisions (
				isolation_domain_id, id, service_id, revision_number, state,
				runtime_profile, required_capabilities, input_schema, output_schema,
				generation, version, created_at, updated_at, created_by
			) VALUES ($1, $2, $3, $4, 'draft', $5, $6, $7, $8, 1, 1, $9, $9, $10)
		`,
			idempotency.IsolationDomainID,
			input.ID,
			input.ServiceID,
			revisionNumber,
			input.RuntimeProfile,
			requiredCapabilities,
			inputSchema,
			outputSchema,
			now,
			input.ActorID,
		)
		if err != nil {
			return 0, nil, fmt.Errorf("insert service revision: %w", err)
		}
		revision := domain.ServiceRevision{
			Metadata:             metadata(input.ID, idempotency.IsolationDomainID, input.ActorID, now),
			ServiceID:            input.ServiceID,
			RevisionNumber:       revisionNumber,
			State:                "draft",
			RuntimeProfile:       input.RuntimeProfile,
			RequiredCapabilities: requiredCapabilities,
			InputSchema:          input.InputSchema,
			OutputSchema:         input.OutputSchema,
		}
		if err := writeOutboxAndAudit(
			ctx, tx, idempotency.IsolationDomainID, "service-revision", input.ID,
			"service-revision.created", input.ActorID, input.CorrelationID, "accepted", "", now,
		); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, revision, nil
	})
}

func (repository *Repository) AcceptPublication(
	ctx context.Context,
	idempotency Idempotency,
	input AcceptPublicationInput,
	supportedCapabilities map[string]string,
) (CommandResult, error) {
	return repository.execute(ctx, idempotency, func(tx pgx.Tx, now time.Time) (int, any, error) {
		revision, err := getRevisionForUpdate(ctx, tx, idempotency.IsolationDomainID, input.RevisionID)
		if err != nil {
			return 0, nil, err
		}
		if revision.Metadata.Version != input.ExpectedVersion {
			return 0, nil, &DomainError{Code: "VERSION_CONFLICT", Message: "Revision version did not match."}
		}
		if revision.State != "draft" {
			return 0, nil, &DomainError{Code: "REVISION_IMMUTABLE", Message: "Only a draft revision can be published."}
		}
		if revision.RuntimeProfile != "reference/v1" {
			return 0, nil, &DomainError{Code: "RUNTIME_PROFILE_UNAVAILABLE", Message: "Runtime profile is unavailable."}
		}
		for _, capability := range revision.RequiredCapabilities {
			if supportedCapabilities[capability] != "supported" {
				return 0, nil, &DomainError{Code: "REQUIRED_CAPABILITY_UNAVAILABLE", Message: "A required runtime capability is unavailable."}
			}
		}

		if problem := domain.ValidateRevisionSchemas(revision.InputSchema, revision.OutputSchema); problem != nil {
			return 0, nil, &DomainError{Code: problem.Code, Message: problem.Message}
		}

		operationID := identity.Derived("op", idempotency.IsolationDomainID+":"+input.RevisionID+":publication:v1")
		insertResult, err := tx.Exec(ctx, `
			INSERT INTO service_publication_operations (
				isolation_domain_id, id, revision_id, command, desired_state, observed_state,
				state_machine_version, generation, attempt, lease_token, due_at, deadline_at,
				correlation_id, actor_id, last_transition_at, created_at, updated_at
			) VALUES (
				$1, $2, $3, 'publish', 'published', 'queued', $4, 1, 0, 0, $5, $6,
				$7, $8, $5, $5, $5
			)
			ON CONFLICT (isolation_domain_id, revision_id) DO NOTHING
		`,
			idempotency.IsolationDomainID,
			operationID,
			input.RevisionID,
			publication.StateMachineVersion,
			now,
			input.Deadline,
			input.CorrelationID,
			input.ActorID,
		)
		if err != nil {
			return 0, nil, fmt.Errorf("accept service publication: %w", err)
		}
		operation, err := getPublicationOperation(ctx, tx, idempotency.IsolationDomainID, operationID)
		if err != nil {
			return 0, nil, err
		}
		if insertResult.RowsAffected() > 0 {
			if err := writeOutboxAndAudit(
				ctx, tx, idempotency.IsolationDomainID, "service-publication", operationID,
				"service-publication.accepted", input.ActorID, input.CorrelationID, "accepted", operationID, now,
			); err != nil {
				return 0, nil, err
			}
		}
		return http.StatusAccepted, operation, nil
	})
}

func (repository *Repository) AssignAlias(
	ctx context.Context,
	idempotency Idempotency,
	input AssignAliasInput,
) (CommandResult, error) {
	return repository.execute(ctx, idempotency, func(tx pgx.Tx, now time.Time) (int, any, error) {
		var revisionServiceID, revisionState string
		if err := tx.QueryRow(ctx, `
			SELECT service_id, state
			FROM service_revisions
			WHERE isolation_domain_id = $1 AND id = $2
 FOR SHARE
		`, idempotency.IsolationDomainID, input.RevisionID).Scan(&revisionServiceID, &revisionState); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, nil, &DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Published service revision was not found."}
			}
			return 0, nil, fmt.Errorf("read alias target: %w", err)
		}
		if revisionServiceID != input.ServiceID || revisionState != "published" {
			return 0, nil, &DomainError{Code: "REVISION_NOT_PUBLISHED", Message: "Alias target must be a published revision of this service."}
		}
		var alias domain.ServiceAlias
		err := scanAlias(tx.QueryRow(ctx, `
			SELECT isolation_domain_id, id, service_id, name, revision_id,
			       generation, version, created_at, updated_at, created_by, withdrawn_at
			FROM service_aliases
			WHERE isolation_domain_id = $1 AND service_id = $2 AND name = $3
			FOR UPDATE
		`, idempotency.IsolationDomainID, input.ServiceID, input.Name), &alias)
		switch {
		case err == nil:
			requiredVersion := alias.Metadata.Version
			if alias.WithdrawnAt != nil {
				requiredVersion = 0
			}
			if (input.ExpectedVersion == nil && requiredVersion != 0) || (input.ExpectedVersion != nil && *input.ExpectedVersion != requiredVersion) {
				return 0, nil, &DomainError{Code: "VERSION_CONFLICT", Message: "Alias version did not match."}
			}
			alias.RevisionID = input.RevisionID
			alias.WithdrawnAt = nil
			alias.Metadata.Generation++
			alias.Metadata.Version++
			alias.Metadata.UpdatedAt = now
			_, err = tx.Exec(ctx, `
				UPDATE service_aliases
				SET revision_id = $4, withdrawn_at = NULL, generation = generation + 1, version = version + 1, updated_at = $5
				WHERE isolation_domain_id = $1 AND service_id = $2 AND name = $3
			`, idempotency.IsolationDomainID, input.ServiceID, input.Name, input.RevisionID, now)
		case errors.Is(err, pgx.ErrNoRows):
			if input.ExpectedVersion != nil && *input.ExpectedVersion != 0 {
				return 0, nil, &DomainError{Code: "VERSION_CONFLICT", Message: "A new alias expects version zero."}
			}
			alias = domain.ServiceAlias{
				Metadata:   metadata(input.ID, idempotency.IsolationDomainID, input.ActorID, now),
				ServiceID:  input.ServiceID,
				Name:       input.Name,
				RevisionID: input.RevisionID,
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO service_aliases (
					isolation_domain_id, id, service_id, name, revision_id,
					generation, version, created_at, updated_at, created_by
				) VALUES ($1, $2, $3, $4, $5, 1, 1, $6, $6, $7)
			`, idempotency.IsolationDomainID, input.ID, input.ServiceID, input.Name, input.RevisionID, now, input.ActorID)
		default:
			return 0, nil, fmt.Errorf("read service alias: %w", err)
		}
		if err != nil {
			return 0, nil, fmt.Errorf("write service alias: %w", err)
		}
		if err := writeOutboxAndAudit(
			ctx, tx, idempotency.IsolationDomainID, "service-alias", alias.Metadata.ID,
			"service-alias.assigned", input.ActorID, input.CorrelationID, "accepted", "", now,
		); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, alias, nil
	})
}

func (repository *Repository) GetServiceAlias(
	ctx context.Context,
	isolationDomainID string,
	serviceID string,
	name string,
) (domain.ServiceAlias, error) {
	if !repository.Configured() || isolationDomainID == "" || serviceID == "" || name == "" {
		return domain.ServiceAlias{}, errors.New("service alias read request is invalid")
	}
	var serviceExists bool
	if err := repository.pool.QueryRow(ctx, `
		SELECT true
		FROM agent_services
		WHERE isolation_domain_id = $1 AND id = $2
	`, isolationDomainID, serviceID).Scan(&serviceExists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ServiceAlias{}, &DomainError{
				Code: "RESOURCE_NOT_FOUND", Message: "Agent service was not found.",
			}
		}
		return domain.ServiceAlias{}, fmt.Errorf("read agent service for alias: %w", err)
	}
	var alias domain.ServiceAlias
	if err := scanAlias(repository.pool.QueryRow(ctx, `
		SELECT isolation_domain_id, id, service_id, name, revision_id,
		       generation, version, created_at, updated_at, created_by, withdrawn_at
		FROM service_aliases
		WHERE isolation_domain_id = $1 AND service_id = $2 AND name = $3 AND withdrawn_at IS NULL
	`, isolationDomainID, serviceID, name), &alias); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ServiceAlias{}, &DomainError{
				Code: "SERVICE_ALIAS_NOT_FOUND", Message: "Service alias was not found.",
			}
		}
		return domain.ServiceAlias{}, fmt.Errorf("read service alias: %w", err)
	}
	return alias, nil
}

func (repository *Repository) AcceptInvocation(
	ctx context.Context,
	idempotency Idempotency,
	input AcceptInvocationInput,
) (CommandResult, error) {
	if input.DispatchTarget != nil && !input.DispatchTarget.Valid() {
		return CommandResult{}, errors.New("invocation dispatch target is invalid")
	}
	if input.DispatchTarget != nil {
		clonedTarget := *input.DispatchTarget
		input.DispatchTarget = &clonedTarget
	}
	return repository.execute(ctx, idempotency, func(tx pgx.Tx, now time.Time) (int, any, error) {
		if input.DispatchTarget != nil &&
			(idempotency.IsolationDomainID != input.DispatchTarget.IsolationDomainID ||
				input.ServiceID != input.DispatchTarget.ServiceID) {
			return 0, nil, invocationDispatchMismatch()
		}
		var revisionID, runtimeProfile string
		var inputSchemaJSON []byte
		if err := tx.QueryRow(ctx, `
			SELECT alias.revision_id, revision.runtime_profile, revision.input_schema
			FROM service_aliases AS alias
			JOIN service_revisions AS revision
			  ON revision.isolation_domain_id = alias.isolation_domain_id
			 AND revision.id = alias.revision_id
			 AND revision.service_id = alias.service_id
			WHERE alias.isolation_domain_id = $1
			  AND alias.service_id = $2
			  AND alias.name = $3
			  AND alias.withdrawn_at IS NULL
			  AND revision.state = 'published'
			FOR SHARE OF alias, revision
		`, idempotency.IsolationDomainID, input.ServiceID, input.Alias).Scan(&revisionID, &runtimeProfile, &inputSchemaJSON); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, nil, &DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Published service alias was not found."}
			}
			return 0, nil, fmt.Errorf("resolve service alias: %w", err)
		}
		if input.DispatchTarget != nil &&
			(revisionID != input.DispatchTarget.RevisionID ||
				runtimeProfile != input.DispatchTarget.RuntimeProfile) {
			return 0, nil, invocationDispatchMismatch()
		}
		var inputSchema map[string]any
		if len(inputSchemaJSON) > 0 && json.Unmarshal(inputSchemaJSON, &inputSchema) != nil {
			return 0, nil, &DomainError{Code: "REVISION_INPUT_SCHEMA_INVALID", Message: "The service revision input contract cannot be validated."}
		}
		if problem := domain.ValidateInvocationInput(inputSchema, input.Input); problem != nil {
			return 0, nil, &DomainError{Code: problem.Code, Message: problem.Message}
		}
		encodedInput, err := json.Marshal(input.Input)
		if err != nil {
			return 0, nil, fmt.Errorf("encode invocation input: %w", err)
		}
		operationID := identity.Derived("op", idempotency.IsolationDomainID+":"+input.ID+":invocation:v1")
		_, err = tx.Exec(ctx, `
			INSERT INTO invocations (
				isolation_domain_id, id, service_id, revision_id, alias, state, input,
				correlation_id, operation_id, generation, version, created_at, updated_at, created_by
			) VALUES ($1, $2, $3, $4, $5, 'accepted', $6, $7, $8, 1, 1, $9, $9, $10)
		`,
			idempotency.IsolationDomainID, input.ID, input.ServiceID, revisionID, input.Alias,
			encodedInput, input.CorrelationID, operationID, now, input.ActorID,
		)
		if err != nil {
			return 0, nil, fmt.Errorf("insert invocation: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO invocation_execution_operations (
				isolation_domain_id, id, invocation_id, command, desired_state, observed_state,
				state_machine_version, generation, attempt, lease_token, due_at, deadline_at,
				correlation_id, actor_id, last_transition_at, created_at, updated_at
			) VALUES (
				$1, $2, $3, 'invoke', 'succeeded', 'queued', $4, 1, 0, 0, $5, $6,
				$7, $8, $5, $5, $5
			)
		`,
			idempotency.IsolationDomainID, operationID, input.ID, invocation.StateMachineVersion,
			now, input.Deadline, input.CorrelationID, input.ActorID,
		)
		if err != nil {
			return 0, nil, fmt.Errorf("accept invocation execution: %w", err)
		}
		accepted := domain.Invocation{
			Metadata:      metadata(input.ID, idempotency.IsolationDomainID, input.ActorID, now),
			ServiceID:     input.ServiceID,
			RevisionID:    revisionID,
			Alias:         input.Alias,
			State:         "accepted",
			Input:         input.Input,
			CorrelationID: input.CorrelationID,
			OperationID:   operationID,
			ArtifactIDs:   []string{},
		}
		if err := writeOutboxAndAudit(
			ctx, tx, idempotency.IsolationDomainID, "invocation", input.ID,
			"invocation.accepted", input.ActorID, input.CorrelationID, "accepted", operationID, now,
		); err != nil {
			return 0, nil, err
		}
		if err := writeInvocationEvent(
			ctx, tx, accepted, "lifecycle.accepted", input.ActorID,
			map[string]any{"state": "accepted"}, now,
		); err != nil {
			return 0, nil, err
		}
		return http.StatusAccepted, accepted, nil
	})
}

func (repository *Repository) AcceptCancellation(
	ctx context.Context,
	idempotency Idempotency,
	input AcceptCancellationInput,
) (CommandResult, error) {
	if input.ActorID == "" || input.CorrelationID == "" {
		return CommandResult{}, errors.New("cancellation actor and correlation are required")
	}
	return repository.execute(ctx, idempotency, func(tx pgx.Tx, now time.Time) (int, any, error) {
		invocationValue, err := getInvocationForUpdate(ctx, tx, idempotency.IsolationDomainID, input.InvocationID)
		if err != nil {
			return 0, nil, err
		}
		if invocationValue.State == "cancelled" {
			return http.StatusOK, invocationValue, nil
		}
		if invocationValue.State == "cancelling" {
			return http.StatusAccepted, invocationValue, nil
		}
		if invocationValue.State == "succeeded" || invocationValue.State == "failed" {
			return 0, nil, &DomainError{Code: "INVOCATION_TERMINAL", Message: "A completed invocation cannot be cancelled."}
		}
		_, err = tx.Exec(ctx, `
			UPDATE invocations
			SET state = 'cancelling', generation = generation + 1, version = version + 1,
			    updated_at = $3
			WHERE isolation_domain_id = $1 AND id = $2
		`, idempotency.IsolationDomainID, input.InvocationID, now)
		if err != nil {
			return 0, nil, fmt.Errorf("mark invocation cancelling: %w", err)
		}
		_, err = tx.Exec(ctx, `
			UPDATE invocation_execution_operations
			SET command = 'cancel', desired_state = 'cancelled', observed_state = 'cancelling',
			    generation = generation + 1, due_at = $3,
			    effect_actor_id = $4, effect_correlation_id = $5,
			    lease_owner = NULL, lease_expires_at = NULL,
			    last_transition_at = $3, updated_at = $3
			WHERE isolation_domain_id = $1 AND invocation_id = $2
		`, idempotency.IsolationDomainID, input.InvocationID, now, input.ActorID, input.CorrelationID)
		if err != nil {
			return 0, nil, fmt.Errorf("accept invocation cancellation: %w", err)
		}
		invocationValue.State = "cancelling"
		invocationValue.Metadata.Generation++
		invocationValue.Metadata.Version++
		invocationValue.Metadata.UpdatedAt = now
		if err := writeOutboxAndAudit(
			ctx, tx, idempotency.IsolationDomainID, "invocation", input.InvocationID,
			"invocation.cancellation-accepted", input.ActorID, input.CorrelationID,
			"accepted", invocationValue.OperationID, now,
		); err != nil {
			return 0, nil, err
		}
		if err := writeInvocationEvent(
			ctx, tx, invocationValue, "lifecycle.cancellation.requested", input.ActorID,
			map[string]any{"state": "cancelling"}, now,
		); err != nil {
			return 0, nil, err
		}
		return http.StatusAccepted, invocationValue, nil
	})
}

func (repository *Repository) GetInvocation(
	ctx context.Context,
	isolationDomainID string,
	invocationID string,
) (domain.Invocation, error) {
	return scanInvocation(repository.pool.QueryRow(ctx, invocationSelect+`
		WHERE invocation.isolation_domain_id = $1 AND invocation.id = $2
		GROUP BY invocation.isolation_domain_id, invocation.id
	`, isolationDomainID, invocationID))
}

func (repository *Repository) GetOperation(
	ctx context.Context,
	isolationDomainID string,
	operationID string,
) (domain.Operation, error) {
	operation, err := getPublicationOperation(ctx, repository.pool, isolationDomainID, operationID)
	if err == nil {
		return operation, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Operation{}, err
	}
	return getInvocationOperation(ctx, repository.pool, isolationDomainID, operationID)
}

func (repository *Repository) ListEvents(
	ctx context.Context,
	isolationDomainID string,
	invocationID string,
	after uint64,
) ([]domain.EventEnvelope, error) {
	return repository.listEvents(ctx, isolationDomainID, invocationID, after, 0)
}

// ListEventsBounded reads at most limit records, allowing the HTTP boundary to
// request one extra record to determine whether another replay page remains.
func (repository *Repository) ListEventsBounded(ctx context.Context, isolationDomainID, invocationID string, after uint64, limit int) ([]domain.EventEnvelope, error) {
	if limit < 1 || limit > 501 {
		return nil, errors.New("event replay record limit is invalid")
	}
	return repository.listEvents(ctx, isolationDomainID, invocationID, after, limit)
}

func (repository *Repository) listEvents(ctx context.Context, isolationDomainID, invocationID string, after uint64, limit int) ([]domain.EventEnvelope, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT schema_version, id, isolation_domain_id, invocation_id, sequence,
		       event_type, COALESCE(source_kind, 'platform'), occurred_at, recorded_at, correlation_id, actor_id,
		       service_id, revision_id, payload, extensions
		FROM invocation_events
		WHERE isolation_domain_id = $1 AND invocation_id = $2 AND sequence > $3
		ORDER BY sequence
        LIMIT NULLIF($4, 0)
	`, isolationDomainID, invocationID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("query invocation events: %w", err)
	}
	defer rows.Close()
	var events []domain.EventEnvelope
	for rows.Next() {
		var event domain.EventEnvelope
		var payload, extensions []byte
		if err := rows.Scan(
			&event.SchemaVersion, &event.ID, &event.IsolationDomainID, &event.InvocationID,
			&event.Sequence, &event.Type, &event.Source, &event.OccurredAt, &event.RecordedAt,
			&event.CorrelationID, &event.ActorID, &event.ServiceID, &event.RevisionID,
			&payload, &extensions,
		); err != nil {
			return nil, fmt.Errorf("scan invocation event: %w", err)
		}
		if err := json.Unmarshal(payload, &event.Payload); err != nil {
			return nil, fmt.Errorf("decode invocation event payload: %w", err)
		}
		if len(extensions) > 0 {
			if err := json.Unmarshal(extensions, &event.Extensions); err != nil {
				return nil, fmt.Errorf("decode invocation event extensions: %w", err)
			}
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate invocation events: %w", err)
	}
	if events == nil {
		var exists bool
		if err := repository.pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM invocations WHERE isolation_domain_id = $1 AND id = $2)
		`, isolationDomainID, invocationID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("inspect invocation for event replay: %w", err)
		}
		if !exists {
			return nil, pgx.ErrNoRows
		}
	}
	return events, nil
}

func (repository *Repository) GetArtifact(
	ctx context.Context,
	isolationDomainID string,
	invocationID string,
	artifactID string,
) (domain.ArtifactDescriptor, error) {
	var artifact domain.ArtifactDescriptor
	err := repository.pool.QueryRow(ctx, `
		SELECT isolation_domain_id, id, invocation_id, name, kind, media_type,
		       size_bytes, digest, state, sensitive, generation, version,
		       created_at, updated_at, created_by
		FROM artifacts
		WHERE isolation_domain_id = $1 AND invocation_id = $2 AND id = $3
	`, isolationDomainID, invocationID, artifactID).Scan(
		&artifact.Metadata.IsolationDomainID, &artifact.Metadata.ID, &artifact.InvocationID,
		&artifact.Name, &artifact.Kind, &artifact.MediaType, &artifact.SizeBytes,
		&artifact.Digest, &artifact.State, &artifact.Sensitive,
		&artifact.Metadata.Generation, &artifact.Metadata.Version,
		&artifact.Metadata.CreatedAt, &artifact.Metadata.UpdatedAt, &artifact.Metadata.CreatedBy,
	)
	if err != nil {
		return domain.ArtifactDescriptor{}, err
	}
	return artifact, nil
}

func (repository *Repository) execute(
	ctx context.Context,
	idempotency Idempotency,
	action func(pgx.Tx, time.Time) (int, any, error),
) (CommandResult, error) {
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin command transaction: %w", err)
	}
	defer transaction.Rollback(ctx)
	now := repository.now()
	result, err := transaction.Exec(ctx, `
		INSERT INTO idempotency_records (
			isolation_domain_id, method, request_path, idempotency_key,
			request_digest, created_at, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT DO NOTHING
	`,
		idempotency.IsolationDomainID,
		idempotency.Method,
		idempotency.Path,
		idempotency.Key,
		idempotency.RequestDigest[:],
		now,
		now.Add(idempotencyRetention),
	)
	if err != nil {
		return CommandResult{}, fmt.Errorf("reserve idempotency key: %w", err)
	}
	if result.RowsAffected() == 0 {
		var requestDigest, responseBody []byte
		var responseStatus pgtype.Int4
		if err := transaction.QueryRow(ctx, `
			SELECT request_digest, response_status, response_body
			FROM idempotency_records
			WHERE isolation_domain_id = $1 AND method = $2 AND request_path = $3 AND idempotency_key = $4
		`,
			idempotency.IsolationDomainID,
			idempotency.Method,
			idempotency.Path,
			idempotency.Key,
		).Scan(&requestDigest, &responseStatus, &responseBody); err != nil {
			return CommandResult{}, fmt.Errorf("read idempotency result: %w", err)
		}
		if !bytes.Equal(requestDigest, idempotency.RequestDigest[:]) {
			return CommandResult{}, &DomainError{Code: "IDEMPOTENCY_KEY_REUSED", Message: "Idempotency key was reused with a different request."}
		}
		if !responseStatus.Valid || responseBody == nil {
			return CommandResult{}, &DomainError{Code: "COMMAND_IN_PROGRESS", Message: "The original command is still being committed.", Retryable: true}
		}
		if err := transaction.Commit(ctx); err != nil {
			return CommandResult{}, fmt.Errorf("finish idempotency replay: %w", err)
		}
		return CommandResult{Status: int(responseStatus.Int32), Body: responseBody, Replayed: true}, nil
	}

	status, value, err := action(transaction, now)
	if err != nil {
		return CommandResult{}, err
	}
	body, err := json.Marshal(value)
	if err != nil {
		return CommandResult{}, fmt.Errorf("encode command result: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE idempotency_records
		SET response_status = $5, response_body = $6, completed_at = $7
		WHERE isolation_domain_id = $1 AND method = $2 AND request_path = $3 AND idempotency_key = $4
	`,
		idempotency.IsolationDomainID,
		idempotency.Method,
		idempotency.Path,
		idempotency.Key,
		status,
		body,
		now,
	); err != nil {
		return CommandResult{}, fmt.Errorf("complete idempotency record: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return CommandResult{}, fmt.Errorf("commit command: %w", err)
	}
	return CommandResult{Status: status, Body: body}, nil
}

func writeOutboxAndAudit(
	ctx context.Context,
	tx pgx.Tx,
	isolationDomainID string,
	resourceType string,
	resourceID string,
	eventType string,
	actorID string,
	correlationID string,
	outcome string,
	operationID string,
	now time.Time,
) error {
	outboxID := identity.Derived("out", isolationDomainID+":"+eventType+":"+resourceID+":"+correlationID)
	payload, err := json.Marshal(map[string]any{
		"resourceType": resourceType,
		"resourceId":   resourceID,
		"outcome":      outcome,
	})
	if err != nil {
		return fmt.Errorf("encode safe outbox payload: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO outbox_events (
			id, isolation_domain_id, aggregate_type, aggregate_id, event_type,
			payload, correlation_id, available_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
		ON CONFLICT (id) DO NOTHING
	`, outboxID, isolationDomainID, resourceType, resourceID, eventType, payload, correlationID, now); err != nil {
		return fmt.Errorf("write outbox event: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, operation_id, safe_metadata, occurred_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), '{}', $10)
	`,
		identity.New("aud"), isolationDomainID, actorID, eventType, resourceType, resourceID,
		outcome, correlationID, operationID, now,
	); err != nil {
		return fmt.Errorf("write audit record: %w", err)
	}
	return nil
}

func writeInvocationEvent(
	ctx context.Context,
	tx pgx.Tx,
	invocationValue domain.Invocation,
	eventType string,
	actorID string,
	payload map[string]any,
	now time.Time,
) error {
	var sequence uint64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(sequence), 0) + 1
		FROM invocation_events
		WHERE isolation_domain_id = $1 AND invocation_id = $2
	`, invocationValue.Metadata.IsolationDomainID, invocationValue.Metadata.ID).Scan(&sequence); err != nil {
		return fmt.Errorf("allocate invocation event sequence: %w", err)
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode invocation event: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO invocation_events (
			isolation_domain_id, invocation_id, id, sequence, schema_version,
			event_type, occurred_at, recorded_at, correlation_id, actor_id,
			service_id, revision_id, payload
		) VALUES ($1, $2, $3, $4, 'dataground.event/v1', $5, $6, $6, $7, $8, $9, $10, $11)
	`, invocationValue.Metadata.IsolationDomainID, invocationValue.Metadata.ID,
		identity.Derived("evt", invocationValue.Metadata.ID+":"+fmt.Sprint(sequence)), sequence,
		eventType, now, invocationValue.CorrelationID, actorID,
		invocationValue.ServiceID, invocationValue.RevisionID, encodedPayload,
	)
	if err != nil {
		return fmt.Errorf("persist invocation event: %w", err)
	}
	return nil
}

func metadata(id, isolationDomainID, actorID string, now time.Time) domain.ResourceMetadata {
	return domain.ResourceMetadata{
		ID:                id,
		IsolationDomainID: isolationDomainID,
		Generation:        1,
		Version:           1,
		CreatedAt:         now,
		UpdatedAt:         now,
		CreatedBy:         actorID,
	}
}

func marshalNullable(value map[string]any) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	return json.Marshal(value)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAlias(row rowScanner, alias *domain.ServiceAlias) error {
	return row.Scan(
		&alias.Metadata.IsolationDomainID,
		&alias.Metadata.ID,
		&alias.ServiceID,
		&alias.Name,
		&alias.RevisionID,
		&alias.Metadata.Generation,
		&alias.Metadata.Version,
		&alias.Metadata.CreatedAt,
		&alias.Metadata.UpdatedAt,
		&alias.Metadata.CreatedBy,
		&alias.WithdrawnAt,
	)
}

const invocationSelect = `
	SELECT invocation.isolation_domain_id, invocation.id, invocation.service_id,
	       invocation.revision_id, invocation.alias, invocation.state, invocation.input,
	       invocation.result, invocation.error, invocation.usage, invocation.correlation_id,
	       invocation.operation_id, invocation.completed_at, invocation.generation,
	       invocation.version, invocation.created_at, invocation.updated_at, invocation.created_by,
	       COALESCE(array_agg(artifact.id) FILTER (WHERE artifact.id IS NOT NULL), '{}')
	FROM invocations AS invocation
	LEFT JOIN artifacts AS artifact
	  ON artifact.isolation_domain_id = invocation.isolation_domain_id
	 AND artifact.invocation_id = invocation.id
`

func scanInvocation(row rowScanner) (domain.Invocation, error) {
	var value domain.Invocation
	var encodedInput, encodedResult, encodedError, encodedUsage []byte
	err := row.Scan(
		&value.Metadata.IsolationDomainID,
		&value.Metadata.ID,
		&value.ServiceID,
		&value.RevisionID,
		&value.Alias,
		&value.State,
		&encodedInput,
		&encodedResult,
		&encodedError,
		&encodedUsage,
		&value.CorrelationID,
		&value.OperationID,
		&value.CompletedAt,
		&value.Metadata.Generation,
		&value.Metadata.Version,
		&value.Metadata.CreatedAt,
		&value.Metadata.UpdatedAt,
		&value.Metadata.CreatedBy,
		&value.ArtifactIDs,
	)
	if err != nil {
		return domain.Invocation{}, err
	}
	if err := json.Unmarshal(encodedInput, &value.Input); err != nil {
		return domain.Invocation{}, fmt.Errorf("decode invocation input: %w", err)
	}
	if len(encodedResult) > 0 {
		if err := json.Unmarshal(encodedResult, &value.Result); err != nil {
			return domain.Invocation{}, fmt.Errorf("decode invocation result: %w", err)
		}
	}
	if len(encodedError) > 0 {
		value.Error = &domain.APIError{}
		if err := json.Unmarshal(encodedError, value.Error); err != nil {
			return domain.Invocation{}, fmt.Errorf("decode invocation error: %w", err)
		}
	}
	if len(encodedUsage) > 0 {
		value.Usage = &domain.Usage{}
		if err := json.Unmarshal(encodedUsage, value.Usage); err != nil {
			return domain.Invocation{}, fmt.Errorf("decode invocation usage: %w", err)
		}
	}
	return value, nil
}

func getInvocationForUpdate(
	ctx context.Context,
	tx pgx.Tx,
	isolationDomainID string,
	invocationID string,
) (domain.Invocation, error) {
	var found int
	if err := tx.QueryRow(ctx, `
		SELECT 1
		FROM invocations
		WHERE isolation_domain_id = $1 AND id = $2
		FOR UPDATE
	`, isolationDomainID, invocationID).Scan(&found); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Invocation{}, &DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Invocation was not found."}
		}
		return domain.Invocation{}, fmt.Errorf("lock invocation: %w", err)
	}
	return scanInvocation(tx.QueryRow(ctx, invocationSelect+`
		WHERE invocation.isolation_domain_id = $1 AND invocation.id = $2
		GROUP BY invocation.isolation_domain_id, invocation.id
	`, isolationDomainID, invocationID))
}

func getRevisionForUpdate(ctx context.Context, tx pgx.Tx, isolationDomainID, revisionID string) (domain.ServiceRevision, error) {
	return scanServiceRevision(tx.QueryRow(ctx, serviceRevisionSelect+" FOR UPDATE", isolationDomainID, revisionID))
}

type operationQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getPublicationOperation(
	ctx context.Context,
	querier operationQuerier,
	isolationDomainID string,
	operationID string,
) (domain.Operation, error) {
	return scanOperation(querier.QueryRow(ctx, `
		SELECT isolation_domain_id, id, command, desired_state, observed_state,
		       state_machine_version, generation, attempt, lease_owner, lease_token,
		       lease_expires_at, due_at, deadline_at, error_classification, error,
		       terminal_result, correlation_id, actor_id, created_at, updated_at, revision_id
		FROM service_publication_operations
		WHERE isolation_domain_id = $1 AND id = $2
	`, isolationDomainID, operationID), "service-publication")
}

func getInvocationOperation(
	ctx context.Context,
	querier operationQuerier,
	isolationDomainID string,
	operationID string,
) (domain.Operation, error) {
	return scanOperation(querier.QueryRow(ctx, `
		SELECT isolation_domain_id, id, command, desired_state, observed_state,
		       state_machine_version, generation, attempt, lease_owner, lease_token,
		       lease_expires_at, due_at, deadline_at, error_classification, error,
		       terminal_result, correlation_id, actor_id, created_at, updated_at, invocation_id
		FROM invocation_execution_operations
		WHERE isolation_domain_id = $1 AND id = $2
	`, isolationDomainID, operationID), "invocation-execution")
}

func scanOperation(row rowScanner, kind string) (domain.Operation, error) {
	var value domain.Operation
	var leaseOwner, errorClassification pgtype.Text
	var leaseToken int64
	var leaseExpires pgtype.Timestamptz
	var encodedError, encodedResult []byte
	var actorID string
	value.Kind = kind
	err := row.Scan(
		&value.Metadata.IsolationDomainID,
		&value.Metadata.ID,
		&value.Command,
		&value.DesiredState,
		&value.ObservedState,
		&value.StateMachineVersion,
		&value.Metadata.Generation,
		&value.Attempt,
		&leaseOwner,
		&leaseToken,
		&leaseExpires,
		&value.DueAt,
		&value.DeadlineAt,
		&errorClassification,
		&encodedError,
		&encodedResult,
		&value.CorrelationID,
		&actorID,
		&value.Metadata.CreatedAt,
		&value.Metadata.UpdatedAt,
		&value.ResourceID,
	)
	if err != nil {
		return domain.Operation{}, err
	}
	value.Metadata.Version = value.Metadata.Generation
	value.Metadata.CreatedBy = actorID
	if leaseOwner.Valid && leaseExpires.Valid {
		value.Lease = &domain.OperationLease{
			Owner:        leaseOwner.String,
			FencingToken: leaseToken,
			ExpiresAt:    leaseExpires.Time,
		}
	}
	if errorClassification.Valid {
		value.ErrorClassification = errorClassification.String
	}
	if len(encodedError) > 0 {
		value.Error = &domain.APIError{}
		if err := json.Unmarshal(encodedError, value.Error); err != nil {
			return domain.Operation{}, fmt.Errorf("decode operation error: %w", err)
		}
	}
	if len(encodedResult) > 0 {
		if err := json.Unmarshal(encodedResult, &value.TerminalResult); err != nil {
			return domain.Operation{}, fmt.Errorf("decode operation result: %w", err)
		}
	}
	return value, nil
}
