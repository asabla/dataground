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
	"os"
	"sort"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/execution"
)

const (
	managedLabel   = "dataground.managed"
	operationLabel = "dataground.operation"
	isolationLabel = "dataground.isolation"
	executionLabel = "dataground.execution"
)

var (
	ErrNoGateway        = execution.ErrNoGateway
	ErrExecutionMissing = execution.ErrExecutionMissing
	ErrProviderFailure  = errors.New("execution provider operation failed")
)

type CommandResult struct {
	Stdout   []byte
	ExitCode int
}

type Runner interface {
	Run(context.Context, string, ...string) (CommandResult, error)
	Start(context.Context, string, ...string) (execution.RuntimeSession, error)
}

type Config struct {
	Binary          string
	ExpectedVersion string
	Now             func() time.Time
	StateStore      execution.StateStore
}

// Provider is the development OpenShell adapter. Gateway and sandbox
// coordinates stay in private state and never appear in returned resources.
type Provider struct {
	runner   Runner
	binary   string
	expected string
	now      func() time.Time
	store    execution.StateStore
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
	return &Provider{
		runner: runner, binary: binary, expected: config.ExpectedVersion, now: now, store: store,
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
	if err := verifyFileDigest(request.PolicyPath, request.PolicySHA256); err != nil {
		return execution.Execution{}, err
	}
	gateway, err := provider.executionContext(ctx, request.IsolationDomainID, request.Placement.GatewayID)
	if err != nil {
		return execution.Execution{}, err
	}
	endpoint := gateway.Endpoint
	executionID := derivedID("exe", request.IsolationDomainID+":"+request.OperationID)
	sandbox := sandboxName(request.IsolationDomainID, request.OperationID)
	observed, found, observeErr := provider.observeByName(
		ctx, request.IsolationDomainID, endpoint, sandbox, executionID, request.Placement.GatewayID,
	)
	if observeErr != nil {
		return execution.Execution{}, ErrProviderFailure
	}
	if found {
		if err := provider.rememberExecution(ctx, observed, request.Placement.ID, request.OperationID, sandbox); err != nil {
			return execution.Execution{}, err
		}
		return observed, nil
	}
	args := provider.gatewayArgs(endpoint, "sandbox", "create", "--name", sandbox, "--from", request.Image,
		"--policy", request.PolicyPath, "--no-auto-providers", "--approval-mode", "manual",
		"--label", managedLabel+"=true", "--label", operationLabel+"="+shortDigest(request.OperationID),
		"--label", isolationLabel+"="+shortDigest(request.IsolationDomainID), "--label", executionLabel+"="+executionID)
	for _, profile := range request.ProviderProfiles {
		if profile == "" || strings.ContainsAny(profile, "=\x00\n\r") {
			return execution.Execution{}, errors.New("provider profile name is invalid")
		}
		args = append(args, "--provider", profile)
	}
	args = append(args, "--", "true")
	result, runErr := provider.runner.Run(ctx, provider.binary, args...)
	if runErr != nil || result.ExitCode != 0 {
		// Creation may have succeeded before acknowledgement was lost. Observe
		// the deterministic name before permitting a retry.
		observed, found, observeErr := provider.observeByName(
			ctx, request.IsolationDomainID, endpoint, sandbox, executionID, request.Placement.GatewayID,
		)
		if observeErr == nil && found {
			if err := provider.rememberExecution(ctx, observed, request.Placement.ID, request.OperationID, sandbox); err != nil {
				return execution.Execution{}, err
			}
			return observed, nil
		}
		return execution.Execution{}, ErrProviderFailure
	}
	created := execution.Execution{
		IsolationDomainID: request.IsolationDomainID, ID: executionID,
		GatewayID: gateway.Gateway.ID, State: "provisioning",
	}
	if err := provider.rememberExecution(ctx, created, request.Placement.ID, request.OperationID, sandbox); err != nil {
		return execution.Execution{}, err
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
	ref := execution.ExecutionRef{IsolationDomainID: request.IsolationDomainID, ID: request.ExecutionID}
	entry, gateway, err := provider.lookupExecution(ctx, ref)
	if err != nil {
		return execution.ExportResult{}, err
	}
	if request.SandboxPath == "" || request.Destination == "" {
		return execution.ExportResult{}, errors.New("sandbox path and destination are required")
	}
	result, err := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(gateway.Endpoint, "sandbox", "download", entry.SandboxName, request.SandboxPath, request.Destination)...)
	if err != nil || result.ExitCode != 0 {
		return execution.ExportResult{}, ErrProviderFailure
	}
	return execution.ExportResult{IsolationDomainID: request.IsolationDomainID, ExecutionID: request.ExecutionID, Destination: request.Destination}, nil
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
	return provider.store.SaveExecution(ctx, execution.ExecutionRecord{
		Execution: value, PlacementID: placementID, OperationID: operationID, SandboxName: sandbox,
	})
}

func (provider *Provider) observeByName(
	ctx context.Context,
	isolationDomainID string,
	endpoint string,
	sandbox string,
	executionID string,
	gatewayID string,
) (execution.Execution, bool, error) {
	result, err := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(endpoint, "sandbox", "list", "--selector", managedLabel+"=true", "--output", "json")...)
	if err != nil || result.ExitCode != 0 {
		return execution.Execution{}, false, ErrProviderFailure
	}
	items, err := parseSandboxes(result.Stdout)
	if err != nil {
		return execution.Execution{}, false, err
	}
	for _, item := range items {
		if item.Name == sandbox {
			return execution.Execution{
				IsolationDomainID: isolationDomainID, ID: executionID,
				GatewayID: gatewayID, State: strings.ToLower(item.Phase),
			}, true, nil
		}
	}
	return execution.Execution{}, false, nil
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

func verifyFileDigest(path, expected string) error {
	if path == "" || expected == "" {
		return errors.New("policy path and sha256 are required")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return errors.New("read enforcement policy")
	}
	digest := sha256.Sum256(content)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), strings.TrimPrefix(expected, "sha256:")) {
		return errors.New("enforcement policy digest does not match")
	}
	return nil
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

func derivedID(prefix, seed string) string { return prefix + "_" + shortDigest(seed) }

func sandboxName(isolationDomainID, operationID string) string {
	return "dg-" + shortDigest(isolationDomainID+":"+operationID)
}

var _ execution.ExecutionProvider = (*Provider)(nil)
