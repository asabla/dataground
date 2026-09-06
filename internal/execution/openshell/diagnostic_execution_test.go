package openshell

import (
	"context"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/execution"
)

func TestLocalDiagnosticCreationRetainsOrdinaryImageAndAdmissionDenials(t *testing.T) {
	for _, change := range []string{"ordinary-create", "mutable-image", "scope", "policy", "profile", "placement", "version", "driver", "endpoint"} {
		t.Run(change, func(t *testing.T) {
			provider, runner, placement, policy, digest := preparedProvider(t, nil)
			request := createRequest(placement, policy, digest)
			request.Image = "sha256:" + strings.Repeat("a", 64)
			create := provider.CreateLocalDiagnostic
			switch change {
			case "ordinary-create":
				create = provider.Create
			case "mutable-image":
				request.Image = "candidate:latest"
			case "scope":
				request.IsolationDomainID = "iso-b"
			case "policy":
				request.PolicyDigest = "sha256:" + strings.Repeat("0", 64)
			case "profile":
				request.ProviderProfiles = []string{"unregistered"}
			case "placement":
				request.OperationID = "different-operation"
			case "version":
				provider.expected = "0.0.87"
			case "driver", "endpoint":
				provider.store = execution.NewMemoryStateStore()
				registration := execution.GatewayRegistration{IsolationDomainID: "iso-a", ID: "gateway-a", Driver: "docker", Endpoint: runtimeSSHGatewayEndpoint, Capabilities: []string{"codex.app-server"}}
				if change == "driver" {
					registration.Driver = "kubernetes"
				} else {
					registration.Endpoint = "https://gateway.example.invalid"
				}
				if _, err := provider.RegisterGateway(context.Background(), registration); err != nil {
					t.Fatal(err)
				}
				var err error
				request.Placement, err = provider.SelectGateway(context.Background(), execution.PlacementRequest{IsolationDomainID: "iso-a", OperationID: "op-a", RequiredCapabilities: []string{"codex.app-server"}})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := create(context.Background(), request); err == nil {
				t.Fatal("invalid diagnostic boundary was accepted")
			}
			if len(runner.calls) != 0 {
				t.Fatal("invalid diagnostic boundary reached an external provider effect")
			}
		})
	}
}

func TestLocalDiagnosticCreationPreservesExactImageAndObservesReplay(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{{result: CommandResult{Stdout: []byte("[]")}}, {result: CommandResult{}}}}
	provider, _, placement, policy, digest := preparedProvider(t, runner)
	request := createRequest(placement, policy, digest)
	request.Image = "sha256:" + strings.Repeat("a", 64)
	created, err := provider.CreateLocalDiagnostic(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 || !containsSequence(runner.calls[1].args, "--from", request.Image) {
		t.Fatal("local image identity was changed")
	}
	runner.results = []scriptedResult{{result: CommandResult{Stdout: []byte(`[{"name":"` + sandboxName(request.IsolationDomainID, request.OperationID) + `","phase":"Ready","labels":{"` + createLabel + `":"` + fingerprintCreate(request, nil) + `"}}]`)}}}
	replayed, err := provider.CreateLocalDiagnostic(context.Background(), request)
	if err != nil || replayed.ID != created.ID || replayed.State != "ready" {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
	if len(runner.calls) != 3 || containsSequence(runner.calls[2].args, "sandbox", "create") {
		t.Fatal("diagnostic replay repeated sandbox creation")
	}
}
