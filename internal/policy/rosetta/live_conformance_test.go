package rosetta

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPinnedRosettaHTTPConformance(t *testing.T) {
	endpoint := os.Getenv("DATAGROUND_ROSETTA_CONFORMANCE_ENDPOINT")
	if endpoint == "" {
		t.Skip("pinned local Rosetta conformance service is not configured")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname()) {
		t.Fatal("Rosetta conformance requires a loopback HTTP endpoint")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client, err := New(Config{Endpoint: endpoint, ExpectedCompilerVersion: CompilerVersionV1, ExpectedTargetContract: OpenShellTargetContractV1, AllowInsecureLoopback: true}, &http.Client{Transport: transport, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := client.VerifyCompatibility(ctx); err != nil {
		t.Fatalf("pinned compiler compatibility: %v", err)
	}
	request := localConformanceRequest(t)
	first, err := client.Materialize(ctx, request)
	if err != nil {
		t.Fatalf("pinned compiler materialization: %v", err)
	}
	digest := sha256.Sum256(first.Content)
	checkedPolicy, err := os.ReadFile("../../../deploy/openshell/codex-compatibility/rosetta-runtime-policy.yaml")
	if err != nil || !bytes.Equal(checkedPolicy, first.Content) {
		t.Fatal("checked runtime policy is not the exact pinned compiler output")
	}
	if first.Provenance.ArtifactDigest != "sha256:"+hex.EncodeToString(digest[:]) ||
		hex.EncodeToString(digest[:]) != "a1d56c0470c3264c4c37183352d783ebb67911d92ef2eb6ec5f7c76c61f69f39" ||
		bytes.Contains(first.Content, []byte("/outside")) || !bytes.Contains(first.Content, []byte(`"/dev/null"`)) || !bytes.Contains(first.Content, []byte(`"/sandbox"`)) {
		t.Fatal("compiled artifact lost allow/deny or digest semantics")
	}
	denied := false
	for _, mapping := range first.Mappings {
		if mapping.CapabilityID == "denied-write" && mapping.Status == "denied" {
			denied = true
		}
	}
	if !denied {
		t.Fatal("default-deny decision was not preserved")
	}
	second, err := client.Materialize(ctx, request)
	if err != nil || !bytes.Equal(first.Content, second.Content) || first.Provenance != second.Provenance || first.BundleID != second.BundleID {
		t.Fatal("same pinned compiler input was not deterministic")
	}
	request.Context.IsolationDomainID = "iso_" + strings.Repeat("c", 20)
	otherScope, err := client.Materialize(ctx, request)
	if err != nil || !bytes.Equal(first.Content, otherScope.Content) || first.BundleID == otherScope.BundleID || first.Provenance.BindingDigest == otherScope.Provenance.BindingDigest {
		t.Fatal("identical compilation lost isolation-scoped binding")
	}
	request.CedarSource += ` forbid (principal, action, resource is Rosetta::Capability) when { resource.selector == "/sandbox" };`
	forbidden, err := client.Materialize(ctx, request)
	if err != nil {
		t.Fatalf("explicit forbid materialization: %v", err)
	}
	if bytes.Contains(forbidden.Content, []byte(`"/sandbox"`)) || !bytes.Contains(forbidden.Content, []byte(`"/dev/null"`)) || first.Provenance.InputDigest == forbidden.Provenance.InputDigest {
		t.Fatal("explicit forbid did not narrow the artifact")
	}
	request.CedarSource = `permit (principal, action, resource);`
	request.Catalog.Capabilities = []Capability{{ID: "unsafe-root", Kind: "filesystem", Action: "write", Selector: "/", Targets: []string{TargetOpenShell}}}
	if _, err := client.Materialize(ctx, request); !errors.Is(err, ErrRejected) {
		t.Fatalf("unrepresentable root-write request: %v", err)
	}
}

func localConformanceRequest(t *testing.T) MaterializeRequest {
	t.Helper()
	input, err := os.ReadFile("../../../deploy/openshell/codex-compatibility/rosetta-runtime-input.json")
	if err != nil {
		t.Fatal(err)
	}
	var request compileRequest
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Target != TargetOpenShell || request.Mode != ModeStrict {
		t.Fatal("invalid checked Rosetta runtime input")
	}
	return MaterializeRequest{CedarSource: request.Source, Catalog: request.Catalog, OpenShell: request.Options.OpenShell, Context: BindingContext{IsolationDomainID: "iso_" + strings.Repeat("a", 20), ResourceType: "service-revision", ResourceID: "rev_" + strings.Repeat("b", 20)}}
}

func TestPinnedRosettaCompileInputDigest(t *testing.T) {
	digest, err := digestCompileInput(testCompileRequest(localConformanceRequest(t)))
	if err != nil || digest != "b2895b9172c50ba7a5fdf574cebdf6789258cc8ce9f90ce5ad8f2b1ff0a825ab" {
		t.Fatal("input digest does not match the pinned Rosetta v1 response")
	}
}
