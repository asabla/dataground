package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/execution/s3store"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

type liveWorkspaceAPI struct {
	t      *testing.T
	ctx    context.Context
	server *httptest.Server
	client *http.Client
	token  string
	target persistence.DevelopmentPublicationInput
}

func newLiveWorkspaceAPI(t *testing.T, ctx context.Context, repo *persistence.Repository, objects *s3store.Store, target persistence.DevelopmentPublicationInput, actor string) *liveWorkspaceAPI {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	clear(secret)
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: actor, IsolationDomainID: target.Target.IsolationDomainID})
	if err != nil {
		t.Fatal(err)
	}
	auditedAuthentication, err := authn.NewAuditedAuthenticator(authenticator, repo)
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := authz.NewDevelopmentCedarAuthorizer(actor, target.Target.IsolationDomainID)
	if err != nil {
		t.Fatal(err)
	}
	auditedAuthorization, err := authz.NewAuditedAuthorizer(authorizer, repo)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := s3store.NewArtifactStore(objects, 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewPublishingDurableHandler(ctx, repo, auditedAuthentication, auditedAuthorization, target, api.DurableArtifactContentConfig{Objects: artifacts, MaximumBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	client := server.Client()
	client.Timeout = 20 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	t.Cleanup(func() { client.CloseIdleConnections(); server.Close() })
	return &liveWorkspaceAPI{t: t, ctx: ctx, server: server, client: client, token: token, target: target}
}

func (live *liveWorkspaceAPI) root() string {
	return "/v1/isolation-domains/" + live.target.Target.IsolationDomainID
}
func (live *liveWorkspaceAPI) servicePath() string {
	return live.root() + "/agent-services/" + live.target.Target.ServiceID
}

func (live *liveWorkspaceAPI) call(method, path string, body any, key string, authenticated bool, status int) []byte {
	live.t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			live.t.Fatal(err)
		}
	}
	request, err := http.NewRequestWithContext(live.ctx, method, live.server.URL+path, bytes.NewReader(encoded))
	if err != nil {
		live.t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if authenticated {
		request.Header.Set("Authorization", "Bearer "+live.token)
	}
	response, err := live.client.Do(request)
	if err != nil {
		live.t.Fatal("public request failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (10<<20)+1))
	if err != nil || len(data) > 10<<20 || response.StatusCode != status {
		live.t.Fatalf("public response status=%d want=%d bounded=%t", response.StatusCode, status, len(data) <= 10<<20)
	}
	return data
}

func (live *liveWorkspaceAPI) publish(args []string) {
	path := live.root() + "/service-revisions/" + live.target.Target.RevisionID + "/actions/publish"
	body := map[string]any{"expectedVersion": 1}
	live.call(http.MethodPost, path, body, "live-source-no-auth", false, http.StatusUnauthorized)
	foreign := strings.Replace(path, live.target.Target.IsolationDomainID, identity.New("iso"), 1)
	live.call(http.MethodPost, foreign, body, "live-source-cross-domain", true, http.StatusForbidden)
	accepted := live.call(http.MethodPost, path, body, "live-source-publish", true, http.StatusAccepted)
	version := 5
	if live.target.Contract == persistence.OwnedWorkspacePublicationContract {
		version = 6
	}
	var operation domain.Operation
	if json.Unmarshal(accepted, &operation) != nil || operation.StateMachineVersion != version || operation.ObservedState != "queued" {
		live.t.Fatal("public source operation contract mismatch")
	}
	var receipt bytes.Buffer
	if err := runDevelopmentPublication(live.ctx, args, &receipt); err != nil {
		live.t.Fatal("live authorized publication", err)
	}
	result := live.call(http.MethodGet, live.root()+"/operations/"+operation.Metadata.ID, nil, "", true, http.StatusOK)
	if json.Unmarshal(result, &operation) != nil || operation.StateMachineVersion != version || operation.ObservedState != "published" {
		live.t.Fatal("public source publication incomplete")
	}
	replay := live.call(http.MethodPost, path, body, "live-source-publish", true, http.StatusAccepted)
	if !bytes.Equal(accepted, replay) {
		live.t.Fatal("public publication replay changed")
	}
}

func (live *liveWorkspaceAPI) invocation(id string) domain.Invocation {
	var invocation domain.Invocation
	if json.Unmarshal(live.call(http.MethodGet, live.root()+"/invocations/"+id, nil, "", true, http.StatusOK), &invocation) != nil || invocation.Metadata.ID != id || invocation.Metadata.IsolationDomainID != live.target.Target.IsolationDomainID {
		live.t.Fatal("public invocation scope mismatch")
	}
	return invocation
}

func (live *liveWorkspaceAPI) verifyArtifact(invocation, artifact string, expected []byte) {
	path := live.root() + "/invocations/" + invocation + "/artifacts/" + artifact + "/content"
	live.call(http.MethodGet, path, nil, "", false, http.StatusUnauthorized)
	live.call(http.MethodGet, strings.Replace(path, live.target.Target.IsolationDomainID, identity.New("iso"), 1), nil, "", true, http.StatusForbidden)
	data := live.call(http.MethodGet, path, nil, "", true, http.StatusOK)
	defer clear(data)
	if !bytes.Equal(data, expected) {
		live.t.Fatal("public artifact differs from source")
	}
}

func (live *liveWorkspaceAPI) verifyEvents(invocation string) {
	path := live.root() + "/invocations/" + invocation + "/events?limit=500"
	events := live.call(http.MethodGet, path, nil, "", true, http.StatusOK)
	replay := live.call(http.MethodGet, path, nil, "", true, http.StatusOK)
	if !bytes.Equal(events, replay) {
		live.t.Fatal("completed event replay changed")
	}
	var last uint64
	for _, line := range bytes.Split(events, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		var event domain.EventEnvelope
		if json.Unmarshal(line[6:], &event) != nil || event.Sequence <= last || event.IsolationDomainID != live.target.Target.IsolationDomainID || event.InvocationID != invocation || event.Type == "" {
			live.t.Fatal("invalid public event sequence or scope")
		}
		last = event.Sequence
	}
	if last == 0 {
		live.t.Fatal("no public runtime events")
	}
	// The regular replay endpoint uses Last-Event-ID for resume, tested separately.
	live.t.Log("Verified public event replay through sequence " + strconv.FormatUint(last, 10))
}
