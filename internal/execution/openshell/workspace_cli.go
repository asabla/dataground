package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/asabla/dataground/internal/execution"
)

const (
	WorkspaceCLIVersion = "0.1.9"
	localWorkspace      = "default"
	workspacePageSize   = 100
	workspacePageLimit  = 16
)

func (provider *Provider) usesWorkspaceCLI() bool {
	return strings.TrimPrefix(provider.expected, "v") == WorkspaceCLIVersion
}

type workspaceResource struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
}

func (resource workspaceResource) identity() workspaceResource { return resource }

// A missing, repeated, or unbounded page cannot prove absence of a resource.
func listWorkspaceResources[T interface{ identity() workspaceResource }](
	ctx context.Context, provider *Provider, endpoint string, collection string, args ...string,
) ([]T, error) {
	names := make(map[string]bool)
	ids := make(map[string]bool)
	tokens := make(map[string]bool)
	var items []T
	token := ""
	for range workspacePageLimit {
		query := append(append([]string(nil), args...), "--output", "json", "--page-size", strconv.Itoa(workspacePageSize))
		if token != "" {
			query = append(query, "--page-token="+token)
		}
		result, err := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(endpoint, query...)...)
		if err != nil || result.ExitCode != 0 {
			clear(result.Stdout)
			return nil, nativeCommandFailure(result)
		}
		var rows []T
		var next *string
		var fields map[string]json.RawMessage
		if len(result.Stdout) <= providerBindingMaxOutputBytes {
			err = json.Unmarshal(result.Stdout, &fields)
		}
		clear(result.Stdout)
		if err != nil || json.Unmarshal(fields["next_page_token"], &next) != nil ||
			json.Unmarshal(fields[collection], &rows) != nil ||
			rows == nil || len(rows) > workspacePageSize || next == nil {
			return nil, ErrProviderFailure
		}
		for _, row := range rows {
			resource := row.identity()
			if resource.Workspace != localWorkspace || resource.Name == "" || resource.ID == "" ||
				names[resource.Name] || ids[resource.ID] {
				return nil, ErrProviderFailure
			}
			names[resource.Name] = true
			ids[resource.ID] = true
		}
		items = append(items, rows...)
		token = *next
		if token == "" {
			return items, nil
		}
		if len(token) > 4096 || tokens[token] || strings.ContainsFunc(token, func(character rune) bool {
			return character < 32 || character == 127
		}) {
			return nil, ErrProviderFailure
		}
		tokens[token] = true
	}
	return nil, ErrProviderFailure
}

func (provider *Provider) listManagedSandboxes(ctx context.Context, endpoint string) ([]sandboxView, error) {
	if provider.usesWorkspaceCLI() {
		rows, err := listWorkspaceResources[sandboxView](ctx, provider, endpoint, "sandboxes",
			"sandbox", "list", "--selector", managedLabel+"=true")
		if err != nil {
			return nil, errors.Join(ErrProviderObservation, err)
		}
		for index := range rows {
			switch rows[index].Phase {
			case "Ready":
				rows[index].Phase = "ready"
			case "Provisioning", "Starting":
				rows[index].Phase = "provisioning"
			case "Error":
				rows[index].Phase = "error"
			case "Deleting":
				rows[index].Phase = "deleting"
			case "Stopping", "Stopped", "Completed", "Suspending", "Suspended", "Unknown", "Unspecified":
				rows[index].Phase = "unknown"
			default:
				return nil, errors.Join(ErrProviderObservation, ErrProviderFailure)
			}
		}
		return rows, nil
	}
	result, err := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(endpoint,
		"sandbox", "list", "--selector", managedLabel+"=true", "--output", "json")...)
	if err != nil || result.ExitCode != 0 {
		return nil, errors.Join(ErrProviderObservation, nativeCommandFailure(result))
	}
	var rows []sandboxView
	if json.Unmarshal(result.Stdout, &rows) != nil {
		return nil, errors.Join(ErrProviderObservation, ErrProviderFailure)
	}
	return rows, nil
}

func (provider *Provider) checkWorkspaceProfiles(ctx context.Context, endpoint string) error {
	result, runErr := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(
		endpoint, "provider", "profile", "list", "--output", "json")...)
	defer clear(result.Stdout)
	var profiles []json.RawMessage
	if runErr != nil || result.ExitCode != 0 {
		return errors.Join(ErrProviderSettingsObservation, nativeCommandFailure(result))
	}
	if len(result.Stdout) > providerBindingMaxOutputBytes ||
		json.Unmarshal(result.Stdout, &profiles) != nil || profiles == nil {
		return errors.Join(ErrProviderFailure, ErrProviderSettingsObservation)
	}
	return nil
}

func (provider *Provider) checkWorkspaceGateway(ctx context.Context, endpoint string) error {
	result, runErr := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(
		endpoint, "status", "--output", "json")...)
	defer clear(result.Stdout)
	var status struct {
		Server  string `json:"server"`
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if runErr != nil || result.ExitCode != 0 || len(result.Stdout) > providerBindingMaxOutputBytes ||
		json.Unmarshal(result.Stdout, &status) != nil || status.Server != endpoint ||
		status.Status != "connected" || status.Version != WorkspaceCLIVersion {
		return ErrProviderFailure
	}
	return nil
}

func (provider *Provider) checkWorkspaceSandbox(
	ctx context.Context, endpoint string, entry execution.ExecutionRecord, requireReady bool,
) error {
	result, runErr := provider.runner.Run(ctx, provider.binary, provider.gatewayArgs(
		endpoint, "sandbox", "get", entry.SandboxName, "--output", "json")...)
	defer clear(result.Stdout)
	var row sandboxView
	if runErr != nil || result.ExitCode != 0 || len(result.Stdout) > providerBindingMaxOutputBytes ||
		json.Unmarshal(result.Stdout, &row) != nil {
		return ErrProviderFailure
	}
	if row.Workspace != localWorkspace || row.ID == "" || row.Name != entry.SandboxName ||
		!row.matchesExecution(entry.Execution.IsolationDomainID, entry.Execution.ID, entry.OperationID) ||
		(requireReady && row.Phase != "Ready") {
		return execution.ErrStateConflict
	}
	return nil
}
