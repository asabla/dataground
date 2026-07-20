package execution

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"

	"github.com/asabla/dataground/internal/identity"
)

type memoryGateway struct {
	record   GatewayRecord
	reserved int
}

type memoryPlacement struct {
	placement            Placement
	operationID          string
	requiredCapabilities []string
	state                string
}

// MemoryStateStore is the process-local conformance store. Durable workers use
// the PostgreSQL implementation instead.
type MemoryStateStore struct {
	mu         sync.Mutex
	gateways   map[string]*memoryGateway
	placements map[string]memoryPlacement
	executions map[string]ExecutionRecord
}

func NewMemoryStateStore() *MemoryStateStore {
	return &MemoryStateStore{
		gateways: make(map[string]*memoryGateway), placements: make(map[string]memoryPlacement),
		executions: make(map[string]ExecutionRecord),
	}
}

func (store *MemoryStateStore) RegisterGateway(_ context.Context, registration GatewayRegistration) (Gateway, error) {
	if registration.IsolationDomainID == "" || registration.ID == "" || registration.Driver == "" || registration.Endpoint == "" {
		return Gateway{}, errors.New("isolation domain, gateway id, driver, and endpoint are required")
	}
	capabilities, err := NormalizeCapabilities(registration.Capabilities)
	if err != nil {
		return Gateway{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	key := scopedKey(registration.IsolationDomainID, registration.ID)
	if existing, exists := store.gateways[key]; exists {
		if existing.record.Endpoint != registration.Endpoint || existing.record.Gateway.Driver != registration.Driver ||
			!slices.Equal(existing.record.Gateway.Capabilities, capabilities) {
			return Gateway{}, ErrStateConflict
		}
		return existing.record.Gateway, nil
	}
	gateway := Gateway{
		IsolationDomainID: registration.IsolationDomainID, ID: registration.ID, Driver: registration.Driver,
		State: GatewayActive, Capabilities: capabilities,
	}
	store.gateways[key] = &memoryGateway{record: GatewayRecord{Gateway: gateway, Endpoint: registration.Endpoint}}
	return gateway, nil
}

func (store *MemoryStateStore) SetGatewayState(_ context.Context, isolationDomainID, gatewayID string, state GatewayState) error {
	if !ValidGatewayState(state) {
		return errors.New("invalid gateway state")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.gateways[scopedKey(isolationDomainID, gatewayID)]
	if !ok {
		return ErrNoGateway
	}
	entry.record.Gateway.State = state
	if state == GatewayLost {
		for key, placement := range store.placements {
			if placement.placement.IsolationDomainID == isolationDomainID && placement.placement.GatewayID == gatewayID &&
				(placement.state == "reserved" || placement.state == "active") {
				placement.state = "lost"
				store.placements[key] = placement
			}
		}
		for key, record := range store.executions {
			if record.Execution.IsolationDomainID == isolationDomainID && record.Execution.GatewayID == gatewayID &&
				record.Execution.State != "terminated" && record.Execution.State != "error" {
				record.Execution.State = "unknown"
				store.executions[key] = record
			}
		}
	}
	return nil
}

func (store *MemoryStateStore) ReservePlacement(_ context.Context, request PlacementRequest) (Placement, error) {
	if request.IsolationDomainID == "" || request.OperationID == "" {
		return Placement{}, errors.New("isolation domain and operation are required")
	}
	capabilities, err := NormalizeCapabilities(request.RequiredCapabilities)
	if err != nil {
		return Placement{}, err
	}
	placementID := identity.Derived("plc", request.IsolationDomainID+":"+request.OperationID)
	store.mu.Lock()
	defer store.mu.Unlock()
	key := scopedKey(request.IsolationDomainID, placementID)
	if record, ok := store.placements[key]; ok {
		if record.operationID != request.OperationID || !slices.Equal(record.requiredCapabilities, capabilities) {
			return Placement{}, ErrStateConflict
		}
		return record.placement, nil
	}
	eligible := make([]*memoryGateway, 0, len(store.gateways))
	for _, entry := range store.gateways {
		if entry.record.Gateway.IsolationDomainID == request.IsolationDomainID && entry.record.Gateway.State == GatewayActive &&
			containsCapabilities(entry.record.Gateway.Capabilities, capabilities) {
			eligible = append(eligible, entry)
		}
	}
	if len(eligible) == 0 {
		return Placement{}, ErrNoGateway
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].reserved == eligible[j].reserved {
			return eligible[i].record.Gateway.ID < eligible[j].record.Gateway.ID
		}
		return eligible[i].reserved < eligible[j].reserved
	})
	selected := eligible[0]
	selected.reserved++
	placement := Placement{IsolationDomainID: request.IsolationDomainID, ID: placementID, GatewayID: selected.record.Gateway.ID}
	store.placements[key] = memoryPlacement{
		placement: placement, operationID: request.OperationID,
		requiredCapabilities: capabilities, state: "reserved",
	}
	return placement, nil
}

func (store *MemoryStateStore) GetPlacement(_ context.Context, isolationDomainID, placementID string) (Placement, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.placements[scopedKey(isolationDomainID, placementID)]
	if !ok {
		return Placement{}, ErrPlacementMissing
	}
	return record.placement, nil
}

func (store *MemoryStateStore) GetGateway(_ context.Context, isolationDomainID, gatewayID string) (GatewayRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.gateways[scopedKey(isolationDomainID, gatewayID)]
	if !ok {
		return GatewayRecord{}, ErrNoGateway
	}
	return entry.record, nil
}

func (store *MemoryStateStore) SaveExecution(_ context.Context, record ExecutionRecord) error {
	if record.Execution.IsolationDomainID == "" || record.Execution.ID == "" || record.PlacementID == "" ||
		record.OperationID == "" || record.SandboxName == "" || !validMemoryExecutionState(record.Execution.State) {
		return errors.New("complete execution identity and valid state are required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	key := scopedKey(record.Execution.IsolationDomainID, record.Execution.ID)
	if existing, ok := store.executions[key]; ok {
		if !sameMemoryExecutionIdentity(existing, record) {
			return ErrStateConflict
		}
		return nil
	}
	placementKey := scopedKey(record.Execution.IsolationDomainID, record.PlacementID)
	placement, ok := store.placements[placementKey]
	if !ok {
		return ErrPlacementMissing
	}
	if placement.placement.GatewayID != record.Execution.GatewayID || placement.operationID != record.OperationID ||
		(placement.state != "reserved" && placement.state != "active") {
		return ErrStateConflict
	}
	store.executions[key] = record
	if record.Execution.State == "terminated" {
		placement.state = "released"
	} else {
		placement.state = "active"
	}
	store.placements[placementKey] = placement
	return nil
}

func (store *MemoryStateStore) GetExecution(_ context.Context, ref ExecutionRef) (ExecutionRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.executions[scopedKey(ref.IsolationDomainID, ref.ID)]
	if !ok {
		return ExecutionRecord{}, ErrExecutionMissing
	}
	return record, nil
}

func (store *MemoryStateStore) UpdateExecutionState(_ context.Context, ref ExecutionRef, state string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := scopedKey(ref.IsolationDomainID, ref.ID)
	record, ok := store.executions[key]
	if !ok {
		return ErrExecutionMissing
	}
	if record.Execution.State == "terminated" && state != "terminated" {
		return ErrStateConflict
	}
	alreadyTerminated := record.Execution.State == "terminated"
	record.Execution.State = state
	store.executions[key] = record
	if state == "terminated" && !alreadyTerminated {
		placementKey := scopedKey(ref.IsolationDomainID, record.PlacementID)
		if placement, exists := store.placements[placementKey]; exists {
			placement.state = "released"
			store.placements[placementKey] = placement
			if gateway := store.gateways[scopedKey(ref.IsolationDomainID, placement.placement.GatewayID)]; gateway != nil && gateway.reserved > 0 {
				gateway.reserved--
			}
		}
	}
	return nil
}

func scopedKey(isolationDomainID, id string) string { return isolationDomainID + "\x00" + id }

func containsCapabilities(have, required []string) bool {
	available := make(map[string]struct{}, len(have))
	for _, item := range have {
		available[item] = struct{}{}
	}
	for _, item := range required {
		if _, ok := available[item]; !ok {
			return false
		}
	}
	return true
}

func validMemoryExecutionState(state string) bool {
	switch state {
	case "provisioning", "ready", "running", "waiting", "deleting", "terminated", "error", "unknown":
		return true
	default:
		return false
	}
}

func sameMemoryExecutionIdentity(left, right ExecutionRecord) bool {
	return left.Execution.IsolationDomainID == right.Execution.IsolationDomainID &&
		left.Execution.ID == right.Execution.ID &&
		left.Execution.GatewayID == right.Execution.GatewayID &&
		left.PlacementID == right.PlacementID &&
		left.OperationID == right.OperationID &&
		left.SandboxName == right.SandboxName
}
