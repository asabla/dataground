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
	"sync"
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
	ErrNoGateway        = errors.New("no eligible execution gateway")
	ErrGatewayExists    = errors.New("execution gateway already registered")
	ErrExecutionMissing = errors.New("execution not found")
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
}

type gatewayEntry struct {
	gateway  execution.Gateway
	endpoint string
	reserved int
}

type executionEntry struct {
	execution execution.Execution
	sandbox   string
}

// Provider is the development OpenShell adapter. Gateway and sandbox
// coordinates stay in private state and never appear in returned resources.
type Provider struct {
	mu         sync.Mutex
	runner     Runner
	binary     string
	expected   string
	now        func() time.Time
	gateways   map[string]*gatewayEntry
	placements map[string]execution.Placement
	executions map[string]executionEntry
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
	return &Provider{
		runner: runner, binary: binary, expected: config.ExpectedVersion, now: now,
		gateways: make(map[string]*gatewayEntry), placements: make(map[string]execution.Placement),
		executions: make(map[string]executionEntry),
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

func (provider *Provider) RegisterGateway(_ context.Context, registration execution.GatewayRegistration) (execution.Gateway, error) {
	if registration.ID == "" || registration.Driver == "" {
		return execution.Gateway{}, errors.New("gateway id and driver are required")
	}
	parsed, err := url.Parse(registration.Endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return execution.Gateway{}, errors.New("gateway endpoint is invalid")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return execution.Gateway{}, errors.New("plaintext gateway endpoint must be loopback")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if _, exists := provider.gateways[registration.ID]; exists {
		return execution.Gateway{}, ErrGatewayExists
	}
	gateway := execution.Gateway{
		ID: registration.ID, Driver: registration.Driver, State: execution.GatewayActive,
		Capabilities: append([]string(nil), registration.Capabilities...),
	}
	provider.gateways[registration.ID] = &gatewayEntry{gateway: gateway, endpoint: registration.Endpoint}
	return gateway, nil
}

func (provider *Provider) SetGatewayState(_ context.Context, gatewayID string, state execution.GatewayState) error {
	switch state {
	case execution.GatewayActive, execution.GatewayDraining, execution.GatewayUnavailable, execution.GatewayLost:
	default:
		return errors.New("invalid gateway state")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	entry, ok := provider.gateways[gatewayID]
	if !ok {
		return ErrNoGateway
	}
	entry.gateway.State = state
	return nil
}

func (provider *Provider) SelectGateway(_ context.Context, request execution.PlacementRequest) (execution.Placement, error) {
	if request.IsolationDomainID == "" || request.OperationID == "" {
		return execution.Placement{}, errors.New("isolation domain and operation are required")
	}
	placementID := derivedID("plc", request.IsolationDomainID+":"+request.OperationID)
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if placement, ok := provider.placements[placementID]; ok {
		return placement, nil
	}
	eligible := make([]*gatewayEntry, 0, len(provider.gateways))
	for _, entry := range provider.gateways {
		if entry.gateway.State == execution.GatewayActive && containsAll(entry.gateway.Capabilities, request.RequiredCapabilities) {
			eligible = append(eligible, entry)
		}
	}
	if len(eligible) == 0 {
		return execution.Placement{}, ErrNoGateway
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].reserved == eligible[j].reserved {
			return eligible[i].gateway.ID < eligible[j].gateway.ID
		}
		return eligible[i].reserved < eligible[j].reserved
	})
	selected := eligible[0]
	selected.reserved++
	placement := execution.Placement{ID: placementID, GatewayID: selected.gateway.ID}
	provider.placements[placementID] = placement
	return placement, nil
}

func (provider *Provider) Create(ctx context.Context, request execution.CreateRequest) (execution.Execution, error) {
	if request.Placement.ID == "" || request.Placement.GatewayID == "" || request.IsolationDomainID == "" || request.OperationID == "" {
		return execution.Execution{}, errors.New("placement, isolation domain, and operation are required")
	}
	if !isDigestPinned(request.Image) {
		return execution.Execution{}, errors.New("sandbox image must be pinned by sha256 digest")
	}
	if err := provider.validatePlacement(request); err != nil {
		return execution.Execution{}, err
	}
	if err := verifyFileDigest(request.PolicyPath, request.PolicySHA256); err != nil {
		return execution.Execution{}, err
	}
	entry, endpoint, err := provider.executionContext(request.Placement.GatewayID)
	if err != nil {
		return execution.Execution{}, err
	}
	executionID := derivedID("exe", request.IsolationDomainID+":"+request.OperationID)
	sandbox := sandboxName(request.IsolationDomainID, request.OperationID)
	observed, found, observeErr := provider.observeByName(ctx, endpoint, sandbox, executionID, request.Placement.GatewayID)
	if observeErr != nil {
		return execution.Execution{}, ErrProviderFailure
	}
	if found {
		provider.rememberExecution(observed, sandbox)
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
		observed, found, observeErr := provider.observeByName(ctx, endpoint, sandbox, executionID, request.Placement.GatewayID)
		if observeErr == nil && found {
			provider.rememberExecution(observed, sandbox)
			return observed, nil
		}
		return execution.Execution{}, ErrProviderFailure
	}
	execution := execution.Execution{ID: executionID, GatewayID: entry.gateway.ID, State: "provisioning"}
	provider.rememberExecution(execution, sandbox)
	return execution, nil
}

func (provider *Provider) Observe(ctx context.Context, executionID string) (execution.Observation, error) {
	entry, gateway, err := provider.lookupExecution(executionID)
	if err != nil {
		return execution.Observation{}, err
	}
	result, err := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(gateway.endpoint, "sandbox", "list", "--selector", managedLabel+"=true", "--output", "json")...)
	if err != nil || result.ExitCode != 0 {
		return execution.Observation{}, ErrProviderFailure
	}
	items, err := parseSandboxes(result.Stdout)
	if err != nil {
		return execution.Observation{}, ErrProviderFailure
	}
	for _, item := range items {
		if item.Name == entry.sandbox {
			return execution.Observation{ExecutionID: executionID, State: strings.ToLower(item.Phase), ObservedAt: provider.now()}, nil
		}
	}
	return execution.Observation{ExecutionID: executionID, State: "terminated", ObservedAt: provider.now()}, nil
}

func (provider *Provider) StartRuntime(ctx context.Context, executionID string) (execution.RuntimeSession, error) {
	entry, gateway, err := provider.lookupExecution(executionID)
	if err != nil {
		return nil, err
	}
	args := provider.gatewayArgs(gateway.endpoint, "sandbox", "exec", "--name", entry.sandbox, "--no-tty", "--", "codex", "app-server")
	session, err := provider.runner.Start(ctx, provider.binary, args...)
	if err != nil {
		return nil, ErrProviderFailure
	}
	return session, nil

}

func (provider *Provider) Logs(ctx context.Context, request execution.LogRequest) ([]byte, error) {
	entry, gateway, err := provider.lookupExecution(request.ExecutionID)
	if err != nil {
		return nil, err
	}
	lines := request.Lines
	if lines == 0 {
		lines = 200
	}
	result, err := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(gateway.endpoint, "logs", entry.sandbox, "-n", fmt.Sprint(lines))...)
	if err != nil || result.ExitCode != 0 {
		return nil, ErrProviderFailure
	}
	return result.Stdout, nil
}

func (provider *Provider) Export(ctx context.Context, request execution.ExportRequest) (execution.ExportResult, error) {
	entry, gateway, err := provider.lookupExecution(request.ExecutionID)
	if err != nil {
		return execution.ExportResult{}, err
	}
	if request.SandboxPath == "" || request.Destination == "" {
		return execution.ExportResult{}, errors.New("sandbox path and destination are required")
	}
	result, err := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(gateway.endpoint, "sandbox", "download", entry.sandbox, request.SandboxPath, request.Destination)...)
	if err != nil || result.ExitCode != 0 {
		return execution.ExportResult{}, ErrProviderFailure
	}
	return execution.ExportResult{ExecutionID: request.ExecutionID, Destination: request.Destination}, nil
}

func (provider *Provider) Terminate(ctx context.Context, executionID string) error {
	entry, gateway, err := provider.lookupExecution(executionID)
	if errors.Is(err, ErrExecutionMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	result, runErr := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(gateway.endpoint, "sandbox", "delete", entry.sandbox)...)
	if runErr != nil || result.ExitCode != 0 {
		observation, observeErr := provider.Observe(ctx, executionID)
		if observeErr == nil && observation.State == "terminated" {
			provider.forgetExecution(executionID)
			return nil
		}
		return ErrProviderFailure
	}
	provider.forgetExecution(executionID)
	return nil
}

func (provider *Provider) ListOrphans(ctx context.Context, gatewayID string, known map[string]struct{}) ([]execution.Orphan, error) {
	_, endpoint, err := provider.executionContext(gatewayID)
	if err != nil {
		return nil, err
	}
	result, err := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(endpoint, "sandbox", "list", "--selector", managedLabel+"=true", "--output", "json")...)
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
			orphans = append(orphans, execution.Orphan{ID: candidate, GatewayID: gatewayID})
		}
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].ID < orphans[j].ID })
	return orphans, nil
}

func (provider *Provider) executionContext(gatewayID string) (*gatewayEntry, string, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	entry, ok := provider.gateways[gatewayID]
	if !ok || entry.gateway.State == execution.GatewayLost || entry.gateway.State == execution.GatewayUnavailable {
		return nil, "", ErrNoGateway
	}
	return entry, entry.endpoint, nil
}

func (provider *Provider) validatePlacement(request execution.CreateRequest) error {
	expectedID := derivedID("plc", request.IsolationDomainID+":"+request.OperationID)
	if request.Placement.ID != expectedID {
		return errors.New("placement does not match the requested operation")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	stored, ok := provider.placements[request.Placement.ID]
	if !ok || stored != request.Placement {
		return errors.New("placement is not reserved")
	}
	return nil
}

func (provider *Provider) lookupExecution(executionID string) (executionEntry, *gatewayEntry, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	entry, ok := provider.executions[executionID]
	if !ok {
		return executionEntry{}, nil, ErrExecutionMissing
	}
	gateway, ok := provider.gateways[entry.execution.GatewayID]
	if !ok || gateway.gateway.State == execution.GatewayLost {
		return executionEntry{}, nil, ErrNoGateway
	}
	return entry, gateway, nil
}

func (provider *Provider) rememberExecution(value execution.Execution, sandbox string) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.executions[value.ID] = executionEntry{execution: value, sandbox: sandbox}
}

func (provider *Provider) forgetExecution(executionID string) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	delete(provider.executions, executionID)
}

func (provider *Provider) observeByName(ctx context.Context, endpoint, sandbox, executionID, gatewayID string) (execution.Execution, bool, error) {
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
			return execution.Execution{ID: executionID, GatewayID: gatewayID, State: strings.ToLower(item.Phase)}, true, nil
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

func containsAll(have, required []string) bool {
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
