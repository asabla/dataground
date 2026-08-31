package authz_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
)

const (
	testActor  = "usr_00000000000000000001"
	otherActor = "usr_00000000000000000002"
	testDomain = "iso_00000000000000000001"
)

func TestDevelopmentCedarAuthorizerBindsPrincipalDomainAndClosedActions(t *testing.T) {
	t.Parallel()

	authorizer, err := authz.NewDevelopmentCedarAuthorizer(testActor, testDomain)
	if err != nil {
		t.Fatalf("create development authorizer: %v", err)
	}
	principal := newPrincipal(t, testActor, testDomain)
	requests := []authz.Request{
		request(principal, authz.CreateAgentService, authz.IsolationDomain, testDomain),
		request(principal, authz.ListAgentServices, authz.IsolationDomain, testDomain),
		request(principal, authz.CreateServiceRevision, authz.AgentService, "svc_00000000000000000001"),
		request(principal, authz.ListServiceRevisions, authz.AgentService, "svc_00000000000000000001"),
		request(principal, authz.PublishServiceRevision, authz.ServiceRevision, "rev_00000000000000000001"),
		request(principal, authz.AssignServiceAlias, authz.AgentService, "svc_00000000000000000001"),
		request(principal, authz.InvokeAgentService, authz.AgentService, "svc_00000000000000000001"),
		request(principal, authz.ReadInvocation, authz.Invocation, "inv_00000000000000000001"),
		request(principal, authz.ReadOperation, authz.Operation, "op_00000000000000000001"),
		request(principal, authz.CancelInvocation, authz.Invocation, "inv_00000000000000000001"),
		request(principal, authz.ReadInvocationApproval, authz.InvocationApproval, "apr_00000000000000000001"),
		request(principal, authz.ResolveInvocationApproval, authz.InvocationApproval, "apr_00000000000000000001"),
		request(principal, authz.ReadInvocationEvents, authz.Invocation, "inv_00000000000000000001"),
		request(principal, authz.ReadInvocationArtifact, authz.Artifact, "art_00000000000000000001"),
	}
	for _, authorization := range requests {
		if err := authorizer.Authorize(context.Background(), authorization); err != nil {
			t.Fatalf("authorize %s: %v", authorization.Action, err)
		}
	}

	denied := request(newPrincipal(t, otherActor, testDomain), authz.ReadInvocation, authz.Invocation, "inv_00000000000000000001")
	if err := authorizer.Authorize(context.Background(), denied); !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("expected principal-bound denial, got %v", err)
	}
}

func TestStaticCedarAuthorizerRejectsDriftAndInvalidRequests(t *testing.T) {
	t.Parallel()

	schema := authz.CanonicalAPICedarSchema()
	policies := []byte(`permit(principal, action, resource);`)
	authorizer, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{
		PolicySetID: "test-policy",
		Schema:      schema,
		Policies:    policies,
	})
	if err != nil {
		t.Fatalf("create static authorizer: %v", err)
	}
	clear(schema)
	clear(policies)
	principal := newPrincipal(t, testActor, testDomain)
	if err := authorizer.Authorize(context.Background(), request(
		principal,
		authz.ReadInvocation,
		authz.Invocation,
		"inv_00000000000000000001",
	)); err != nil {
		t.Fatalf("authorizer retained caller-owned configuration: %v", err)
	}
	invalid := request(principal, authz.Action("unknown"), authz.Invocation, "inv_00000000000000000001")
	if err := authorizer.Authorize(context.Background(), invalid); !errors.Is(err, authz.ErrInvalid) {
		t.Fatalf("expected invalid action rejection, got %v", err)
	}
	mismatched := request(principal, authz.ReadInvocation, authz.Artifact, "art_00000000000000000001")
	if err := authorizer.Authorize(context.Background(), mismatched); !errors.Is(err, authz.ErrInvalid) {
		t.Fatalf("expected action/resource rejection, got %v", err)
	}
	mismatchedApproval := request(
		principal,
		authz.ReadInvocationApproval,
		authz.Invocation,
		"inv_00000000000000000001",
	)
	if err := authorizer.Authorize(context.Background(), mismatchedApproval); !errors.Is(err, authz.ErrInvalid) {
		t.Fatalf("expected approval action/resource rejection, got %v", err)
	}

	driftedSchema := authz.CanonicalAPICedarSchema()
	driftedSchema[0] = '['
	if _, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{
		PolicySetID: "test-policy",
		Schema:      driftedSchema,
		Policies:    []byte(`permit(principal, action, resource);`),
	}); err == nil {
		t.Fatal("schema drift was accepted")
	}
	if _, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{
		PolicySetID: "test-policy",
		Schema:      authz.CanonicalAPICedarSchema(),
		Policies:    []byte("not Cedar"),
	}); err == nil {
		t.Fatal("invalid policy was accepted")
	}
}

func TestStaticCedarAuthorizerPreservesCancellationAndCannotSerialize(t *testing.T) {
	t.Parallel()

	authorizer, err := authz.NewDevelopmentCedarAuthorizer(testActor, testDomain)
	if err != nil {
		t.Fatalf("create development authorizer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := authorizer.Authorize(ctx, request(
		newPrincipal(t, testActor, testDomain),
		authz.ReadInvocation,
		authz.Invocation,
		"inv_00000000000000000001",
	)); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if _, err := json.Marshal(authorizer); err == nil {
		t.Fatal("authorizer serialized")
	}
}

func newPrincipal(t *testing.T, actorID, domainID string) authn.Principal {
	t.Helper()
	principal, err := authn.NewPrincipal(authn.PrincipalInput{
		ID: actorID, Kind: authn.PrincipalHuman, Issuer: "test", Subject: actorID,
		Audience: authn.APIAudience, IsolationDomains: []string{domainID},
	})
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	return principal
}

func request(
	principal authn.Principal,
	action authz.Action,
	resourceType authz.ResourceType,
	resourceID string,
) authz.Request {
	return authz.Request{
		Principal: principal, Action: action, ResourceType: resourceType,
		ResourceID: resourceID, IsolationDomainID: testDomain,
	}
}
