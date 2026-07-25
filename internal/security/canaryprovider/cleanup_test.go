package canaryprovider

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/security/canaryevidence"
)

func TestAdapterOwnsExactProviderCleanup(t *testing.T) {
	t.Parallel()

	binding := testBinding()
	manager := &fakeManager{observations: []observationResult{
		{observation: presentObservation(binding)},
		{observation: absentObservation(binding)},
	}}
	adapter, err := New(Config{RunID: testRunID, Binding: binding}, manager)
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Name() != binding.Name {
		t.Fatalf("Name() = %q", adapter.Name())
	}
	if err := adapter.Cleanup(context.Background(), cleanupRequest(binding.Name)); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if calls := manager.callNames(); !slices.Equal(calls, []string{"observe", "delete", "observe"}) {
		t.Fatalf("manager calls = %v", calls)
	}
	if manager.deletedRef != bindingRef(binding) {
		t.Fatalf("deleted ref = %+v", manager.deletedRef)
	}
}

func TestAdapterAcceptsObservedPriorAbsenceWithoutMutation(t *testing.T) {
	t.Parallel()

	binding := testBinding()
	manager := &fakeManager{observations: []observationResult{{
		observation: absentObservation(binding),
	}}}
	adapter, err := New(Config{RunID: testRunID, Binding: binding}, manager)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Cleanup(context.Background(), cleanupRequest(binding.Name)); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if calls := manager.callNames(); !slices.Equal(calls, []string{"observe"}) {
		t.Fatalf("manager calls = %v", calls)
	}
}

func TestAdapterRejectsProviderReplacementWithoutDelete(t *testing.T) {
	t.Parallel()

	binding := testBinding()
	replacement := presentObservation(binding)
	replacement.ID = "replacement"
	replacement.ResourceVersion++
	manager := &fakeManager{observations: []observationResult{{observation: replacement}}}
	adapter, err := New(Config{RunID: testRunID, Binding: binding}, manager)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Cleanup(context.Background(), cleanupRequest(binding.Name)); !errors.Is(err, ErrCleanupUncertain) {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if calls := manager.callNames(); !slices.Equal(calls, []string{"observe"}) {
		t.Fatalf("replacement triggered mutation: %v", calls)
	}
}

func TestAdapterRecoversLostDeleteAcknowledgementByAbsence(t *testing.T) {
	t.Parallel()

	binding := testBinding()
	manager := &fakeManager{
		observations: []observationResult{
			{observation: presentObservation(binding)},
			{observation: absentObservation(binding)},
		},
		deleteErr: errors.New("sensitive deletion payload"),
	}
	adapter, err := New(Config{RunID: testRunID, Binding: binding}, manager)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Cleanup(context.Background(), cleanupRequest(binding.Name)); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
}

func TestAdapterSanitizesManagerFailures(t *testing.T) {
	t.Parallel()

	binding := testBinding()
	tests := map[string]*fakeManager{
		"initial observation": {
			observations: []observationResult{{err: errors.New("sensitive observation payload")}},
		},
		"delete remains present": {
			observations: []observationResult{
				{observation: presentObservation(binding)},
				{observation: presentObservation(binding)},
			},
			deleteErr: errors.New("sensitive deletion payload"),
		},
		"terminal observation": {
			observations: []observationResult{
				{observation: presentObservation(binding)},
				{err: errors.New("sensitive observation payload")},
			},
		},
	}
	for name, manager := range tests {
		name, manager := name, manager
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			adapter, err := New(Config{RunID: testRunID, Binding: binding}, manager)
			if err != nil {
				t.Fatal(err)
			}
			err = adapter.Cleanup(context.Background(), cleanupRequest(binding.Name))
			if !errors.Is(err, ErrCleanupFailure) {
				t.Fatalf("Cleanup() error = %v", err)
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("Cleanup() leaked manager error: %v", err)
			}
		})
	}
}

func TestAdapterRejectsIdentityDriftAndInvalidRequests(t *testing.T) {
	t.Parallel()

	binding := testBinding()
	for name, mutate := range map[string]func(*execution.ProviderBindingObservation){
		"scope": func(observation *execution.ProviderBindingObservation) {
			observation.IsolationDomainID = "other"
		},
		"gateway": func(observation *execution.ProviderBindingObservation) {
			observation.GatewayID = "other"
		},
		"name": func(observation *execution.ProviderBindingObservation) {
			observation.Name = "other"
		},
		"timestamp": func(observation *execution.ProviderBindingObservation) {
			observation.ObservedAt = time.Time{}
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			observation := presentObservation(binding)
			mutate(&observation)
			manager := &fakeManager{observations: []observationResult{{observation: observation}}}
			adapter, err := New(Config{RunID: testRunID, Binding: binding}, manager)
			if err != nil {
				t.Fatal(err)
			}
			if err := adapter.Cleanup(context.Background(), cleanupRequest(binding.Name)); !errors.Is(err, ErrCleanupUncertain) {
				t.Fatalf("Cleanup() error = %v", err)
			}
		})
	}

	adapter, err := New(Config{RunID: testRunID, Binding: binding}, &fakeManager{})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []canaryevidence.CleanupRequest{
		{RunID: "fedcba9876543210fedcba9876543210", ResourceKind: "provider", ResourceName: binding.Name},
		{RunID: testRunID, ResourceKind: "sandbox", ResourceName: binding.Name},
		{RunID: testRunID, ResourceKind: "provider", ResourceName: "other"},
	} {
		if err := adapter.Cleanup(context.Background(), request); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("Cleanup(%+v) error = %v", request, err)
		}
	}
}

func TestAdapterCleanupIsConcurrentAndIdempotent(t *testing.T) {
	t.Parallel()

	binding := testBinding()
	manager := &fakeManager{observations: []observationResult{
		{observation: presentObservation(binding)},
		{observation: absentObservation(binding)},
	}}
	adapter, err := New(Config{RunID: testRunID, Binding: binding}, manager)
	if err != nil {
		t.Fatal(err)
	}

	const callers = 24
	errorsByCaller := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsByCaller <- adapter.Cleanup(context.Background(), cleanupRequest(binding.Name))
		}()
	}
	wait.Wait()
	close(errorsByCaller)
	for err := range errorsByCaller {
		if err != nil {
			t.Fatalf("Cleanup() error = %v", err)
		}
	}
	if calls := manager.callNames(); !slices.Equal(calls, []string{"observe", "delete", "observe"}) {
		t.Fatalf("manager calls = %v", calls)
	}
}

func TestAdapterRejectsInvalidConstructionAndSerialization(t *testing.T) {
	t.Parallel()

	binding := testBinding()
	var nilManager *fakeManager
	for name, config := range map[string]Config{
		"run": {RunID: "", Binding: binding},
		"name": {
			RunID: testRunID,
			Binding: func() execution.ProviderBinding {
				value := binding
				value.Name = "other"
				return value
			}(),
		},
		"id": {
			RunID: testRunID,
			Binding: func() execution.ProviderBinding {
				value := binding
				value.ID = ""
				return value
			}(),
		},
		"version": {
			RunID: testRunID,
			Binding: func() execution.ProviderBinding {
				value := binding
				value.ResourceVersion = 0
				return value
			}(),
		},
	} {
		name, config := name, config
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(config, &fakeManager{}); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("New() error = %v", err)
			}
		})
	}
	if _, err := New(Config{RunID: testRunID, Binding: binding}, nilManager); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("New(typed nil) error = %v", err)
	}

	adapter, err := New(Config{RunID: testRunID, Binding: binding}, &fakeManager{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(adapter); !errors.Is(err, ErrSerialization) {
		t.Fatalf("json.Marshal() error = %v", err)
	}
}

const testRunID = "0123456789abcdef0123456789abcdef"

type observationResult struct {
	observation execution.ProviderBindingObservation
	err         error
}

type fakeManager struct {
	mu           sync.Mutex
	observations []observationResult
	deleteErr    error
	deletedRef   execution.ProviderBindingRef
	calls        []string
}

func (manager *fakeManager) ObserveProviderBinding(
	_ context.Context,
	_ execution.ProviderBindingRef,
) (execution.ProviderBindingObservation, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.calls = append(manager.calls, "observe")
	if len(manager.observations) == 0 {
		return execution.ProviderBindingObservation{}, errors.New("unexpected observation")
	}
	next := manager.observations[0]
	manager.observations = manager.observations[1:]
	return next.observation, next.err
}

func (manager *fakeManager) DeleteProviderBinding(
	_ context.Context,
	ref execution.ProviderBindingRef,
) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.calls = append(manager.calls, "delete")
	manager.deletedRef = ref
	return manager.deleteErr
}

func (manager *fakeManager) callNames() []string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return slices.Clone(manager.calls)
}

func testBinding() execution.ProviderBinding {
	return execution.ProviderBinding{
		IsolationDomainID: "iso-a",
		GatewayID:         "gateway-a",
		ID:                "provider-id",
		Name:              providerNamePrefix + testRunID,
		ResourceVersion:   7,
	}
}

func bindingRef(binding execution.ProviderBinding) execution.ProviderBindingRef {
	return execution.ProviderBindingRef{
		IsolationDomainID: binding.IsolationDomainID,
		GatewayID:         binding.GatewayID,
		ID:                binding.ID,
		Name:              binding.Name,
		ResourceVersion:   binding.ResourceVersion,
	}
}

func presentObservation(binding execution.ProviderBinding) execution.ProviderBindingObservation {
	return execution.ProviderBindingObservation{
		IsolationDomainID: binding.IsolationDomainID,
		GatewayID:         binding.GatewayID,
		ID:                binding.ID,
		Name:              binding.Name,
		ResourceVersion:   binding.ResourceVersion,
		Exists:            true,
		ObservedAt:        time.Date(2026, time.July, 25, 18, 0, 0, 0, time.UTC),
	}
}

func absentObservation(binding execution.ProviderBinding) execution.ProviderBindingObservation {
	return execution.ProviderBindingObservation{
		IsolationDomainID: binding.IsolationDomainID,
		GatewayID:         binding.GatewayID,
		Name:              binding.Name,
		Exists:            false,
		ObservedAt:        time.Date(2026, time.July, 25, 18, 0, 1, 0, time.UTC),
	}
}

func cleanupRequest(name string) canaryevidence.CleanupRequest {
	return canaryevidence.CleanupRequest{
		RunID:        testRunID,
		ResourceKind: "provider",
		ResourceName: name,
	}
}
