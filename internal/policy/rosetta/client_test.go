package rosetta

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const validPolicy = `version: 1
filesystem_policy:
  include_workdir: false
  read_only:
    - "/workspace"
  read_write: []
landlock:
  compatibility: "hard_requirement"
process:
  run_as_user: "sandbox"
  run_as_group: "sandbox"
network_policies: {}
`

func TestClientVerifiesCompatibilityAndMaterializesStrictOpenShellPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/capabilities":
			_ = json.NewEncoder(response).Encode(capabilitiesResponse{
				Version: CompilerVersionV1,
				Capabilities: []string{
					"authorize", "compile", "schema-validation", "deterministic-artifacts",
				},
				Targets: []string{TargetOpenShell},
				TargetContracts: []targetContractInfo{{
					Target: TargetOpenShell, Version: OpenShellTargetContractV1, Maturity: "supported",
				}},
			})
		case "/v1/compile":
			var input compileRequest
			decoder := json.NewDecoder(request.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				t.Errorf("decode compile request: %v", err)
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			if input.Target != TargetOpenShell || input.Mode != ModeStrict || input.Catalog.Version != CatalogVersion {
				t.Errorf("unsafe compile request: %#v", input)
			}
			_ = json.NewEncoder(response).Encode(validCompileResponse(input))
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := testClient(t, server.URL, server.Client())

	compatibility, err := client.VerifyCompatibility(context.Background())
	if err != nil {
		t.Fatalf("verify compatibility: %v", err)
	}
	if compatibility.CompilerVersion != CompilerVersionV1 || compatibility.TargetContract != OpenShellTargetContractV1 ||
		compatibility.TargetMaturity != "supported" {
		t.Fatalf("compatibility = %#v", compatibility)
	}

	request := testMaterializeRequest()
	result, err := client.Materialize(context.Background(), request)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	policyDigest := sha256.Sum256([]byte(validPolicy))
	expectedDigest := "sha256:" + hex.EncodeToString(policyDigest[:])
	inputDigest, err := digestCompileInput(testCompileRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	expectedBinding := digestBinding(request.Context, inputDigest, hex.EncodeToString(policyDigest[:]))
	if result.Provenance.ArtifactDigest != expectedDigest || result.Provenance.BindingDigest != expectedBinding ||
		result.BundleID != "rosetta-"+strings.TrimPrefix(expectedBinding, "sha256:") || result.Context != request.Context {
		t.Fatalf("materialization identity = %#v", result)
	}
	if string(result.Content) != validPolicy || result.MediaType != "application/yaml" {
		t.Fatal("materialization did not preserve the validated artifact")
	}
	if got := result.Mappings; len(got) != 2 || got[0].CapabilityID != "network" || got[0].Status != "denied" ||
		got[1].CapabilityID != "workspace" || got[1].Status != "exact" {
		t.Fatalf("capability mappings = %#v", got)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "compiled" {
		t.Fatalf("sanitized diagnostics = %#v", result.Diagnostics)
	}
	result.Content[0] = 'x'
	second, err := client.Materialize(context.Background(), request)
	if err != nil || second.Content[0] != 'v' {
		t.Fatal("materialization aliases response content")
	}
}

func TestMaterializationIdentityBindsIsolationDomainAndResource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(validCompileResponse(testCompileRequest(testMaterializeRequest())))
	}))
	defer server.Close()
	client := testClient(t, server.URL, server.Client())

	firstRequest := testMaterializeRequest()
	first, err := client.Materialize(context.Background(), firstRequest)
	if err != nil {
		t.Fatalf("materialize first binding: %v", err)
	}
	secondRequest := testMaterializeRequest()
	secondRequest.Context.IsolationDomainID = "iso_" + strings.Repeat("c", 20)
	second, err := client.Materialize(context.Background(), secondRequest)
	if err != nil {
		t.Fatalf("materialize second binding: %v", err)
	}
	if first.Provenance.ArtifactDigest != second.Provenance.ArtifactDigest {
		t.Fatal("identical Rosetta artifacts did not preserve their artifact identity")
	}
	if first.Provenance.BindingDigest == second.Provenance.BindingDigest || first.BundleID == second.BundleID {
		t.Fatal("materialization identity was reused across isolation domains")
	}
}

func TestClientFailsClosedOnTransportAndContractDrift(t *testing.T) {
	tests := map[string]struct {
		response func(http.ResponseWriter)
		want     error
	}{
		"rejected": {
			response: func(writer http.ResponseWriter) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = writer.Write([]byte(`{"error":"secret policy selector"}`))
			},
			want: ErrRejected,
		},
		"unauthorized": {
			response: func(writer http.ResponseWriter) { writer.WriteHeader(http.StatusUnauthorized) },
			want:     ErrUnauthorized,
		},
		"unavailable": {
			response: func(writer http.ResponseWriter) { writer.WriteHeader(http.StatusServiceUnavailable) },
			want:     ErrUnavailable,
		},
		"wrong media type": {
			response: func(writer http.ResponseWriter) {
				writer.Header().Set("Content-Type", "text/plain")
				_, _ = writer.Write([]byte("not json"))
			},
			want: ErrProtocol,
		},
		"unknown response field": {
			response: func(writer http.ResponseWriter) {
				writer.Header().Set("Content-Type", "application/json")
				body, _ := json.Marshal(validCompileResponse(testCompileRequest(testMaterializeRequest())))
				body = append(body[:len(body)-1], []byte(`,"unexpected":true}`)...)
				_, _ = writer.Write(body)
			},
			want: ErrProtocol,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				test.response(writer)
			}))
			defer server.Close()
			client := testClient(t, server.URL, server.Client())
			_, err := client.Materialize(context.Background(), testMaterializeRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("materialize error = %v, want %v", err, test.want)
			}
			if strings.Contains(strings.ToLower(err.Error()), "secret") {
				t.Fatal("upstream error content escaped the client")
			}
		})
	}
}

func TestClientPreservesCallerCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
		writer.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer server.Close()
	client := testClient(t, server.URL, server.Client())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Materialize(ctx, testMaterializeRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("materialize error = %v, want context cancellation", err)
	}
}

func TestClientRejectsInvalidArtifactsAndDecisionSets(t *testing.T) {
	tests := map[string]func(*compileResponse){
		"input digest":     func(response *compileResponse) { response.Metadata.InputSHA256 = strings.Repeat("0", 64) },
		"compiler version": func(response *compileResponse) { response.Metadata.CompilerVersion = "2.0.0" },
		"target contract": func(response *compileResponse) {
			response.Metadata.TargetContractVersion = "rosetta/openshell-policy-v2"
		},
		"artifact digest":    func(response *compileResponse) { response.Metadata.ArtifactSHA256 = strings.Repeat("0", 64) },
		"output mismatch":    func(response *compileResponse) { response.Output += "extra" },
		"missing decision":   func(response *compileResponse) { response.Decisions = response.Decisions[:1] },
		"duplicate decision": func(response *compileResponse) { response.Decisions[1] = response.Decisions[0] },
		"unsafe workdir": func(response *compileResponse) {
			response.Artifacts[0].Content = strings.Replace(validPolicy, "include_workdir: false", "include_workdir: true", 1)
			response.Output = response.Artifacts[0].Content
			setArtifactDigest(response)
		},
		"writable root": func(response *compileResponse) {
			response.Artifacts[0].Content = strings.Replace(validPolicy, "read_write: []", "read_write: [\"/\"]", 1)
			response.Output = response.Artifacts[0].Content
			setArtifactDigest(response)
		},
		"missing filesystem policy": func(response *compileResponse) {
			response.Artifacts[0].Content = strings.Replace(validPolicy, "filesystem_policy:\n  include_workdir: false\n  read_only:\n    - \"/workspace\"\n  read_write: []\n", "", 1)
			response.Output = response.Artifacts[0].Content
			setArtifactDigest(response)
		},
		"missing explicit read list": func(response *compileResponse) {
			response.Artifacts[0].Content = strings.Replace(validPolicy, "  read_only:\n    - \"/workspace\"\n", "", 1)
			response.Output = response.Artifacts[0].Content
			setArtifactDigest(response)
		},
		"missing process identity": func(response *compileResponse) {
			response.Artifacts[0].Content = strings.Replace(validPolicy, "process:\n  run_as_user: \"sandbox\"\n  run_as_group: \"sandbox\"\n", "", 1)
			response.Output = response.Artifacts[0].Content
			setArtifactDigest(response)
		},
		"root process identity": func(response *compileResponse) {
			response.Artifacts[0].Content = strings.Replace(validPolicy, "run_as_user: \"sandbox\"", "run_as_user: \"root\"", 1)
			response.Output = response.Artifacts[0].Content
			setArtifactDigest(response)
		},
		"unknown policy field": func(response *compileResponse) {
			response.Artifacts[0].Content += "unexpected: true\n"
			response.Output = response.Artifacts[0].Content
			setArtifactDigest(response)
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			response := validCompileResponse(testCompileRequest(testMaterializeRequest()))
			change(&response)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(writer).Encode(response)
			}))
			defer server.Close()
			client := testClient(t, server.URL, server.Client())
			if _, err := client.Materialize(context.Background(), testMaterializeRequest()); err == nil {
				t.Fatal("invalid Rosetta result was accepted")
			}
		})
	}
}

func TestClientBoundsRequestsResponsesAndRedirects(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client := testClient(t, redirect.URL, redirect.Client())
	if _, err := client.Materialize(context.Background(), testMaterializeRequest()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("redirect error = %v, want ErrProtocol", err)
	}
	if targetCalls.Load() != 0 {
		t.Fatal("Rosetta client followed a redirect")
	}

	var requestCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requestCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"padding":"` + strings.Repeat("x", maximumResponseBytes) + `"}`))
	}))
	defer server.Close()
	client = testClient(t, server.URL, server.Client())
	request := testMaterializeRequest()
	request.CedarSource = strings.Repeat("x", maximumRequestBytes)
	if _, err := client.Materialize(context.Background(), request); !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("oversized request error = %v, want ErrRequestTooLarge", err)
	}
	if requestCalls.Load() != 0 {
		t.Fatal("oversized request reached Rosetta")
	}
	if _, err := client.Materialize(context.Background(), testMaterializeRequest()); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("oversized response error = %v, want ErrResponseTooLarge", err)
	}
}

func TestClientRequiresSecureExplicitConfiguration(t *testing.T) {
	tests := map[string]Config{
		"missing client version": {Endpoint: "https://rosetta.invalid", ExpectedTargetContract: OpenShellTargetContractV1},
		"userinfo": {
			Endpoint: "https://user:secret@rosetta.invalid", ExpectedCompilerVersion: CompilerVersionV1,
			ExpectedTargetContract: OpenShellTargetContractV1,
		},
		"path": {
			Endpoint: "https://rosetta.invalid/api", ExpectedCompilerVersion: CompilerVersionV1,
			ExpectedTargetContract: OpenShellTargetContractV1,
		},
		"insecure remote": {
			Endpoint: "http://rosetta.invalid", ExpectedCompilerVersion: CompilerVersionV1,
			ExpectedTargetContract: OpenShellTargetContractV1, AllowInsecureLoopback: true,
		},
	}
	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := New(config, http.DefaultClient); err == nil {
				t.Fatal("unsafe Rosetta configuration was accepted")
			}
		})
	}
}

func TestClientRejectsAmbiguousOrTransformedCatalogInputsBeforeSending(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := testClient(t, server.URL, server.Client())
	tests := map[string]func(*MaterializeRequest){
		"missing binding context": func(request *MaterializeRequest) {
			request.Context = BindingContext{}
		},
		"mismatched binding resource": func(request *MaterializeRequest) {
			request.Context.ResourceType = "execution"
		},
		"invalid UTF-8 source": func(request *MaterializeRequest) {
			request.CedarSource = string([]byte{0xff})
		},
		"wrong action": func(request *MaterializeRequest) {
			request.Catalog.Capabilities[0].Action = "execute"
		},
		"irrelevant target": func(request *MaterializeRequest) {
			request.Catalog.Capabilities[0].Targets = []string{"codex"}
		},
		"duplicate binary": func(request *MaterializeRequest) {
			request.Catalog.Capabilities[1].Binaries = []string{"/usr/bin/client", "/usr/bin/client"}
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			request := testMaterializeRequest()
			change(&request)
			if _, err := client.Materialize(context.Background(), request); !errors.Is(err, ErrRejected) {
				t.Fatalf("materialize error = %v, want ErrRejected", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid catalog inputs reached Rosetta %d times", calls.Load())
	}
}

func TestCompatibilityRejectsDuplicateOpenShellContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(capabilitiesResponse{
			Version: CompilerVersionV1,
			Capabilities: []string{
				"authorize", "compile", "schema-validation", "deterministic-artifacts",
			},
			Targets: []string{TargetOpenShell},
			TargetContracts: []targetContractInfo{
				{Target: TargetOpenShell, Version: OpenShellTargetContractV1, Maturity: "supported"},
				{Target: TargetOpenShell, Version: OpenShellTargetContractV1, Maturity: "supported"},
			},
		})
	}))
	defer server.Close()
	client := testClient(t, server.URL, server.Client())
	if _, err := client.VerifyCompatibility(context.Background()); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("compatibility error = %v, want ErrIncompatible", err)
	}
}

func TestCompatibilityRequiresCompilerCapabilities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(capabilitiesResponse{
			Version: CompilerVersionV1,
			Capabilities: []string{
				"authorize", "compile", "schema-validation",
			},
			Targets: []string{TargetOpenShell},
			TargetContracts: []targetContractInfo{{
				Target: TargetOpenShell, Version: OpenShellTargetContractV1, Maturity: "supported",
			}},
		})
	}))
	defer server.Close()
	client := testClient(t, server.URL, server.Client())
	if _, err := client.VerifyCompatibility(context.Background()); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("compatibility error = %v, want ErrIncompatible", err)
	}
}

func testClient(t *testing.T, endpoint string, httpClient *http.Client) *Client {
	t.Helper()
	client, err := New(Config{
		Endpoint: endpoint, ExpectedCompilerVersion: CompilerVersionV1,
		ExpectedTargetContract: OpenShellTargetContractV1, AllowInsecureLoopback: true,
	}, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func testMaterializeRequest() MaterializeRequest {
	return MaterializeRequest{
		CedarSource: `permit(principal, action, resource);`,
		Context: BindingContext{
			IsolationDomainID: "iso_" + strings.Repeat("a", 20),
			ResourceType:      "service-revision",
			ResourceID:        "rev_" + strings.Repeat("b", 20),
		},
		Catalog: Catalog{
			Version:   CatalogVersion,
			Principal: EntityRef{ID: "service-revision", Roles: []string{"agent"}},
			Capabilities: []Capability{
				{ID: "workspace", Kind: "filesystem", Action: "read", Selector: "/workspace"},
				{ID: "network", Kind: "network", Action: "connect", Selector: "api.example.invalid", Port: 443, Binaries: []string{"/usr/bin/client"}},
			},
		},
		OpenShell: OpenShellOptions{RunAsUser: "sandbox", RunAsGroup: "sandbox"},
	}
}

func testCompileRequest(request MaterializeRequest) compileRequest {
	return compileRequest{
		Source: request.CedarSource, Target: TargetOpenShell, Mode: ModeStrict,
		Catalog: request.Catalog, Options: targetOptions{OpenShell: request.OpenShell}, Context: request.Context,
	}
}

func validCompileResponse(request compileRequest) compileResponse {
	digest := sha256.Sum256([]byte(validPolicy))
	decisions := make([]decision, 0, len(request.Catalog.Capabilities))
	for _, capability := range request.Catalog.Capabilities {
		decisions = append(decisions, decision{CapabilityID: capability.ID, Allowed: capability.ID == "workspace"})
	}
	inputDigest, _ := digestCompileInput(request)
	return compileResponse{
		Output: validPolicy, Target: TargetOpenShell,
		Artifacts: []artifact{{
			Name: "policy.yaml", PathHint: "policy.yaml", MediaType: "application/yaml",
			Target: TargetOpenShell, Content: validPolicy, Encoding: "plain",
		}},
		Decisions: decisions,
		Diagnostics: []diagnostic{{
			Severity: "info", Code: "compiled", Message: "sensitive selector details remain upstream",
		}},
		Metadata: compileMetadata{
			CompilerVersion: CompilerVersionV1, CatalogVersion: CatalogVersion,
			TargetContractVersion: OpenShellTargetContractV1, Mode: ModeStrict,
			InputSHA256: inputDigest, ArtifactSHA256: hex.EncodeToString(digest[:]),
		},
	}
}

func setArtifactDigest(response *compileResponse) {
	digest := sha256.Sum256([]byte(response.Artifacts[0].Content))
	response.Metadata.ArtifactSHA256 = hex.EncodeToString(digest[:])
}
