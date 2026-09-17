package runtimeevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/openshell"
)

var (
	ErrDevelopmentProvider        = errors.New("development provider unavailable; retain the deployment records and inspect the exact request")
	developmentOperatorPattern    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)
	developmentCorrelationPattern = regexp.MustCompile(`^cor_[0-9a-z]{20,32}$`)
)

// DevelopmentProviderConfig identifies operator-owned deployment setup. Source
// values never enter this request, its receipt, or platform workload state.
type DevelopmentProviderConfig struct {
	Gateway             DevelopmentGatewayConfig `json:"gateway"`
	OpenShellBinary     string                   `json:"openshellBinary"`
	CredentialDirectory string                   `json:"credentialDirectory"`
	ActorID             string                   `json:"actorId"`
	CorrelationID       string                   `json:"correlationId"`
}

type DevelopmentProviderReceipt struct {
	Contract              string `json:"contract"`
	IsolationDomainID     string `json:"isolationDomainId"`
	ServiceID             string `json:"serviceId"`
	RevisionID            string `json:"revisionId"`
	RunID                 string `json:"runId"`
	ProviderProfile       string `json:"providerProfile"`
	ActorID               string `json:"actorId"`
	CorrelationID         string `json:"correlationId"`
	State                 string `json:"state"`
	CertificationEligible bool   `json:"certificationEligible"`
}

type developmentProviderRecord struct {
	Receipt         DevelopmentProviderReceipt `json:"receipt"`
	Gateway         DevelopmentGatewayReceipt  `json:"gateway"`
	BindingID       string                     `json:"bindingId"`
	ResourceVersion uint64                     `json:"resourceVersion"`
}

type developmentProviderPort interface {
	Check(context.Context) error
	EnableProviderProfiles(context.Context, string, string) error
	CheckDevelopmentProviderProfiles(context.Context, string, string) error
	CreateDevelopmentProvider(context.Context, string, string, execution.RuntimeConformanceCredentials) (execution.ProviderBinding, error)
	ObserveDevelopmentProvider(context.Context, string, string) (execution.ProviderBindingObservation, error)
}

type developmentProviderDependencies struct {
	gateway developmentGatewayDependencies
	open    func(context.Context, DevelopmentProviderConfig) (developmentProviderPort, error)
	load    func(context.Context, string) (execution.RuntimeConformanceCredentials, error)
}

// RunDevelopmentProvider installs once or inspects the exact fixed Codex
// binding. It neither rotates credentials nor grants service execution access.
func RunDevelopmentProvider(ctx context.Context, action string, config DevelopmentProviderConfig) (DevelopmentProviderReceipt, error) {
	if runtime.GOOS != "linux" || (config.Gateway.SupervisorLocalImageID != "" && runtime.GOARCH != "arm64") {
		return DevelopmentProviderReceipt{}, ErrDevelopmentProvider
	}
	return runDevelopmentProvider(ctx, action, config, developmentProviderDependencies{
		gateway: defaultDevelopmentGatewayDependencies(), open: openDevelopmentProvider,
		load: loadDevelopmentProviderCredentials,
	})
}

func runDevelopmentProvider(ctx context.Context, action string, config DevelopmentProviderConfig, deps developmentProviderDependencies) (DevelopmentProviderReceipt, error) {
	empty := DevelopmentProviderReceipt{}
	if ctx == nil || (action != "install" && action != "inspect") || !config.valid() || deps.open == nil || deps.load == nil {
		return empty, ErrDevelopmentProvider
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	root, err := resolveRuntimeTopologyDirectory(config.Gateway.WorkspaceRoot, true)
	if err != nil || root != config.Gateway.WorkspaceRoot {
		return empty, ErrDevelopmentProvider
	}
	lock, err := lockDevelopmentGateway(root, config.Gateway.RunID)
	if err != nil {
		return empty, ErrDevelopmentProvider
	}
	defer closeDevelopmentGatewayLock(lock)
	// Hold the gateway's own operator lock through both observations and the
	// effect, so stop cannot race this command through the supported interface.
	gateway, err := runLockedDevelopmentGateway(ctx, "inspect", config.Gateway, deps.gateway)
	if err != nil {
		return empty, ErrDevelopmentProvider
	}
	prefix := filepath.Join(root, "dg-runtime-deployment-"+config.Gateway.RunID+".provider")
	request, _ := json.Marshal(config)
	if immutableDevelopmentRecord(prefix+".request.json", request, action == "install") != nil {
		return empty, ErrDevelopmentProvider
	}
	intent, err := readDevelopmentRecord(prefix+".create", []byte("create\n"))
	if err != nil {
		return empty, ErrDevelopmentProvider
	}
	receipt := DevelopmentProviderReceipt{Contract: "dataground.dev.provider-deployment/v1", IsolationDomainID: config.Gateway.IsolationDomainID, ServiceID: config.Gateway.ServiceID, RevisionID: config.Gateway.RevisionID, RunID: config.Gateway.RunID, ProviderProfile: "codex", ActorID: config.ActorID, CorrelationID: config.CorrelationID, State: "configured"}
	var record developmentProviderRecord
	content, err := readDevelopmentBytes(prefix + ".receipt.json")
	if err == nil {
		if json.Unmarshal(content, &record) != nil {
			return empty, ErrDevelopmentProvider
		}
		canonical, _ := json.Marshal(record)
		if !intent || !bytes.Equal(content, canonical) || record.Receipt != receipt || record.Gateway != gateway || record.BindingID == "" || record.ResourceVersion == 0 {
			return empty, ErrDevelopmentProvider
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return empty, ErrDevelopmentProvider
	}
	if action == "inspect" && !intent {
		return empty, ErrDevelopmentProvider
	}
	port, err := deps.open(ctx, config)
	if err != nil || isNilHarnessPort(port) || port.Check(ctx) != nil {
		return empty, ErrDevelopmentProvider
	}
	isolationID, gatewayID := config.Gateway.IsolationDomainID, namesForRun(config.Gateway.RunID).Gateway
	observe := func() (execution.ProviderBindingObservation, error) {
		started := time.Now().UTC()
		value, err := port.ObserveDevelopmentProvider(ctx, isolationID, gatewayID)
		if err != nil || ctx.Err() != nil || value.IsolationDomainID != isolationID || value.GatewayID != gatewayID || value.Name != "codex" || value.ObservedAt.Before(started) || value.ObservedAt.After(time.Now().UTC()) || (value.Exists && (value.ID == "" || value.ResourceVersion == 0)) || (!value.Exists && (value.ID != "" || value.ResourceVersion != 0)) {
			return execution.ProviderBindingObservation{}, ErrDevelopmentProvider
		}
		return value, nil
	}
	observed, err := observe()
	if err != nil {
		return empty, ErrDevelopmentProvider
	}
	if !intent {
		if observed.Exists || action != "install" || port.EnableProviderProfiles(ctx, isolationID, gatewayID) != nil {
			return empty, ErrDevelopmentProvider
		}
		credentials, err := deps.load(ctx, config.CredentialDirectory)
		defer clearRuntimeProviderCredentials(&credentials)
		if err != nil || !validRuntimeProviderCredentials(credentials) || ctx.Err() != nil {
			return empty, ErrDevelopmentProvider
		}
		// The source is consumed before publication of intent. Once intent is
		// durable, a retry may observe but can never repeat credential transfer.
		if immutableDevelopmentRecord(prefix+".create", []byte("create\n"), true) != nil {
			return empty, ErrDevelopmentProvider
		}
		binding, _ := port.CreateDevelopmentProvider(ctx, isolationID, gatewayID, credentials)
		clearRuntimeProviderCredentials(&credentials)
		observed, err = observe()
		if err != nil || (!emptyRuntimeProviderBinding(binding) && !runtimeProviderBindingMatches(binding, observed)) {
			return empty, ErrDevelopmentProvider
		}
	}
	if !observed.Exists || port.CheckDevelopmentProviderProfiles(ctx, isolationID, gatewayID) != nil {
		return empty, ErrDevelopmentProvider
	}
	if record.BindingID != "" && (record.BindingID != observed.ID || record.ResourceVersion != observed.ResourceVersion) {
		return empty, ErrDevelopmentProvider
	}
	after, err := runLockedDevelopmentGateway(ctx, "inspect", config.Gateway, deps.gateway)
	if err != nil || after != gateway || ctx.Err() != nil {
		return empty, ErrDevelopmentProvider
	}
	record = developmentProviderRecord{Receipt: receipt, Gateway: gateway, BindingID: observed.ID, ResourceVersion: observed.ResourceVersion}
	content, _ = json.Marshal(record)
	if immutableDevelopmentRecord(prefix+".receipt.json", content, true) != nil {
		return empty, ErrDevelopmentProvider
	}
	return receipt, nil
}

func (config DevelopmentProviderConfig) valid() bool {
	if !config.Gateway.valid() || !developmentOperatorPattern.MatchString(config.ActorID) || !developmentCorrelationPattern.MatchString(config.CorrelationID) {
		return false
	}
	for _, path := range []string{config.OpenShellBinary, config.CredentialDirectory} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsRune(path, 0) {
			return false
		}
	}
	return !strings.ContainsRune(config.OpenShellBinary, os.PathListSeparator) && !runtimeTopologyPathsOverlap(config.CredentialDirectory, config.Gateway.RepositoryRoot) && !runtimeTopologyPathsOverlap(config.CredentialDirectory, config.Gateway.WorkspaceRoot)
}

func openDevelopmentProvider(ctx context.Context, config DevelopmentProviderConfig) (developmentProviderPort, error) {
	binary, err := resolveRuntimeTopologyBinary(config.OpenShellBinary)
	if err != nil || binary != config.OpenShellBinary {
		return nil, ErrDevelopmentProvider
	}
	// This memory store holds only command-local gateway addressing. OpenShell
	// owns durable credentials; the immutable private receipt pins its binding.
	provider := openshell.New(openshell.Config{Binary: binary, ExpectedVersion: runtimeLauncherOpenShellVersion, StateStore: execution.NewMemoryStateStore()}, openshell.ExecRunner{Environment: runtimeLauncherOpenShellEnvironment(config.Gateway.WorkspaceRoot)})
	_, err = provider.RegisterGateway(ctx, execution.GatewayRegistration{IsolationDomainID: config.Gateway.IsolationDomainID, ID: namesForRun(config.Gateway.RunID).Gateway, Endpoint: gatewayEndpoint, Driver: driver, Capabilities: []string{openShellRuntimeCapability}})
	if err != nil {
		return nil, ErrDevelopmentProvider
	}
	return provider, nil
}

func loadDevelopmentProviderCredentials(ctx context.Context, directory string) (execution.RuntimeConformanceCredentials, error) {
	source, err := NewRuntimeCredentialSource(CredentialSourceConfig{Directory: directory})
	if err != nil {
		return execution.RuntimeConformanceCredentials{}, err
	}
	// load consumes the owned source even on read failure. Close any
	// remaining handles without deleting a substituted path on failure.
	defer source.state.closeHandles()
	return source.load(ctx)
}
