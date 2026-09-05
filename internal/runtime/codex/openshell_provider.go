package codex

import "github.com/asabla/dataground/internal/execution"

const openShellModelProvider = "dataground_openshell_codex"

// NewOpenShellConformance creates a client for the run-derived OpenShell
// conformance provider. That binding exposes its literal credential keys as
// environment placeholders; the built-in profile's discovery aliases are not
// injected. The caller owns the binding and its non-exposure boundary. This
// client receives no credential values and accepts no alternate endpoint.
func NewOpenShellConformance(session execution.RuntimeSession) (*Client, error) {
	return newClient(session, true)
}

func openShellProviderConfig() map[string]any {
	return map[string]any{
		"model_providers": map[string]any{
			openShellModelProvider: map[string]any{
				"name":                 "DataGround OpenShell Codex",
				"base_url":             "https://chatgpt.com/backend-api/codex",
				"env_key":              "access_token",
				"env_http_headers":     map[string]any{"ChatGPT-Account-Id": "account_id"},
				"wire_api":             "responses",
				"requires_openai_auth": false,
				"supports_websockets":  false,
			},
		},
	}
}
