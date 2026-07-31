package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
)

const executionCreatorTestPolicy = "# SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.\n# SPDX-License-Identifier: Apache-2.0\n\nversion: 1\n"

func TestExecutionCreatorOwnsPinnedCreationAndCleanup(t *testing.T) {
	t.Parallel()

	fixture := newExecutionCreatorFixture()
	creator, err := newExecutionCreator(fixture.config(), fixture.poll)
	if err != nil {
		t.Fatalf("newExecutionCreator() error = %v", err)
	}
	value, err := creator.Create(context.Background())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if value.ID != runtimeExecutionID(testRunID) || value.State != "ready" {
		t.Fatalf("Create() = %+v", value)
	}
	request := fixture.provider.createRequest
	if request.IsolationDomainID != runtimeIsolationDomain(testRunID) ||
		request.OperationID != runtimeOperationID(testRunID) ||
		request.Image != sandboxImage ||
		request.PolicyDigest != "sha256:"+runtimePolicySHA256 ||
		len(request.ProviderProfiles) != 1 ||
		request.ProviderProfiles[0] != namesForRun(testRunID).Provider {
		t.Fatalf("CreateRequest = %+v", request)
	}
	if string(request.Policy) != executionCreatorTestPolicy {
		t.Fatalf("CreateRequest.Policy changed")
	}
	if err := creator.Cleanup(context.Background(), CleanupRequest{
		RunID:        testRunID,
		ResourceKind: "sandbox",
		ResourceName: namesForRun(testRunID).Sandbox,
	}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if fixture.provider.terminateCalls != 1 {
		t.Fatalf("Terminate() calls = %d", fixture.provider.terminateCalls)
	}
}

func TestExecutionCreatorRejectsDriftAndSerialization(t *testing.T) {
	t.Parallel()

	fixture := newExecutionCreatorFixture()
	var typedNilStore *executionCreatorStore
	var typedNilProvider *executionCreatorProvider
	for name, mutate := range map[string]func(*ExecutionCreationConfig){
		"run":      func(config *ExecutionCreationConfig) { config.RunID = "invalid" },
		"policy":   func(config *ExecutionCreationConfig) { config.Policy = []byte("version: 1\n") },
		"store":    func(config *ExecutionCreationConfig) { config.Store = nil },
		"provider":           func(config *ExecutionCreationConfig) { config.Provider = nil },
		"typed nil store":    func(config *ExecutionCreationConfig) { config.Store = typedNilStore },
		"typed nil provider": func(config *ExecutionCreationConfig) { config.Provider = typedNilProvider },
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			config := fixture.config()
			mutate(&config)
			if _, err := NewExecutionCreator(config); !errors.Is(
				err,
				ErrExecutionCreationConfiguration,
			) {
				t.Fatalf("NewExecutionCreator() error = %v", err)
			}
		})
	}
	config := fixture.config()
	creator, err := newExecutionCreator(config, fixture.poll)
	if err != nil {
		t.Fatalf("newExecutionCreator() error = %v", err)
	}
	if _, err := json.Marshal(config); !errors.Is(err, ErrSerialization) {
		t.Fatalf("json.Marshal(config) error = %v", err)
	}
	if _, err := json.Marshal(creator); !errors.Is(err, ErrSerialization) {
		t.Fatalf("json.Marshal(creator) error = %v", err)
	}
}

func TestExecutionCreatorCleansAmbiguousCreateFailure(t *testing.T) {
	t.Parallel()

	fixture := newExecutionCreatorFixture()
	privateErr := errors.New("private native create diagnostics")
	fixture.provider.createErr = privateErr
	creator, err := newExecutionCreator(fixture.config(), fixture.poll)
	if err != nil {
		t.Fatalf("newExecutionCreator() error = %v", err)
	}
	_, err = creator.Create(context.Background())
	if !errors.Is(err, ErrExecutionCreation) ||
		errors.Is(err, privateErr) ||
		strings.Contains(err.Error(), "private") {
		t.Fatalf("Create() error = %v", err)
	}
	if fixture.provider.terminateCalls != 1 {
		t.Fatalf("Terminate() calls = %d", fixture.provider.terminateCalls)
	}
	record, getErr := fixture.store.GetExecution(context.Background(), execution.ExecutionRef{
		IsolationDomainID: runtimeIsolationDomain(testRunID),
		ID:                runtimeExecutionID(testRunID),
	})
	if getErr != nil || record.Execution.State != "terminated" {
		t.Fatalf("GetExecution() = %+v, %v", record, getErr)
	}
}

func TestExecutionCreatorPoisonsReplayButPreservesCleanup(t *testing.T) {
	t.Parallel()

	fixture := newExecutionCreatorFixture()
	creator, err := newExecutionCreator(fixture.config(), fixture.poll)
	if err != nil {
		t.Fatalf("newExecutionCreator() error = %v", err)
	}
	if _, err := creator.Create(context.Background()); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	copyOfCreator := *creator
	if _, err := copyOfCreator.Create(context.Background()); !errors.Is(
		err,
		ErrExecutionCreationOrder,
	) {
		t.Fatalf("second Create() error = %v", err)
	}
	if err := creator.Cleanup(context.Background(), CleanupRequest{
		RunID:        testRunID,
		ResourceKind: "sandbox",
		ResourceName: namesForRun(testRunID).Sandbox,
	}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
}

func TestExecutionCreatorRejectsOverlapBeforeNativeMutation(t *testing.T) {
	t.Parallel()

	fixture := newExecutionCreatorFixture()
	fixture.provider.registerStarted = make(chan struct{})
	fixture.provider.releaseRegister = make(chan struct{})
	creator, err := newExecutionCreator(fixture.config(), fixture.poll)
	if err != nil {
		t.Fatalf("newExecutionCreator() error = %v", err)
	}
	firstResult := make(chan error, 1)
	go func() {
		_, createErr := creator.Create(context.Background())
		firstResult <- createErr
	}()
	<-fixture.provider.registerStarted
	copyOfCreator := *creator
	if _, err := copyOfCreator.Create(context.Background()); !errors.Is(
		err,
		ErrExecutionCreationOrder,
	) {
		t.Fatalf("overlapping Create() error = %v", err)
	}
	close(fixture.provider.releaseRegister)
	if err := <-firstResult; !errors.Is(err, ErrExecutionCreation) {
		t.Fatalf("first Create() error = %v", err)
	}
	if fixture.provider.createCalls != 0 {
		t.Fatalf("Create() calls = %d", fixture.provider.createCalls)
	}
}

func TestExecutionCreatorFailsClosedOnUncertainCleanup(t *testing.T) {
	t.Parallel()

	fixture := newExecutionCreatorFixture()
	creator, err := newExecutionCreator(fixture.config(), fixture.poll)
	if err != nil {
		t.Fatalf("newExecutionCreator() error = %v", err)
	}
	if _, err := creator.Create(context.Background()); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	fixture.provider.terminateObservation = "ready"
	if err := creator.Cleanup(context.Background(), CleanupRequest{
		RunID:        testRunID,
		ResourceKind: "sandbox",
		ResourceName: namesForRun(testRunID).Sandbox,
	}); !errors.Is(err, ErrExecutionCreationCleanup) {
		t.Fatalf("Cleanup() error = %v", err)
	}
}

type executionCreatorFixture struct {
	store    *executionCreatorStore
	provider *executionCreatorProvider
}

func newExecutionCreatorFixture() *executionCreatorFixture {
	store := &executionCreatorStore{}
	provider := &executionCreatorProvider{
		store:                store,
		terminateObservation: "terminated",
	}
	return &executionCreatorFixture{store: store, provider: provider}
}

func (fixture *executionCreatorFixture) config() ExecutionCreationConfig {
	return ExecutionCreationConfig{
		RunID:    testRunID,
		Policy:   []byte(executionCreatorTestPolicy),
		Store:    fixture.store,
		Provider: fixture.provider,
	}
}

func (fixture *executionCreatorFixture) poll(context.Context) error {
	return nil
}

type executionCreatorStore struct {
	mu        sync.Mutex
	gateway   execution.GatewayRecord
	execution execution.ExecutionRecord
}

func (store *executionCreatorStore) GetGateway(
	_ context.Context,
	isolationDomainID string,
	gatewayID string,
) (execution.GatewayRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.gateway.Gateway.IsolationDomainID != isolationDomainID ||
		store.gateway.Gateway.ID != gatewayID {
		return execution.GatewayRecord{}, execution.ErrNoGateway
	}
	return store.gateway, nil
}

func (store *executionCreatorStore) GetExecution(
	_ context.Context,
	ref execution.ExecutionRef,
) (execution.ExecutionRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.execution.Execution.IsolationDomainID != ref.IsolationDomainID ||
		store.execution.Execution.ID != ref.ID {
		return execution.ExecutionRecord{}, execution.ErrExecutionMissing
	}
	return store.execution, nil
}

func (store *executionCreatorStore) setExecutionState(state string) {
	store.mu.Lock()
	store.execution.Execution.State = state
	store.mu.Unlock()
}

type executionCreatorProvider struct {
	store                *executionCreatorStore
	createRequest        execution.CreateRequest
	createErr            error
	terminateErr         error
	terminateObservation string
	terminateCalls       int
	createCalls          int
	registerStarted      chan struct{}
	releaseRegister      chan struct{}
	registerOnce         sync.Once
}

func (provider *executionCreatorProvider) RegisterGateway(
	_ context.Context,
	registration execution.GatewayRegistration,
) (execution.Gateway, error) {
	if provider.registerStarted != nil {
		provider.registerOnce.Do(func() { close(provider.registerStarted) })
		select {
		case <-provider.releaseRegister:
		case <-time.After(time.Second):
			return execution.Gateway{}, errors.New("register release timeout")
		}
	}
	gateway := execution.Gateway{
		IsolationDomainID: registration.IsolationDomainID,
		ID:                registration.ID,
		Driver:            registration.Driver,
		State:             execution.GatewayActive,
		Capabilities:      append([]string(nil), registration.Capabilities...),
	}
	provider.store.mu.Lock()
	provider.store.gateway = execution.GatewayRecord{
		Gateway:  gateway,
		Endpoint: registration.Endpoint,
	}
	provider.store.mu.Unlock()
	return gateway, nil
}

func (*executionCreatorProvider) EnableProviderProfiles(
	context.Context,
	string,
	string,
) error {
	return nil
}

func (provider *executionCreatorProvider) SelectGateway(
	_ context.Context,
	request execution.PlacementRequest,
) (execution.Placement, error) {
	return execution.Placement{
		IsolationDomainID: request.IsolationDomainID,
		ID:                "placement",
		GatewayID:         namesForRun(testRunID).Gateway,
	}, nil
}

func (provider *executionCreatorProvider) Create(
	_ context.Context,
	request execution.CreateRequest,
) (execution.Execution, error) {
	provider.createCalls++
	provider.createRequest = request
	provider.createRequest.Policy = append([]byte(nil), request.Policy...)
	value := execution.Execution{
		IsolationDomainID: request.IsolationDomainID,
		ID:                runtimeExecutionID(testRunID),
		GatewayID:         request.Placement.GatewayID,
		State:             "provisioning",
	}
	provider.store.mu.Lock()
	provider.store.execution = execution.ExecutionRecord{
		Execution:   value,
		PlacementID: request.Placement.ID,
		OperationID: request.OperationID,
		SandboxName: "private-native-sandbox",
	}
	provider.store.mu.Unlock()
	return value, provider.createErr
}

func (provider *executionCreatorProvider) Observe(
	_ context.Context,
	ref execution.ExecutionRef,
) (execution.Observation, error) {
	state := "ready"
	if provider.terminateCalls > 0 {
		state = provider.terminateObservation
	}
	provider.store.setExecutionState(state)
	return execution.Observation{
		IsolationDomainID: ref.IsolationDomainID,
		ExecutionID:       ref.ID,
		State:             state,
		ObservedAt:        time.Now().UTC(),
	}, nil
}

func (*executionCreatorProvider) Export(
	context.Context,
	execution.ExportRequest,
) (execution.ExportResult, error) {
	return execution.ExportResult{}, errors.New("unused")
}

func (provider *executionCreatorProvider) Terminate(
	context.Context,
	execution.ExecutionRef,
) error {
	provider.terminateCalls++
	if provider.terminateErr != nil {
		return provider.terminateErr
	}
	return nil
}

func (*executionCreatorProvider) StartRuntime(
	context.Context,
	execution.ExecutionRef,
) (execution.RuntimeSession, error) {
	return nil, errors.New("unused")
}
