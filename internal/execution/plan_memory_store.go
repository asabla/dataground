package execution

import (
	"context"
	"sync"
)

// MemoryExecutionPlanStore is a process-local conformance store. It does not
// validate that a referenced revision exists; the PostgreSQL implementation
// enforces that relationship.
type MemoryExecutionPlanStore struct {
	mu    sync.Mutex
	plans map[string]ExecutionPlan
}

func NewMemoryExecutionPlanStore() *MemoryExecutionPlanStore {
	return &MemoryExecutionPlanStore{plans: make(map[string]ExecutionPlan)}
}

func (store *MemoryExecutionPlanStore) BindExecutionPlan(
	_ context.Context,
	binding ExecutionPlanBinding,
) (ExecutionPlan, error) {
	normalized, err := NormalizeExecutionPlanBinding(binding)
	if err != nil {
		return ExecutionPlan{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	key := scopedKey(normalized.Plan.IsolationDomainID, normalized.Plan.RevisionID)
	if existing, found := store.plans[key]; found {
		if !EqualExecutionPlans(existing, normalized.Plan) {
			return ExecutionPlan{}, ErrExecutionPlanConflict
		}
		return CloneExecutionPlan(existing), nil
	}
	store.plans[key] = CloneExecutionPlan(normalized.Plan)
	return CloneExecutionPlan(normalized.Plan), nil
}

func (store *MemoryExecutionPlanStore) GetExecutionPlan(
	_ context.Context,
	isolationDomainID string,
	revisionID string,
) (ExecutionPlan, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	plan, found := store.plans[scopedKey(isolationDomainID, revisionID)]
	if !found {
		return ExecutionPlan{}, ErrExecutionPlanMissing
	}
	return CloneExecutionPlan(plan), nil
}
