package openshell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
)

const (
	managedLabel                    = "dataground.managed"
	operationLabel                  = "dataground.operation"
	isolationLabel                  = "dataground.isolation"
	executionLabel                  = "dataground.execution"
	createLabel                     = "dataground.create"
	createFingerprintLength         = 63
	providerProfilesSetting         = "providers_v2_enabled"
	providerSettingsRecoveryTimeout = 10 * time.Second
)

var (
	ErrNoGateway                    = execution.ErrNoGateway
	ErrExecutionMissing             = execution.ErrExecutionMissing
	ErrProviderFailure              = errors.New("execution provider operation failed")
	ErrProviderObservation          = errors.New("execution provider observation failed")
	ErrProviderSettingsObservation  = errors.New("execution provider settings observation failed")
	ErrProviderSettingsMutation     = errors.New("execution provider settings mutation failed")
	ErrProviderSettingsVerification = errors.New("execution provider settings verification failed")
)

type NativeFailureClass string

const (
	NativeFailureUnknown              NativeFailureClass = "unknown"
	NativeFailurePermission           NativeFailureClass = "permission"
	NativeFailureMissing              NativeFailureClass = "missing-path"
	NativeFailureImage                NativeFailureClass = "image"
	NativeFailureSupervisor           NativeFailureClass = "supervisor"
	NativeFailureProvider             NativeFailureClass = "provider"
	NativeFailurePolicy               NativeFailureClass = "policy"
	NativeFailureNetwork              NativeFailureClass = "network"
	NativeFailureTimeout              NativeFailureClass = "timeout"
	NativeFailureOverflow             NativeFailureClass = "diagnostic-overflow"
	NativeFailureArgument             NativeFailureClass = "argument"
	NativeFailureArgumentGateway      NativeFailureClass = "argument-gateway"
	NativeFailureArgumentName         NativeFailureClass = "argument-name"
	NativeFailureArgumentImage        NativeFailureClass = "argument-image"
	NativeFailureArgumentPolicy       NativeFailureClass = "argument-policy"
	NativeFailureArgumentAutoProvider NativeFailureClass = "argument-auto-provider"
	NativeFailureArgumentApproval     NativeFailureClass = "argument-approval"
	NativeFailureArgumentLabel        NativeFailureClass = "argument-label"
	NativeFailureArgumentProvider     NativeFailureClass = "argument-provider"
	NativeFailureArgumentCommand      NativeFailureClass = "argument-command"
	NativeFailureAuth                 NativeFailureClass = "authentication"
	NativeFailureConflict             NativeFailureClass = "conflict"
	NativeFailureStorage              NativeFailureClass = "storage"
	NativeFailureDriver               NativeFailureClass = "compute-driver"
)

type CommandResult struct {
	Stdout       []byte
	ExitCode     int
	FailureClass NativeFailureClass
	FailureHint  string
}

type nativeCommandError struct {
	class NativeFailureClass
	hint  string
}

func (err *nativeCommandError) Error() string {
	return ErrProviderFailure.Error()
}

func (err *nativeCommandError) Unwrap() error {
	return ErrProviderFailure
}

func NativeFailureHintOf(err error) string {
	var native *nativeCommandError
	if errors.As(err, &native) {
		return native.hint
	}
	return ""
}

func NativeFailureClassOf(err error) NativeFailureClass {
	var native *nativeCommandError
	if errors.As(err, &native) {
		return native.class
	}
	return NativeFailureUnknown
}

func IsNativeCommandFailure(err error) bool {
	var native *nativeCommandError
	return errors.As(err, &native)
}

func nativeCommandFailure(result CommandResult, causes ...error) error {
	class := result.FailureClass
	if class == "" {
		class = NativeFailureUnknown
	}
	joined := []error{
		ErrProviderFailure,
		&nativeCommandError{class: class, hint: result.FailureHint},
	}
	joined = append(joined, causes...)
	return errors.Join(joined...)
}

type Runner interface {
	Run(context.Context, string, ...string) (CommandResult, error)
	Start(context.Context, string, ...string) (execution.RuntimeSession, error)
}

type Config struct {
	Binary                   string
	ExpectedVersion          string
	PolicyWorkspace          *PolicyWorkspace
	ExportWorkspace          *ExportWorkspace
	Now                      func() time.Time
	StateStore               execution.StateStore
	ProviderProfiles         *execution.ProviderProfileRegistry
	CredentialProviderRunner CredentialProviderRunner
}

// Provider is the development OpenShell adapter. Gateway and sandbox
// coordinates stay in private state and never appear in returned resources.
type Provider struct {
	runner             Runner
	binary             string
	expected           string
	workspace          *PolicyWorkspace
	exports            *ExportWorkspace
	now                func() time.Time
	store              execution.StateStore
	profiles           *execution.ProviderProfileRegistry
	credentialProvider CredentialProviderRunner
}

func New(config Config, runner Runner) *Provider {
	binary := config.Binary
	if binary == "" {
		binary = "openshell"
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	store := config.StateStore
	if store == nil {
		store = execution.NewMemoryStateStore()
	}
	credentialProvider := config.CredentialProviderRunner
	if credentialProvider == nil {
		credentialProvider = ExecCredentialProviderRunner{}
	}
	return &Provider{
		runner: runner, binary: binary, expected: config.ExpectedVersion, workspace: config.PolicyWorkspace, exports: config.ExportWorkspace,
		now: now, store: store, profiles: config.ProviderProfiles, credentialProvider: credentialProvider,
	}
}

func (provider *Provider) Check(ctx context.Context) error {
	if provider.expected == "" {
		return errors.New("expected OpenShell version is required")
	}
	result, err := provider.runner.Run(ctx, provider.binary, "--version")
	if err != nil || result.ExitCode != 0 {
		return ErrProviderFailure
	}
	fields := strings.Fields(string(result.Stdout))
	if len(fields) == 0 || strings.TrimPrefix(fields[len(fields)-1], "v") != strings.TrimPrefix(provider.expected, "v") {
		return errors.New("OpenShell version does not match the certified profile")
	}
	return nil
}

type gatewaySettingsView struct {
	Scope    string            `json:"scope"`
	Settings map[string]string `json:"settings"`
}

// EnableProviderProfiles owns the pinned gateway-global opt-in required before
// a provider-bound sandbox can be created. The dedicated evidence gateway is
// observed before mutation and again on a cancellation-independent recovery
// context so a lost acknowledgement never permits an unsafe retry.
func (provider *Provider) EnableProviderProfiles(
	ctx context.Context,
	isolationDomainID string,
	gatewayID string,
) error {
	if ctx == nil {
		return ErrProviderFailure
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrProviderFailure, err)
	}
	gateway, err := provider.executionContext(ctx, isolationDomainID, gatewayID)
	if err != nil {
		return err
	}
	enabled, err := provider.providerProfilesEnabled(ctx, gateway.Endpoint)
	if err != nil {
		return err
	}
	if enabled {
		return nil
	}

	result, runErr := provider.runner.Run(
		ctx,
		provider.binary,
		provider.gatewayArgs(
			gateway.Endpoint,
			"settings",
			"set",
			"--global",
			"--key",
			providerProfilesSetting,
			"--value",
			"true",
			"--yes",
		)...,
	)
	clear(result.Stdout)

	recoveryCtx, cancel := context.WithTimeout(
		context.Background(),
		providerSettingsRecoveryTimeout,
	)
	defer cancel()
	enabled, observeErr := provider.providerProfilesEnabled(recoveryCtx, gateway.Endpoint)
	if observeErr == nil && enabled {
		return nil
	}
	if runErr != nil || result.ExitCode != 0 {
		return errors.Join(ErrProviderSettingsMutation, nativeCommandFailure(result))
	}
	if observeErr != nil {
		return observeErr
	}
	return errors.Join(ErrProviderFailure, ErrProviderSettingsVerification)
}

func (provider *Provider) providerProfilesEnabled(
	ctx context.Context,
	endpoint string,
) (bool, error) {
	result, runErr := provider.runner.Run(
		ctx,
		provider.binary,
		provider.gatewayArgs(
			endpoint,
			"settings",
			"get",
			"--global",
			"--json",
		)...,
	)
	if runErr != nil || result.ExitCode != 0 {
		clear(result.Stdout)
		return false, errors.Join(ErrProviderSettingsObservation, nativeCommandFailure(result))
	}
	var settings gatewaySettingsView
	err := json.Unmarshal(result.Stdout, &settings)
	clear(result.Stdout)
	if err != nil ||
		settings.Scope != "global" ||
		settings.Settings == nil {
		return false, errors.Join(ErrProviderFailure, ErrProviderSettingsObservation)
	}
	return settings.Settings[providerProfilesSetting] == "true", nil
}

func (provider *Provider) RegisterGateway(ctx context.Context, registration execution.GatewayRegistration) (execution.Gateway, error) {
	if registration.IsolationDomainID == "" || registration.ID == "" || registration.Driver == "" {
		return execution.Gateway{}, errors.New("isolation domain, gateway id, and driver are required")
	}
	parsed, err := url.Parse(registration.Endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return execution.Gateway{}, errors.New("gateway endpoint is invalid")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return execution.Gateway{}, errors.New("plaintext gateway endpoint must be loopback")
	}
	return provider.store.RegisterGateway(ctx, registration)
}

func (provider *Provider) SetGatewayState(ctx context.Context, isolationDomainID, gatewayID string, state execution.GatewayState) error {
	switch state {
	case execution.GatewayActive, execution.GatewayDraining, execution.GatewayUnavailable, execution.GatewayLost:
	default:
		return errors.New("invalid gateway state")
	}
	return provider.store.SetGatewayState(ctx, isolationDomainID, gatewayID, state)
}

func (provider *Provider) SelectGateway(ctx context.Context, request execution.PlacementRequest) (execution.Placement, error) {
	return provider.store.ReservePlacement(ctx, request)
}

func (provider *Provider) Create(ctx context.Context, request execution.CreateRequest) (execution.Execution, error) {
	if request.Placement.ID == "" || request.Placement.GatewayID == "" || request.IsolationDomainID == "" || request.OperationID == "" {
		return execution.Execution{}, errors.New("placement, isolation domain, and operation are required")
	}
	if !isDigestPinned(request.Image) {
		return execution.Execution{}, errors.New("sandbox image must be pinned by sha256 digest")
	}
	if err := provider.validatePlacement(ctx, request); err != nil {
		return execution.Execution{}, err
	}
	policy := slices.Clone(request.Policy)
	if err := execution.VerifyEnforcementPolicy(policy, request.PolicyDigest); err != nil {
		return execution.Execution{}, err
	}
	providerProfiles, err := provider.profiles.Resolve(request.ProviderProfiles)
	if err != nil {
		return execution.Execution{}, err
	}
	createFingerprint := fingerprintCreate(request, providerProfiles)
	gateway, err := provider.executionContext(ctx, request.IsolationDomainID, request.Placement.GatewayID)
	if err != nil {
		return execution.Execution{}, err
	}
	endpoint := gateway.Endpoint
	executionID := derivedID("exe", request.IsolationDomainID+":"+request.OperationID)
	sandbox := sandboxName(request.IsolationDomainID, request.OperationID)
	observed, found, observeErr := provider.observeByName(
		ctx, request.IsolationDomainID, endpoint, sandbox, executionID, request.Placement.GatewayID, createFingerprint,
	)
	if observeErr != nil {
		return execution.Execution{}, observeErr
	}
	if found {
		if err := provider.rememberExecution(ctx, observed, request.Placement.ID, request.OperationID, sandbox); err != nil {
			return execution.Execution{}, err
		}
		return observed, nil
	}
	if provider.workspace == nil {
		return execution.Execution{}, ErrPolicyWorkspaceUnavailable
	}
	policyPath, cleanup, err := provider.workspace.materialize(policy)
	if err != nil {
		return execution.Execution{}, err
	}
	defer func() { _ = cleanup() }()
	created := execution.Execution{
		IsolationDomainID: request.IsolationDomainID, ID: executionID,
		GatewayID: gateway.Gateway.ID, State: "provisioning",
	}
	if err := provider.rememberExecution(
		ctx,
		created,
		request.Placement.ID,
		request.OperationID,
		sandbox,
	); err != nil {
		return execution.Execution{}, err
	}
	args := provider.gatewayArgs(endpoint, "sandbox", "create", "--name", sandbox, "--from", request.Image,
		"--policy", policyPath, "--keep", "--no-auto-providers", "--approval-mode", "manual",
		"--label", managedLabel+"=true", "--label", operationLabel+"="+shortDigest(request.OperationID),
		"--label", isolationLabel+"="+shortDigest(request.IsolationDomainID), "--label", executionLabel+"="+executionID,
		"--label", createLabel+"="+createFingerprint)
	for _, profile := range providerProfiles {
		args = append(args, "--provider", profile)
	}
	args = append(args, "--", "true")
	result, runErr := provider.runner.Run(ctx, provider.binary, args...)
	cleanupErr := cleanup()
	if cleanupErr != nil {
		if runErr == nil && result.ExitCode == 0 {
			created := execution.Execution{
				IsolationDomainID: request.IsolationDomainID, ID: executionID,
				GatewayID: gateway.Gateway.ID, State: "provisioning",
			}
			if err := provider.rememberExecution(ctx, created, request.Placement.ID, request.OperationID, sandbox); err != nil {
				return execution.Execution{}, errors.Join(cleanupErr, err)
			}
		}
		if runErr != nil || result.ExitCode != 0 {
			return execution.Execution{}, nativeCommandFailure(result, cleanupErr)
		}
		return execution.Execution{}, cleanupErr
	}
	if runErr != nil || result.ExitCode != 0 {
		// Creation may have succeeded before acknowledgement was lost. Observe
		// the deterministic name before permitting a retry.
		observed, found, observeErr := provider.observeByName(
			ctx, request.IsolationDomainID, endpoint, sandbox, executionID, request.Placement.GatewayID, createFingerprint,
		)
		if observeErr == nil && found {
			if observed.State == "error" || observed.State == "deleting" || observed.State == "unknown" {
				return execution.Execution{}, nativeCommandFailure(result)
			}
			if err := provider.rememberExecution(ctx, observed, request.Placement.ID, request.OperationID, sandbox); err != nil {
				return execution.Execution{}, err
			}
			return observed, nil
		}
		if errors.Is(observeErr, execution.ErrStateConflict) {
			return execution.Execution{}, observeErr
		}
		return execution.Execution{}, nativeCommandFailure(result)
	}
	return created, nil
}

func (provider *Provider) Observe(ctx context.Context, ref execution.ExecutionRef) (execution.Observation, error) {
	entry, err := provider.store.GetExecution(ctx, ref)
	if err != nil {
		return execution.Observation{}, err
	}
	if entry.Execution.State == "terminated" {
		return execution.Observation{IsolationDomainID: ref.IsolationDomainID, ExecutionID: ref.ID, State: "terminated", ObservedAt: provider.now()}, nil
	}
	gateway, err := provider.executionContext(ctx, ref.IsolationDomainID, entry.Execution.GatewayID)
	if err != nil {
		return execution.Observation{}, err
	}
	result, err := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(gateway.Endpoint, "sandbox", "list", "--selector", managedLabel+"=true", "--output", "json")...)
	if err != nil || result.ExitCode != 0 {
		return execution.Observation{}, ErrProviderFailure
	}
	items, err := parseSandboxes(result.Stdout)
	if err != nil {
		return execution.Observation{}, ErrProviderFailure
	}
	for _, item := range items {
		if item.Name == entry.SandboxName {
			state := strings.ToLower(item.Phase)
			if err := provider.store.UpdateExecutionState(ctx, ref, state); err != nil {
				return execution.Observation{}, err
			}
			return execution.Observation{IsolationDomainID: ref.IsolationDomainID, ExecutionID: ref.ID, State: state, ObservedAt: provider.now()}, nil
		}
	}
	if err := provider.store.UpdateExecutionState(ctx, ref, "terminated"); err != nil {
		return execution.Observation{}, err
	}
	return execution.Observation{IsolationDomainID: ref.IsolationDomainID, ExecutionID: ref.ID, State: "terminated", ObservedAt: provider.now()}, nil
}

func (provider *Provider) StartRuntime(ctx context.Context, ref execution.ExecutionRef) (execution.RuntimeSession, error) {
	entry, gateway, err := provider.lookupExecution(ctx, ref)
	if err != nil {
		return nil, err
	}
	args := provider.gatewayArgs(gateway.Endpoint, "sandbox", "exec", "--name", entry.SandboxName, "--no-tty", "--", "codex", "app-server")
	session, err := provider.runner.Start(ctx, provider.binary, args...)
	if err != nil {
		return nil, ErrProviderFailure
	}
	return session, nil

}

func (provider *Provider) Logs(ctx context.Context, request execution.LogRequest) ([]byte, error) {
	ref := execution.ExecutionRef{IsolationDomainID: request.IsolationDomainID, ID: request.ExecutionID}
	entry, gateway, err := provider.lookupExecution(ctx, ref)
	if err != nil {
		return nil, err
	}
	lines := request.Lines
	if lines == 0 {
		lines = 200
	}
	result, err := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(gateway.Endpoint, "logs", entry.SandboxName, "-n", fmt.Sprint(lines))...)
	if err != nil || result.ExitCode != 0 {
		return nil, ErrProviderFailure
	}
	return result.Stdout, nil
}

func (provider *Provider) Export(ctx context.Context, request execution.ExportRequest) (execution.ExportResult, error) {
	if provider.exports == nil {
		return execution.ExportResult{}, ErrExportWorkspaceUnavailable
	}
	if request.SandboxPath == "" || !path.IsAbs(request.SandboxPath) ||
		path.Clean(request.SandboxPath) != request.SandboxPath ||
		strings.ContainsRune(request.SandboxPath, '\x00') {
		return execution.ExportResult{}, errors.New("sandbox export path must be clean and absolute")
	}
	ref := execution.ExecutionRef{IsolationDomainID: request.IsolationDomainID, ID: request.ExecutionID}
	entry, gateway, err := provider.lookupExecution(ctx, ref)
	if err != nil {
		return execution.ExportResult{}, err
	}
	destination, cleanup, err := provider.exports.destination()
	if err != nil {
		return execution.ExportResult{}, err
	}
	result, runErr := provider.runner.Run(
		ctx,
		provider.binary,
		provider.gatewayArgs(
			gateway.Endpoint,
			"sandbox",
			"download",
			entry.SandboxName,
			request.SandboxPath,
			destination,
		)...,
	)
	if runErr != nil || result.ExitCode != 0 {
		return execution.ExportResult{}, errors.Join(ErrProviderFailure, cleanup())
	}
	content, consumeErr := provider.exports.consume(destination)
	cleanupErr := cleanup()
	if consumeErr != nil || cleanupErr != nil {
		return execution.ExportResult{}, errors.Join(consumeErr, cleanupErr)
	}
	return execution.ExportResult{
		IsolationDomainID: request.IsolationDomainID,
		ExecutionID:       request.ExecutionID,
		Content:           content,
	}, nil
}

func (provider *Provider) Terminate(ctx context.Context, ref execution.ExecutionRef) error {
	entry, err := provider.store.GetExecution(ctx, ref)
	if errors.Is(err, ErrExecutionMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	if entry.Execution.State == "terminated" {
		return nil
	}
	gateway, err := provider.executionContext(ctx, ref.IsolationDomainID, entry.Execution.GatewayID)
	if err != nil {
		return err
	}
	result, runErr := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(gateway.Endpoint, "sandbox", "delete", entry.SandboxName)...)
	if runErr != nil || result.ExitCode != 0 {
		observation, observeErr := provider.Observe(ctx, ref)
		if observeErr == nil && observation.State == "terminated" {
			return nil
		}
		return ErrProviderFailure
	}
	return provider.store.UpdateExecutionState(ctx, ref, "terminated")
}

func (provider *Provider) ListOrphans(ctx context.Context, isolationDomainID, gatewayID string, known map[string]struct{}) ([]execution.Orphan, error) {
	gateway, err := provider.executionContext(ctx, isolationDomainID, gatewayID)
	if err != nil {
		return nil, err
	}
	result, err := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(gateway.Endpoint, "sandbox", "list", "--selector", managedLabel+"=true", "--output", "json")...)
	if err != nil || result.ExitCode != 0 {
		return nil, ErrProviderFailure
	}
	items, err := parseSandboxes(result.Stdout)
	if err != nil {
		return nil, ErrProviderFailure
	}
	orphans := make([]execution.Orphan, 0)
	for _, item := range items {
		candidate := item.Labels[executionLabel]
		if candidate == "" {
			continue
		}
		if _, ok := known[candidate]; !ok {
			orphans = append(orphans, execution.Orphan{IsolationDomainID: isolationDomainID, ID: candidate, GatewayID: gatewayID})
		}
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].ID < orphans[j].ID })
	return orphans, nil
}

func (provider *Provider) executionContext(ctx context.Context, isolationDomainID, gatewayID string) (execution.GatewayRecord, error) {
	record, err := provider.store.GetGateway(ctx, isolationDomainID, gatewayID)
	if err != nil {
		return execution.GatewayRecord{}, err
	}
	if record.Gateway.State == execution.GatewayLost || record.Gateway.State == execution.GatewayUnavailable {
		return execution.GatewayRecord{}, ErrNoGateway
	}
	return record, nil
}

func (provider *Provider) validatePlacement(ctx context.Context, request execution.CreateRequest) error {
	expectedID := derivedID("plc", request.IsolationDomainID+":"+request.OperationID)
	if request.Placement.ID != expectedID {
		return errors.New("placement does not match the requested operation")
	}
	stored, err := provider.store.GetPlacement(ctx, request.IsolationDomainID, request.Placement.ID)
	if err != nil || stored != request.Placement || request.Placement.IsolationDomainID != request.IsolationDomainID {
		return errors.New("placement is not reserved")
	}
	return nil
}

func (provider *Provider) lookupExecution(ctx context.Context, ref execution.ExecutionRef) (execution.ExecutionRecord, execution.GatewayRecord, error) {
	entry, err := provider.store.GetExecution(ctx, ref)
	if err != nil {
		return execution.ExecutionRecord{}, execution.GatewayRecord{}, err
	}
	gateway, err := provider.executionContext(ctx, ref.IsolationDomainID, entry.Execution.GatewayID)
	if err != nil {
		return execution.ExecutionRecord{}, execution.GatewayRecord{}, err
	}
	return entry, gateway, nil
}

func (provider *Provider) rememberExecution(ctx context.Context, value execution.Execution, placementID, operationID, sandbox string) error {
	if err := provider.store.SaveExecution(ctx, execution.ExecutionRecord{
		Execution: value, PlacementID: placementID, OperationID: operationID, SandboxName: sandbox,
	}); err != nil {
		return err
	}
	return provider.store.UpdateExecutionState(
		ctx,
		execution.ExecutionRef{
			IsolationDomainID: value.IsolationDomainID,
			ID:                value.ID,
		},
		value.State,
	)
}

func (provider *Provider) observeByName(
	ctx context.Context,
	isolationDomainID string,
	endpoint string,
	sandbox string,
	executionID string,
	gatewayID string,
	createFingerprint string,
) (execution.Execution, bool, error) {
	result, err := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(endpoint, "sandbox", "list", "--selector", managedLabel+"=true", "--output", "json")...)
	if err != nil || result.ExitCode != 0 {
		return execution.Execution{}, false, errors.Join(
			ErrProviderObservation,
			nativeCommandFailure(result),
		)
	}
	items, err := parseSandboxes(result.Stdout)
	if err != nil {
		return execution.Execution{}, false, errors.Join(
			ErrProviderFailure,
			ErrProviderObservation,
		)
	}
	for _, item := range items {
		if item.Name == sandbox {
			if item.Labels[createLabel] != createFingerprint {
				return execution.Execution{}, false, execution.ErrStateConflict
			}
			return execution.Execution{
				IsolationDomainID: isolationDomainID, ID: executionID,
				GatewayID: gatewayID, State: strings.ToLower(item.Phase),
			}, true, nil
		}
	}
	return execution.Execution{}, false, nil
}

func fingerprintCreate(request execution.CreateRequest, providerProfiles []string) string {
	encoded, _ := json.Marshal(struct {
		IsolationDomainID string   `json:"isolationDomainId"`
		OperationID       string   `json:"operationId"`
		Image             string   `json:"image"`
		PolicyDigest      string   `json:"policyDigest"`
		ProviderProfiles  []string `json:"providerProfiles"`
	}{
		IsolationDomainID: request.IsolationDomainID,
		OperationID:       request.OperationID,
		Image:             request.Image,
		PolicyDigest:      request.PolicyDigest,
		ProviderProfiles:  providerProfiles,
	})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])[:createFingerprintLength]
}

func (provider *Provider) gatewayArgs(endpoint string, args ...string) []string {
	return append([]string{"--gateway-endpoint", endpoint}, args...)
}

type sandboxView struct {
	Name   string            `json:"name"`
	Phase  string            `json:"phase"`
	Labels map[string]string `json:"labels"`
}

func parseSandboxes(value []byte) ([]sandboxView, error) {
	var items []sandboxView
	if err := json.Unmarshal(value, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func isDigestPinned(image string) bool {
	parts := strings.Split(image, "@sha256:")
	if len(parts) != 2 || parts[0] == "" || len(parts[1]) != 64 {
		return false
	}
	_, err := hex.DecodeString(parts[1])
	return err == nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func shortDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:12])
}

func derivedID(prefix, seed string) string { return identity.Derived(prefix, seed) }

func sandboxName(isolationDomainID, operationID string) string {
	return "dg-" + shortDigest(isolationDomainID+":"+operationID)
}

var _ execution.ExecutionProvider = (*Provider)(nil)
