package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/reference"
)

var demoDomain = regexp.MustCompile(`^iso_[a-z0-9]{20,32}$`)

type demoAPI struct {
	client *http.Client
	scope  string
	token  string
}

func newDemoAPI(origin, isolationDomain, token string) (demoAPI, error) {
	address, err := url.Parse(origin)
	if err != nil || address.Scheme != "http" || address.User != nil ||
		address.Path != "" || address.ForceQuery || address.RawQuery != "" || address.Fragment != "" ||
		!net.ParseIP(address.Hostname()).IsLoopback() {
		return demoAPI{}, errors.New("DATAGROUND_DEMO_API_ORIGIN must be an HTTP origin with a loopback IP")
	}
	if !demoDomain.MatchString(isolationDomain) {
		return demoAPI{}, errors.New("DATAGROUND_DEVELOPMENT_ISOLATION_DOMAIN_ID is required")
	}
	if len(token) < 32 {
		return demoAPI{}, errors.New("DATAGROUND_DEVELOPMENT_BEARER_TOKEN is required")
	}
	return demoAPI{
		client: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		scope: address.String() + "/v1/isolation-domains/" + isolationDomain,
		token: token,
	}, nil
}

func request[T any](ctx context.Context, api demoAPI, method, path, key string, body any) (T, error) {
	var value T
	var input io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return value, err
		}
		input = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, api.scope+path, input)
	if err != nil {
		return value, err
	}
	req.Header.Set("Authorization", "Bearer "+api.token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "dataground-demo.v1."+key)
	}
	response, err := api.client.Do(req)
	if err != nil {
		return value, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return value, fmt.Errorf("%s %s returned HTTP %d", method, path, response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&value); err != nil {
		return value, fmt.Errorf("decode %s %s: %w", method, path, err)
	}
	return value, nil
}

type demoRun struct {
	Scenario    string   `json:"scenario"`
	ID          string   `json:"id"`
	State       string   `json:"state"`
	ArtifactIDs []string `json:"artifactIds,omitempty"`
}

type demoSeed struct {
	WorkspaceID         string    `json:"workspaceId"`
	ServiceID           string    `json:"serviceId"`
	PublishedRevisionID string    `json:"publishedRevisionId"`
	DraftRevisionID     string    `json:"draftRevisionId"`
	Runs                []demoRun `json:"runs"`
}

func seed(ctx context.Context, api demoAPI) (demoSeed, error) {
	var result demoSeed
	workspace, err := request[domain.Workspace](ctx, api, http.MethodPost, "/workspaces", "workspace",
		map[string]string{"name": "Reference demo", "description": "Fictional agent-service scenarios."})
	if err != nil {
		return result, err
	}
	result.WorkspaceID = workspace.Metadata.ID
	service, err := request[domain.AgentService](ctx, api, http.MethodPost,
		"/workspaces/"+url.PathEscape(result.WorkspaceID)+"/agent-services", "service",
		map[string]string{"name": "Order support", "description": "Explore successful, failed, and cancelled reference runs."})
	if err != nil {
		return result, err
	}
	result.ServiceID = service.Metadata.ID
	servicePath := "/agent-services/" + url.PathEscape(result.ServiceID)
	revisionBody := map[string]any{"runtimeProfile": "reference/v1", "requiredCapabilities": []string{}}
	revision, err := request[domain.ServiceRevision](ctx, api, http.MethodPost, servicePath+"/revisions", "release", revisionBody)
	if err != nil {
		return result, err
	}
	result.PublishedRevisionID = revision.Metadata.ID
	revisionPath := "/service-revisions/" + url.PathEscape(revision.Metadata.ID)
	_, err = request[domain.Operation](ctx, api, http.MethodPost, revisionPath+"/actions/publish", "publish",
		map[string]int{"expectedVersion": revision.Metadata.Version})
	if err != nil {
		return result, err
	}
	published, err := request[domain.ServiceRevision](ctx, api, http.MethodGet, revisionPath, "", nil)
	if err != nil {
		return result, err
	}
	if published.State != "published" {
		return result, errors.New("demo seed requires the process-local reference API")
	}
	draft, err := request[domain.ServiceRevision](ctx, api, http.MethodPost, servicePath+"/revisions", "draft", revisionBody)
	if err != nil {
		return result, err
	}
	if draft.State != "draft" {
		return result, errors.New("demo draft is unavailable")
	}
	result.DraftRevisionID = draft.Metadata.ID
	alias, err := request[domain.ServiceAlias](ctx, api, http.MethodPut, servicePath+"/aliases/stable", "route",
		map[string]any{"revisionId": revision.Metadata.ID, "expectedVersion": 0})
	if err != nil {
		return result, err
	}
	if alias.RevisionID != revision.Metadata.ID {
		return result, errors.New("demo alias points to a different revision")
	}

	for _, scenario := range []struct{ name, state string }{
		{reference.ScenarioSuccess, "succeeded"},
		{reference.ScenarioRetryableFailure, "failed"},
		{reference.ScenarioCancellation, "cancelled"},
		{reference.ScenarioArtifact, "succeeded"},
	} {
		invocation, err := request[domain.Invocation](ctx, api, http.MethodPost, servicePath+"/invocations", "run."+scenario.name,
			map[string]any{"alias": "stable", "input": map[string]string{"scenario": scenario.name}})
		if err != nil {
			return result, err
		}
		if invocation.State != scenario.state || invocation.RevisionID != revision.Metadata.ID {
			return result, fmt.Errorf("reference scenario %s returned an unexpected state or revision", scenario.name)
		}
		if scenario.name == reference.ScenarioArtifact && len(invocation.ArtifactIDs) != 1 {
			return result, errors.New("artifact scenario did not create a descriptor")
		}
		result.Runs = append(result.Runs, demoRun{scenario.name, invocation.Metadata.ID, invocation.State, invocation.ArtifactIDs})
	}
	return result, nil
}

func run() error {
	origin := os.Getenv("DATAGROUND_DEMO_API_ORIGIN")
	if origin == "" {
		origin = "http://127.0.0.1:8082"
	}
	api, err := newDemoAPI(origin, os.Getenv("DATAGROUND_DEVELOPMENT_ISOLATION_DOMAIN_ID"), os.Getenv("DATAGROUND_DEVELOPMENT_BEARER_TOKEN"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := seed(ctx, api)
	if err != nil {
		return err
	}
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	return output.Encode(result)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "demo seed:", err)
		os.Exit(1)
	}
}
