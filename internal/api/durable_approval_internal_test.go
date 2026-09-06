package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
)

type durableApprovalResolverStub struct {
	idempotency persistence.Idempotency
	resolution  persistence.InvocationRuntimeApprovalResolution
	result      persistence.CommandResult
	err         error
}

type durableApprovalReaderStub struct {
	domainID     string
	invocationID string
	approvalID   string
	result       InvocationApproval
	err          error
}

func (reader *durableApprovalReaderStub) GetInvocationApproval(
	_ context.Context,
	domainID string,
	invocationID string,
	approvalID string,
) (InvocationApproval, error) {
	reader.domainID = domainID
	reader.invocationID = invocationID
	reader.approvalID = approvalID
	return reader.result, reader.err
}

func (resolver *durableApprovalResolverStub) ResolveCommand(
	_ context.Context,
	idempotency persistence.Idempotency,
	resolution persistence.InvocationRuntimeApprovalResolution,
) (persistence.CommandResult, error) {
	resolver.idempotency = idempotency
	resolver.resolution = resolution
	return resolver.result, resolver.err
}

func TestDurableApprovalReadBindsPublicPath(t *testing.T) {
	t.Parallel()
	const (
		domainID     = "iso_00000000000000000001"
		invocationID = "inv_00000000000000000001"
		approvalID   = "apr_00000000000000000001"
	)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	reader := &durableApprovalReaderStub{result: InvocationApproval{
		SchemaVersion: "dataground.invocation-approval/v1",
		ID:            approvalID, IsolationDomainID: domainID, InvocationID: invocationID,
		RequestedAction: "workspace.change", State: "pending", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}}
	server := &DurableServer{approvalReader: reader}
	request := httptest.NewRequest(
		http.MethodGet,
		"/v1/isolation-domains/"+domainID+"/invocations/"+invocationID+"/approvals/"+approvalID,
		nil,
	)
	request.SetPathValue("isolationDomainId", domainID)
	request.SetPathValue("invocationId", invocationID)
	request.SetPathValue("approvalId", approvalID)
	response := httptest.NewRecorder()

	server.getInvocationApproval(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("read status = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control = %q", response.Header().Get("Cache-Control"))
	}
	if reader.domainID != domainID || reader.invocationID != invocationID || reader.approvalID != approvalID {
		t.Fatalf("read path = (%q, %q, %q)", reader.domainID, reader.invocationID, reader.approvalID)
	}
	var public InvocationApproval
	if err := json.NewDecoder(response.Body).Decode(&public); err != nil {
		t.Fatal(err)
	}
	if public.ID != approvalID || public.State != "pending" || public.Version != 1 {
		t.Fatalf("public approval = %#v", public)
	}
}

func TestDurableApprovalReadMapsClosedErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "missing", err: persistence.ErrInvocationRuntimeApprovalMissing, status: http.StatusNotFound, code: "RESOURCE_NOT_FOUND"},
		{name: "invalid", err: persistence.ErrInvocationRuntimeApprovalInvalid, status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "unavailable", err: errors.New("database unavailable"), status: http.StatusServiceUnavailable, code: "INVOCATION_APPROVAL_UNAVAILABLE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &durableApprovalReaderStub{err: test.err}
			server := &DurableServer{approvalReader: reader}
			request := httptest.NewRequest(
				http.MethodGet,
				"/v1/isolation-domains/iso_00000000000000000001/invocations/inv_00000000000000000001/approvals/apr_00000000000000000001",
				nil,
			)
			request.SetPathValue("isolationDomainId", "iso_00000000000000000001")
			request.SetPathValue("invocationId", "inv_00000000000000000001")
			request.SetPathValue("approvalId", "apr_00000000000000000001")
			response := httptest.NewRecorder()
			server.getInvocationApproval(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			var problem ErrorEnvelope
			if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
				t.Fatal(err)
			}
			if problem.Error.Code != test.code {
				t.Fatalf("error code = %q, want %q", problem.Error.Code, test.code)
			}
		})
	}
}

func TestDurableApprovalResolutionBindsPublicPathPrincipalAndIdempotency(t *testing.T) {
	t.Parallel()
	const (
		domainID     = "iso_00000000000000000001"
		invocationID = "inv_00000000000000000001"
		approvalID   = "apr_00000000000000000001"
		actorID      = "usr_00000000000000000001"
		correlation  = "cor_00000000000000000001"
	)
	resolvedAt := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	encoded, err := json.Marshal(InvocationApproval{
		SchemaVersion: "dataground.invocation-approval/v1",
		ID:            approvalID, IsolationDomainID: domainID, InvocationID: invocationID,
		RequestedAction: "workspace.change", State: "resolved", Version: 2,
		Decision: "approve", ResolvedBy: actorID, ResolvedAt: &resolvedAt,
		CreatedAt: resolvedAt.Add(-time.Minute), UpdatedAt: resolvedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	resolver := &durableApprovalResolverStub{result: persistence.CommandResult{
		Status: http.StatusOK, Body: encoded,
	}}
	server := &DurableServer{approvals: resolver}
	request := approvalResolutionRequest(
		t, domainID, invocationID, approvalID, actorID, correlation,
		`{"expectedVersion":1,"decision":"approve"}`,
	)
	response := httptest.NewRecorder()

	server.resolveInvocationApproval(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("resolution status = %d: %s", response.Code, response.Body.String())
	}
	if resolver.resolution.IsolationDomainID != domainID ||
		resolver.resolution.InvocationID != invocationID ||
		resolver.resolution.ApprovalID != approvalID ||
		resolver.resolution.ActorID != actorID ||
		resolver.resolution.CorrelationID != correlation ||
		resolver.resolution.Decision != "approve" ||
		resolver.resolution.ExpectedVersion != 1 {
		t.Fatalf("resolution binding = %#v", resolver.resolution)
	}
	if resolver.idempotency.IsolationDomainID != domainID ||
		resolver.idempotency.Method != http.MethodPost ||
		resolver.idempotency.Path != request.URL.EscapedPath() ||
		resolver.idempotency.Key != "resolve-approval-0001" {
		t.Fatalf("idempotency binding = %#v", resolver.idempotency)
	}
	var public InvocationApproval
	if err := json.NewDecoder(response.Body).Decode(&public); err != nil {
		t.Fatal(err)
	}
	if public.ID != approvalID || public.Decision != "approve" || public.Version != 2 {
		t.Fatalf("public approval = %#v", public)
	}
}

func TestDurableApprovalResolutionRejectsInvalidBodyBeforeResolver(t *testing.T) {
	t.Parallel()
	resolver := &durableApprovalResolverStub{}
	server := &DurableServer{approvals: resolver}
	request := approvalResolutionRequest(
		t,
		"iso_00000000000000000001",
		"inv_00000000000000000001",
		"apr_00000000000000000001",
		"usr_00000000000000000001",
		"cor_00000000000000000001",
		`{"expectedVersion":1,"decision":"allow_once","nativeId":"hidden"}`,
	)
	response := httptest.NewRecorder()

	server.resolveInvocationApproval(response, request)

	if response.Code != http.StatusBadRequest || resolver.resolution.ApprovalID != "" {
		t.Fatalf("invalid resolution = status %d, call %#v", response.Code, resolver.resolution)
	}
}

func TestDurableApprovalResolutionMapsClosedErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "missing", err: persistence.ErrInvocationRuntimeApprovalMissing, status: http.StatusNotFound, code: "RESOURCE_NOT_FOUND"},
		{name: "expired", err: persistence.ErrInvocationRuntimeApprovalExpired, status: http.StatusGone, code: "INVOCATION_APPROVAL_EXPIRED"},
		{name: "conflict", err: persistence.ErrInvocationRuntimeApprovalConflict, status: http.StatusConflict, code: "INVOCATION_APPROVAL_CONFLICT"},
		{name: "denied", err: reconcile.ErrInvocationApprovalDenied, status: http.StatusForbidden, code: "INVOCATION_APPROVAL_FORBIDDEN"},
		{name: "idempotency reuse", err: &persistence.DomainError{Code: "IDEMPOTENCY_KEY_REUSED", Message: "reused"}, status: http.StatusConflict, code: "IDEMPOTENCY_KEY_REUSED"},
		{name: "command in progress", err: &persistence.DomainError{Code: "COMMAND_IN_PROGRESS", Message: "pending", Retryable: true}, status: http.StatusServiceUnavailable, code: "COMMAND_IN_PROGRESS"},
		{name: "unavailable", err: errors.New("database unavailable"), status: http.StatusServiceUnavailable, code: "INVOCATION_APPROVAL_UNAVAILABLE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := &durableApprovalResolverStub{err: test.err}
			server := &DurableServer{approvals: resolver}
			request := approvalResolutionRequest(
				t,
				"iso_00000000000000000001",
				"inv_00000000000000000001",
				"apr_00000000000000000001",
				"usr_00000000000000000001",
				"cor_00000000000000000001",
				`{"expectedVersion":1,"decision":"deny"}`,
			)
			response := httptest.NewRecorder()
			server.resolveInvocationApproval(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			var problem ErrorEnvelope
			if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
				t.Fatal(err)
			}
			if problem.Error.Code != test.code {
				t.Fatalf("error code = %q, want %q", problem.Error.Code, test.code)
			}
		})
	}
}

func approvalResolutionRequest(
	t *testing.T,
	domainID string,
	invocationID string,
	approvalID string,
	actorID string,
	correlationID string,
	body string,
) *http.Request {
	t.Helper()
	principal, err := authn.NewPrincipal(authn.PrincipalInput{
		ID: actorID, Kind: authn.PrincipalHuman, Issuer: "test", Subject: actorID,
		Audience: authn.APIAudience, IsolationDomains: []string{domainID},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/isolation-domains/"+domainID+"/invocations/"+invocationID+"/approvals/"+approvalID,
		bytes.NewBufferString(body),
	)
	request.SetPathValue("isolationDomainId", domainID)
	request.SetPathValue("invocationId", invocationID)
	request.SetPathValue("approvalId", approvalID)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "resolve-approval-0001")
	ctx := context.WithValue(request.Context(), authenticatedPrincipalKey{}, principal)
	ctx = context.WithValue(ctx, authenticatedCorrelationKey{}, correlationID)
	return request.WithContext(ctx)
}
