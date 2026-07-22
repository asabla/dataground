package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrPersistence = errors.New("execution state persistence failed")

type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

func (store *Store) RegisterGateway(ctx context.Context, registration execution.GatewayRegistration) (execution.Gateway, error) {
	if registration.IsolationDomainID == "" || registration.ID == "" || registration.Driver == "" || registration.Endpoint == "" {
		return execution.Gateway{}, errors.New("isolation domain, gateway id, driver, and endpoint are required")
	}
	capabilities, err := execution.NormalizeCapabilities(registration.Capabilities)
	if err != nil {
		return execution.Gateway{}, err
	}
	now := store.now()
	_, err = store.pool.Exec(ctx, `
		INSERT INTO execution_gateways (
			isolation_domain_id, id, driver, endpoint, state, capabilities,
			registered_at, updated_at
		) VALUES ($1, $2, $3, $4, 'active', $5, $6, $6)
		ON CONFLICT (isolation_domain_id, id) DO NOTHING
	`, registration.IsolationDomainID, registration.ID, registration.Driver, registration.Endpoint, capabilities, now)
	if err != nil {
		return execution.Gateway{}, fmt.Errorf("register gateway: %w", ErrPersistence)
	}
	record, err := store.GetGateway(ctx, registration.IsolationDomainID, registration.ID)
	if err != nil {
		return execution.Gateway{}, err
	}
	if record.Endpoint != registration.Endpoint || record.Gateway.Driver != registration.Driver ||
		!slices.Equal(record.Gateway.Capabilities, capabilities) {
		return execution.Gateway{}, execution.ErrStateConflict
	}
	return record.Gateway, nil
}

func (store *Store) SetGatewayState(
	ctx context.Context,
	isolationDomainID string,
	gatewayID string,
	state execution.GatewayState,
) error {
	if !execution.ValidGatewayState(state) {
		return errors.New("invalid gateway state")
	}
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin gateway state transition: %w", ErrPersistence)
	}
	defer transaction.Rollback(ctx)
	now := store.now()
	result, err := transaction.Exec(ctx, `
		UPDATE execution_gateways
		SET state = $3, version = version + 1, updated_at = $4
		WHERE isolation_domain_id = $1 AND id = $2
	`, isolationDomainID, gatewayID, state, now)
	if err != nil {
		return fmt.Errorf("update gateway state: %w", ErrPersistence)
	}
	if result.RowsAffected() == 0 {
		return execution.ErrNoGateway
	}
	if state == execution.GatewayLost {
		if _, err := transaction.Exec(ctx, `
			UPDATE execution_placements
			SET state = 'lost', updated_at = $3
			WHERE isolation_domain_id = $1 AND gateway_id = $2
			  AND state IN ('reserved', 'active')
		`, isolationDomainID, gatewayID, now); err != nil {
			return fmt.Errorf("mark lost placements: %w", ErrPersistence)
		}
		if _, err := transaction.Exec(ctx, `
			UPDATE execution_instances
			SET observed_state = 'unknown', updated_at = $3
			WHERE isolation_domain_id = $1 AND gateway_id = $2
			  AND observed_state NOT IN ('terminated', 'error')
		`, isolationDomainID, gatewayID, now); err != nil {
			return fmt.Errorf("mark lost executions: %w", ErrPersistence)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit gateway state transition: %w", ErrPersistence)
	}
	return nil
}

func (store *Store) ReservePlacement(ctx context.Context, request execution.PlacementRequest) (execution.Placement, error) {
	if request.IsolationDomainID == "" || request.OperationID == "" {
		return execution.Placement{}, errors.New("isolation domain and operation are required")
	}
	capabilities, err := execution.NormalizeCapabilities(request.RequiredCapabilities)
	if err != nil {
		return execution.Placement{}, err
	}
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return execution.Placement{}, fmt.Errorf("begin placement reservation: %w", ErrPersistence)
	}
	defer transaction.Rollback(ctx)
	if record, found, err := getPlacementByOperation(ctx, transaction, request.IsolationDomainID, request.OperationID); err != nil {
		return execution.Placement{}, err
	} else if found {
		if !slices.Equal(record.requiredCapabilities, capabilities) {
			return execution.Placement{}, execution.ErrStateConflict
		}
		return record.placement, commitPlacement(ctx, transaction)
	}
	var gatewayID string
	err = transaction.QueryRow(ctx, `
		SELECT gateway.id
		FROM execution_gateways AS gateway
		WHERE gateway.isolation_domain_id = $1
		  AND gateway.state = 'active'
		  AND gateway.capabilities @> $2::text[]
		ORDER BY (
			SELECT count(*)
			FROM execution_placements AS placement
			WHERE placement.isolation_domain_id = gateway.isolation_domain_id
			  AND placement.gateway_id = gateway.id
			  AND placement.state IN ('reserved', 'active')
		), gateway.id
		FOR UPDATE OF gateway SKIP LOCKED
		LIMIT 1
	`, request.IsolationDomainID, capabilities).Scan(&gatewayID)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Placement{}, execution.ErrNoGateway
	}
	if err != nil {
		return execution.Placement{}, fmt.Errorf("select placement gateway: %w", ErrPersistence)
	}
	placement := execution.Placement{
		IsolationDomainID: request.IsolationDomainID,
		ID:                identity.Derived("plc", request.IsolationDomainID+":"+request.OperationID),
		GatewayID:         gatewayID,
	}
	now := store.now()
	_, err = transaction.Exec(ctx, `
		INSERT INTO execution_placements (
			isolation_domain_id, id, operation_id, gateway_id,
			required_capabilities, state, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, 'reserved', $6, $6)
		ON CONFLICT (isolation_domain_id, operation_id) DO NOTHING
	`, request.IsolationDomainID, placement.ID, request.OperationID, gatewayID, capabilities, now)
	if err != nil {
		return execution.Placement{}, fmt.Errorf("reserve placement: %w", ErrPersistence)
	}
	record, found, err := getPlacementByOperation(ctx, transaction, request.IsolationDomainID, request.OperationID)
	if err != nil {
		return execution.Placement{}, err
	}
	if !found {
		return execution.Placement{}, fmt.Errorf("read reserved placement: %w", ErrPersistence)
	}
	if !slices.Equal(record.requiredCapabilities, capabilities) {
		return execution.Placement{}, execution.ErrStateConflict
	}
	return record.placement, commitPlacement(ctx, transaction)
}

func commitPlacement(ctx context.Context, transaction pgx.Tx) error {
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit placement reservation: %w", ErrPersistence)
	}
	return nil
}

type placementRecord struct {
	placement            execution.Placement
	requiredCapabilities []string
}

func (store *Store) GetPlacement(ctx context.Context, isolationDomainID, placementID string) (execution.Placement, error) {
	var placement execution.Placement
	err := store.pool.QueryRow(ctx, `
		SELECT isolation_domain_id, id, gateway_id
		FROM execution_placements
		WHERE isolation_domain_id = $1 AND id = $2
	`, isolationDomainID, placementID).Scan(&placement.IsolationDomainID, &placement.ID, &placement.GatewayID)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Placement{}, execution.ErrPlacementMissing
	}
	if err != nil {
		return execution.Placement{}, fmt.Errorf("read placement: %w", ErrPersistence)
	}
	return placement, nil
}

func getPlacementByOperation(
	ctx context.Context,
	querier interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	isolationDomainID string,
	operationID string,
) (placementRecord, bool, error) {
	var record placementRecord
	err := querier.QueryRow(ctx, `
		SELECT isolation_domain_id, id, gateway_id, required_capabilities
		FROM execution_placements
		WHERE isolation_domain_id = $1 AND operation_id = $2
	`, isolationDomainID, operationID).Scan(
		&record.placement.IsolationDomainID, &record.placement.ID, &record.placement.GatewayID,
		&record.requiredCapabilities,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return placementRecord{}, false, nil
	}
	if err != nil {
		return placementRecord{}, false, fmt.Errorf("read placement by operation: %w", ErrPersistence)
	}
	return record, true, nil
}

func (store *Store) GetGateway(ctx context.Context, isolationDomainID, gatewayID string) (execution.GatewayRecord, error) {
	var record execution.GatewayRecord
	err := store.pool.QueryRow(ctx, `
		SELECT isolation_domain_id, id, driver, endpoint, state, capabilities
		FROM execution_gateways
		WHERE isolation_domain_id = $1 AND id = $2
	`, isolationDomainID, gatewayID).Scan(
		&record.Gateway.IsolationDomainID, &record.Gateway.ID, &record.Gateway.Driver,
		&record.Endpoint, &record.Gateway.State, &record.Gateway.Capabilities,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.GatewayRecord{}, execution.ErrNoGateway
	}
	if err != nil {
		return execution.GatewayRecord{}, fmt.Errorf("read gateway: %w", ErrPersistence)
	}
	return record, nil
}

func (store *Store) SaveExecution(ctx context.Context, record execution.ExecutionRecord) error {
	if record.Execution.IsolationDomainID == "" || record.Execution.ID == "" || record.PlacementID == "" ||
		record.OperationID == "" || record.SandboxName == "" || !validExecutionState(record.Execution.State) {
		return errors.New("complete execution identity and valid state are required")
	}
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin execution save: %w", ErrPersistence)
	}
	defer transaction.Rollback(ctx)
	if existing, found, err := getExecution(ctx, transaction, execution.ExecutionRef{
		IsolationDomainID: record.Execution.IsolationDomainID, ID: record.Execution.ID,
	}); err != nil {
		return err
	} else if found {
		if !sameExecutionIdentity(existing, record) {
			return execution.ErrStateConflict
		}
		if err := transaction.Commit(ctx); err != nil {
			return fmt.Errorf("commit execution replay: %w", ErrPersistence)
		}
		return nil
	}
	var placementOperationID, placementGatewayID, placementState string
	err = transaction.QueryRow(ctx, `
		SELECT operation_id, gateway_id, state
		FROM execution_placements
		WHERE isolation_domain_id = $1 AND id = $2
		FOR UPDATE
	`, record.Execution.IsolationDomainID, record.PlacementID).Scan(
		&placementOperationID, &placementGatewayID, &placementState,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.ErrPlacementMissing
	}
	if err != nil {
		return fmt.Errorf("lock execution placement: %w", ErrPersistence)
	}
	// A concurrent creator may have committed while this transaction waited
	// for the placement lock. Re-read before applying the external identity.
	if existing, found, err := getExecution(ctx, transaction, execution.ExecutionRef{
		IsolationDomainID: record.Execution.IsolationDomainID, ID: record.Execution.ID,
	}); err != nil {
		return err
	} else if found {
		if !sameExecutionIdentity(existing, record) {
			return execution.ErrStateConflict
		}
		if err := transaction.Commit(ctx); err != nil {
			return fmt.Errorf("commit concurrent execution replay: %w", ErrPersistence)
		}
		return nil
	}
	if placementOperationID != record.OperationID || placementGatewayID != record.Execution.GatewayID ||
		(placementState != "reserved" && placementState != "active") {
		return execution.ErrStateConflict
	}
	now := store.now()
	_, err = transaction.Exec(ctx, `
		INSERT INTO execution_instances (
			isolation_domain_id, id, placement_id, operation_id, gateway_id,
			sandbox_name, observed_state, created_at, updated_at, terminated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::timestamptz, $8::timestamptz,
		          CASE WHEN $7 = 'terminated' THEN $8::timestamptz ELSE NULL::timestamptz END)
	`, record.Execution.IsolationDomainID, record.Execution.ID, record.PlacementID,
		record.OperationID, record.Execution.GatewayID, record.SandboxName, record.Execution.State, now)
	if err != nil {
		return fmt.Errorf("save execution: %w", ErrPersistence)
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE execution_placements
		SET state = CASE WHEN $3 THEN 'released' ELSE 'active' END, updated_at = $4
		WHERE isolation_domain_id = $1 AND id = $2
	`, record.Execution.IsolationDomainID, record.PlacementID, record.Execution.State == "terminated", now); err != nil {
		return fmt.Errorf("activate execution placement: %w", ErrPersistence)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit execution save: %w", ErrPersistence)
	}
	return nil
}

func (store *Store) GetExecution(ctx context.Context, ref execution.ExecutionRef) (execution.ExecutionRecord, error) {
	record, found, err := getExecution(ctx, store.pool, ref)
	if err != nil {
		return execution.ExecutionRecord{}, err
	}
	if !found {
		return execution.ExecutionRecord{}, execution.ErrExecutionMissing
	}
	return record, nil
}

func (store *Store) GetExecutionByOperation(
	ctx context.Context,
	isolationDomainID string,
	operationID string,
) (execution.Execution, error) {
	var value execution.Execution
	err := store.pool.QueryRow(ctx, `
		SELECT isolation_domain_id, id, gateway_id, observed_state
		FROM execution_instances
		WHERE isolation_domain_id = $1 AND operation_id = $2
	`, isolationDomainID, operationID).Scan(
		&value.IsolationDomainID,
		&value.ID,
		&value.GatewayID,
		&value.State,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Execution{}, execution.ErrExecutionMissing
	}
	if err != nil {
		return execution.Execution{}, fmt.Errorf("read execution by operation: %w", ErrPersistence)
	}
	return value, nil
}

func getExecution(
	ctx context.Context,
	querier interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	ref execution.ExecutionRef,
) (execution.ExecutionRecord, bool, error) {
	var record execution.ExecutionRecord
	err := querier.QueryRow(ctx, `
		SELECT isolation_domain_id, id, gateway_id, observed_state,
		       placement_id, operation_id, sandbox_name
		FROM execution_instances
		WHERE isolation_domain_id = $1 AND id = $2
	`, ref.IsolationDomainID, ref.ID).Scan(
		&record.Execution.IsolationDomainID, &record.Execution.ID, &record.Execution.GatewayID,
		&record.Execution.State, &record.PlacementID, &record.OperationID, &record.SandboxName,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.ExecutionRecord{}, false, nil
	}
	if err != nil {
		return execution.ExecutionRecord{}, false, fmt.Errorf("read execution: %w", ErrPersistence)
	}
	return record, true, nil
}

func (store *Store) UpdateExecutionState(ctx context.Context, ref execution.ExecutionRef, state string) error {
	if !validExecutionState(state) {
		return errors.New("invalid execution state")
	}
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin execution state update: %w", ErrPersistence)
	}
	defer transaction.Rollback(ctx)
	var currentState string
	err = transaction.QueryRow(ctx, `
		SELECT observed_state
		FROM execution_instances
		WHERE isolation_domain_id = $1 AND id = $2
		FOR UPDATE
	`, ref.IsolationDomainID, ref.ID).Scan(&currentState)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.ErrExecutionMissing
	}
	if err != nil {
		return fmt.Errorf("lock execution state: %w", ErrPersistence)
	}
	if currentState == "terminated" && state != "terminated" {
		return execution.ErrStateConflict
	}
	now := store.now()
	_, err = transaction.Exec(ctx, `
		UPDATE execution_instances
		SET observed_state = $3,
		    updated_at = $4::timestamptz,
		    terminated_at = CASE WHEN $3 = 'terminated' THEN $4::timestamptz ELSE NULL::timestamptz END
		WHERE isolation_domain_id = $1 AND id = $2
	`, ref.IsolationDomainID, ref.ID, state, now)
	if err != nil {
		return fmt.Errorf("update execution state: %w", ErrPersistence)
	}
	if state == "terminated" {
		if _, err := transaction.Exec(ctx, `
			UPDATE execution_placements AS placement
			SET state = 'released', updated_at = $3
			FROM execution_instances AS instance
			WHERE instance.isolation_domain_id = $1 AND instance.id = $2
			  AND placement.isolation_domain_id = instance.isolation_domain_id
			  AND placement.id = instance.placement_id
		`, ref.IsolationDomainID, ref.ID, now); err != nil {
			return fmt.Errorf("release execution placement: %w", ErrPersistence)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit execution state update: %w", ErrPersistence)
	}
	return nil
}

func validExecutionState(state string) bool {
	switch state {
	case "provisioning", "ready", "running", "waiting", "deleting", "terminated", "error", "unknown":
		return true
	default:
		return false
	}
}

func sameExecutionIdentity(left, right execution.ExecutionRecord) bool {
	return left.Execution.IsolationDomainID == right.Execution.IsolationDomainID &&
		left.Execution.ID == right.Execution.ID &&
		left.Execution.GatewayID == right.Execution.GatewayID &&
		left.PlacementID == right.PlacementID &&
		left.OperationID == right.OperationID &&
		left.SandboxName == right.SandboxName
}

var _ execution.StateStore = (*Store)(nil)
