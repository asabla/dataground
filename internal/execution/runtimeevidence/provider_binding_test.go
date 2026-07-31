package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
)

func TestRuntimeProviderProvisionsAndRemovesExactBinding(t *testing.T) {
	t.Parallel()

	port := &runtimeProviderPort{}
	credentials := testRuntimeProviderCredentials()
	provider, err := NewRuntimeProvider(RuntimeProviderConfig{
		RunID:       testRunID,
		Credentials: credentials,
		Provider:    port,
	})
	if err != nil {
		t.Fatalf("NewRuntimeProvider() error = %v", err)
	}
	clearRuntimeProviderCredentials(&credentials)
	if err := provider.Provision(context.Background()); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if port.createCalls != 1 || port.request.Name != namesForRun(testRunID).Provider ||
		port.request.IsolationDomainID != runtimeIsolationDomain(testRunID) ||
		port.request.GatewayID != namesForRun(testRunID).Gateway {
		t.Fatalf("create request = %+v", port.request)
	}
	for _, retained := range port.retained {
		if !runtimeProviderBytesCleared(retained) {
			t.Fatal("provisioner retained runtime credential bytes")
		}
	}
	for key, expected := range map[string]string{
		"access":  "access-value",
		"refresh": "refresh-value",
		"account": "account-value",
		"id":      "id-value",
	} {
		if string(port.observed[key]) != expected {
			t.Fatalf("credential %q = %q", key, port.observed[key])
		}
	}
	if err := provider.Cleanup(context.Background(), CleanupRequest{
		RunID:        testRunID,
		ResourceKind: "provider",
		ResourceName: namesForRun(testRunID).Provider,
	}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if port.deleteCalls != 1 || port.exists {
		t.Fatalf("delete calls = %d, exists = %t", port.deleteCalls, port.exists)
	}
}

func TestRuntimeProviderRecoversLostAcknowledgements(t *testing.T) {
	t.Parallel()

	port := &runtimeProviderPort{
		createErr: errors.New("private create payload"),
		deleteErr: errors.New("private delete payload"),
	}
	provider := newTestRuntimeProvider(t, port)
	if err := provider.Provision(context.Background()); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if err := provider.Cleanup(context.Background(), CleanupRequest{
		RunID:        testRunID,
		ResourceKind: "provider",
		ResourceName: namesForRun(testRunID).Provider,
	}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if port.exists {
		t.Fatal("binding survived recovered delete acknowledgement loss")
	}
}

func TestRuntimeProviderRejectsPreexistingOrSubstitutedBinding(t *testing.T) {
	t.Parallel()

	for name, configure := range map[string]func(*runtimeProviderPort){
		"preexisting": func(port *runtimeProviderPort) {
			port.exists = true
			port.binding = port.expectedBinding()
		},
		"substituted result": func(port *runtimeProviderPort) {
			port.returnBinding = execution.ProviderBinding{
				IsolationDomainID: runtimeIsolationDomain(testRunID),
				GatewayID:         namesForRun(testRunID).Gateway,
				ID:                "replacement",
				Name:              namesForRun(testRunID).Provider,
				ResourceVersion:   9,
			}
		},
	} {
		name, configure := name, configure
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			port := &runtimeProviderPort{}
			configure(port)
			provider := newTestRuntimeProvider(t, port)
			err := provider.Provision(context.Background())
			if !errors.Is(err, ErrRuntimeProviderProvision) {
				t.Fatalf("Provision() error = %v", err)
			}
			if name == "preexisting" && port.createCalls != 0 {
				t.Fatal("preexisting binding reached credential mutation")
			}
			if name == "substituted result" {
				if err := provider.Cleanup(context.Background(), CleanupRequest{
					RunID:        testRunID,
					ResourceKind: "provider",
					ResourceName: namesForRun(testRunID).Provider,
				}); err != nil {
					t.Fatalf("cleanup after substituted result = %v", err)
				}
			}
		})
	}
}

func TestRuntimeProviderOverlapPoisonsProvisioningWithoutRevokingCleanup(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})
	port := &runtimeProviderPort{createHook: func() {
		close(entered)
		<-release
	}}
	provider := newTestRuntimeProvider(t, port)
	first := make(chan error, 1)
	go func() {
		first <- provider.Provision(context.Background())
	}()
	<-entered
	if err := provider.Provision(context.Background()); !errors.Is(err, ErrRuntimeProviderOrder) {
		t.Fatalf("overlap error = %v", err)
	}
	close(release)
	if err := <-first; !errors.Is(err, ErrRuntimeProviderOrder) {
		t.Fatalf("first Provision() error = %v", err)
	}
	if err := provider.Cleanup(context.Background(), CleanupRequest{
		RunID:        testRunID,
		ResourceKind: "provider",
		ResourceName: namesForRun(testRunID).Provider,
	}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
}

func TestRuntimeProviderRejectsReplacementDuringCleanup(t *testing.T) {
	t.Parallel()

	port := &runtimeProviderPort{}
	provider := newTestRuntimeProvider(t, port)
	if err := provider.Provision(context.Background()); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	port.mu.Lock()
	port.binding.ID = "replacement"
	port.binding.ResourceVersion++
	port.mu.Unlock()
	err := provider.Cleanup(context.Background(), CleanupRequest{
		RunID:        testRunID,
		ResourceKind: "provider",
		ResourceName: namesForRun(testRunID).Provider,
	})
	if !errors.Is(err, ErrRuntimeProviderCleanup) || port.deleteCalls != 0 {
		t.Fatalf("Cleanup() error = %v, delete calls = %d", err, port.deleteCalls)
	}
}

func TestRuntimeProviderCleanupConvergesBeforeNativeMutation(t *testing.T) {
	t.Parallel()

	privateErr := errors.New("private observation detail")
	port := &runtimeProviderPort{observeErr: privateErr}
	provider := newTestRuntimeProvider(t, port)
	if err := provider.Provision(context.Background()); !errors.Is(
		err,
		ErrRuntimeProviderProvision,
	) || errors.Is(err, privateErr) {
		t.Fatalf("Provision() error = %v", err)
	}
	if err := provider.Cleanup(context.Background(), CleanupRequest{
		RunID:        testRunID,
		ResourceKind: "provider",
		ResourceName: namesForRun(testRunID).Provider,
	}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if port.createCalls != 0 || port.deleteCalls != 0 {
		t.Fatalf("native calls = create %d, delete %d", port.createCalls, port.deleteCalls)
	}
}

func TestRuntimeProviderRejectsInvalidInputsAndSerialization(t *testing.T) {
	t.Parallel()

	var typedNil *runtimeProviderPort
	for name, config := range map[string]RuntimeProviderConfig{
		"run":         {RunID: "invalid", Credentials: testRuntimeProviderCredentials(), Provider: &runtimeProviderPort{}},
		"credentials": {RunID: testRunID, Credentials: execution.RuntimeConformanceCredentials{}, Provider: &runtimeProviderPort{}},
		"provider":    {RunID: testRunID, Credentials: testRuntimeProviderCredentials(), Provider: typedNil},
	} {
		name, config := name, config
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewRuntimeProvider(config); !errors.Is(err, ErrRuntimeProviderConfiguration) {
				t.Fatalf("NewRuntimeProvider() error = %v", err)
			}
		})
	}
	config := RuntimeProviderConfig{
		RunID:       testRunID,
		Credentials: testRuntimeProviderCredentials(),
		Provider:    &runtimeProviderPort{},
	}
	provider := newTestRuntimeProvider(t, &runtimeProviderPort{})
	if _, err := json.Marshal(config); !errors.Is(err, ErrSerialization) {
		t.Fatalf("config MarshalJSON() error = %v", err)
	}
	if _, err := json.Marshal(provider); !errors.Is(err, ErrSerialization) {
		t.Fatalf("provider MarshalJSON() error = %v", err)
	}
}

type runtimeProviderPort struct {
	mu            sync.Mutex
	request       execution.RuntimeConformanceProviderRequest
	observed      map[string][]byte
	retained      [][]byte
	binding       execution.ProviderBinding
	returnBinding execution.ProviderBinding
	exists        bool
	createCalls   int
	deleteCalls   int
	createErr     error
	deleteErr     error
	observeErr    error
	createHook    func()
}

func (port *runtimeProviderPort) CreateRuntimeConformanceProvider(
	_ context.Context,
	request execution.RuntimeConformanceProviderRequest,
) (execution.ProviderBinding, error) {
	port.mu.Lock()
	port.createCalls++
	port.request = request
	port.observed = map[string][]byte{
		"access":  slices.Clone(request.Credentials.AccessToken),
		"refresh": slices.Clone(request.Credentials.RefreshToken),
		"account": slices.Clone(request.Credentials.AccountID),
		"id":      slices.Clone(request.Credentials.IDToken),
	}
	port.retained = [][]byte{
		request.Credentials.AccessToken,
		request.Credentials.RefreshToken,
		request.Credentials.AccountID,
		request.Credentials.IDToken,
	}
	port.binding = port.expectedBinding()
	port.exists = true
	hook := port.createHook
	result := port.returnBinding
	if result == (execution.ProviderBinding{}) {
		result = port.binding
	}
	err := port.createErr
	port.mu.Unlock()
	if hook != nil {
		hook()
	}
	return result, err
}

func (port *runtimeProviderPort) ObserveRuntimeConformanceProvider(
	_ context.Context,
	ref execution.RuntimeConformanceProviderRef,
) (execution.ProviderBindingObservation, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	return port.observation(ref.IsolationDomainID, ref.GatewayID, ref.Name), port.observeErr
}

func (port *runtimeProviderPort) ObserveProviderBinding(
	_ context.Context,
	ref execution.ProviderBindingRef,
) (execution.ProviderBindingObservation, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	return port.observation(ref.IsolationDomainID, ref.GatewayID, ref.Name), port.observeErr
}

func (port *runtimeProviderPort) DeleteProviderBinding(
	_ context.Context,
	ref execution.ProviderBindingRef,
) error {
	port.mu.Lock()
	defer port.mu.Unlock()
	port.deleteCalls++
	if port.binding.ID != ref.ID || port.binding.ResourceVersion != ref.ResourceVersion {
		return execution.ErrStateConflict
	}
	port.exists = false
	return port.deleteErr
}

func (port *runtimeProviderPort) observation(
	isolationDomainID string,
	gatewayID string,
	name string,
) execution.ProviderBindingObservation {
	observation := execution.ProviderBindingObservation{
		IsolationDomainID: isolationDomainID,
		GatewayID:         gatewayID,
		Name:              name,
		ObservedAt:        time.Now().UTC(),
	}
	if !port.exists {
		return observation
	}
	observation.Exists = true
	observation.ID = port.binding.ID
	observation.ResourceVersion = port.binding.ResourceVersion
	return observation
}

func (port *runtimeProviderPort) expectedBinding() execution.ProviderBinding {
	return execution.ProviderBinding{
		IsolationDomainID: runtimeIsolationDomain(testRunID),
		GatewayID:         namesForRun(testRunID).Gateway,
		ID:                "runtime-provider-id",
		Name:              namesForRun(testRunID).Provider,
		ResourceVersion:   7,
	}
}

func newTestRuntimeProvider(t *testing.T, port *runtimeProviderPort) *RuntimeProvider {
	t.Helper()
	provider, err := NewRuntimeProvider(RuntimeProviderConfig{
		RunID:       testRunID,
		Credentials: testRuntimeProviderCredentials(),
		Provider:    port,
	})
	if err != nil {
		t.Fatalf("NewRuntimeProvider() error = %v", err)
	}
	return provider
}

func testRuntimeProviderCredentials() execution.RuntimeConformanceCredentials {
	return execution.RuntimeConformanceCredentials{
		AccessToken:  []byte("access-value"),
		RefreshToken: []byte("refresh-value"),
		AccountID:    []byte("account-value"),
		IDToken:      []byte("id-value"),
	}
}

func runtimeProviderBytesCleared(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}
