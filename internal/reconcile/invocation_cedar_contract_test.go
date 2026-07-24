package reconcile

import (
	"reflect"
	"testing"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestMapInvocationCedarInputBindsDurableScope(t *testing.T) {
	t.Parallel()

	request := testInvocationAuthorizationRequest()
	got, err := mapInvocationCedarInput(request)
	if err != nil {
		t.Fatalf("map Cedar input: %v", err)
	}
	want := InvocationCedarInput{
		Contract: InvocationCedarContract,
		Principal: InvocationCedarEntityUID{
			Type: invocationCedarPrincipalType,
			ID:   "actor_1",
		},
		Action: InvocationCedarEntityUID{
			Type: invocationCedarActionType,
			ID:   "admit",
		},
		Resource: InvocationCedarEntityUID{
			Type: invocationCedarResourceType,
			ID:   "inv_1",
		},
		IsolationDomainID: "iso_1",
		OperationID:       "op_1",
		ServiceID:         "svc_1",
		RevisionID:        "rev_1",
		CorrelationID:     "corr_1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Cedar input = %#v, want %#v", got, want)
	}
}

func TestMapInvocationCedarInputExposesOnlyRuntimeCapabilities(t *testing.T) {
	t.Parallel()

	request := testInvocationAuthorizationRequest()
	request.Action = InvocationAuthorizationRun
	request.Runtime = &dgruntime.StartRequest{
		Prompt:       "sensitive prompt",
		WorkingDir:   "/sensitive/workspace",
		Model:        "native-secret-model",
		OutputSchema: map[string]any{"secret": "schema"},
		ApprovalMode: dgruntime.ApprovalLocked,
		SandboxMode:  dgruntime.SandboxWorkspaceWrite,
		Artifacts: []dgruntime.ArtifactDeclaration{
			{ID: "artifact_1", Name: "secret.csv", SandboxPath: "/workspace/secret.csv", MediaType: "text/csv", Kind: "file"},
			{ID: "artifact_2", Name: "secret.json", SandboxPath: "/workspace/secret.json", MediaType: "application/json", Kind: "file"},
			{ID: "artifact_3", Name: "report", SandboxPath: "/workspace/report", MediaType: "text/plain", Kind: "report"},
		},
	}
	got, err := mapInvocationCedarInput(request)
	if err != nil {
		t.Fatalf("map Cedar input: %v", err)
	}
	want := &InvocationCedarRuntimeContext{
		ApprovalMode:    "locked",
		SandboxMode:     "workspace-write",
		HasOutputSchema: true,
		ArtifactCount:   3,
		ArtifactKinds:   []string{"file", "report"},
	}
	if !reflect.DeepEqual(got.Runtime, want) {
		t.Fatalf("runtime context = %#v, want %#v", got.Runtime, want)
	}
}

func TestMapInvocationCedarInputRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	request := testInvocationAuthorizationRequest()
	request.ActorID = ""
	if _, err := mapInvocationCedarInput(request); err != ErrInvocationAuthorizationInvalid {
		t.Fatalf("map error = %v, want %v", err, ErrInvocationAuthorizationInvalid)
	}
}

func TestCloneInvocationCedarInputOwnsRuntimeKinds(t *testing.T) {
	t.Parallel()

	request := testInvocationAuthorizationRequest()
	request.Action = InvocationAuthorizationRun
	request.Runtime = &dgruntime.StartRequest{
		ApprovalMode: dgruntime.ApprovalLocked,
		SandboxMode:  dgruntime.SandboxReadOnly,
		Artifacts:    []dgruntime.ArtifactDeclaration{{Kind: "file"}},
	}
	input, err := mapInvocationCedarInput(request)
	if err != nil {
		t.Fatalf("map Cedar input: %v", err)
	}
	cloned, err := cloneInvocationCedarInput(input)
	if err != nil {
		t.Fatalf("clone Cedar input: %v", err)
	}
	cloned.Runtime.ArtifactKinds[0] = "changed"
	if input.Runtime.ArtifactKinds[0] != "file" {
		t.Fatal("clone mutated source-owned runtime kinds")
	}
}
