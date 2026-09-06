package main

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/reconcile"
)

func TestLoadWorkerConfigDefaultsToReferenceMode(t *testing.T) {
	t.Parallel()

	config, err := loadWorkerConfig(mapEnvironment(nil))
	if err != nil {
		t.Fatalf("load worker config: %v", err)
	}
	if config.mode != workerModeReference {
		t.Fatalf("worker mode = %q, want %q", config.mode, workerModeReference)
	}
}

func TestLoadWorkerConfigAcceptsCompleteGovernedDevelopmentMode(t *testing.T) {
	t.Parallel()

	config, err := loadWorkerConfig(mapEnvironment(validGovernedEnvironment()))
	if err != nil {
		t.Fatalf("load worker config: %v", err)
	}
	if config.mode != workerModeGovernedDevelopment ||
		config.isolationDomainID == "" ||
		config.gatewayID == "" ||
		config.s3RequestTimeout != 2*time.Minute ||
		config.maximumArtifactBytes != 16<<20 ||
		config.certification.target.serviceID == "" ||
		config.certification.target.revisionID == "" {
		t.Fatalf("worker config = %#v", config)
	}
}

func TestLoadWorkerConfigRejectsIncompleteOrUnsafeGovernedDevelopmentMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(map[string]string)
	}{
		{
			name: "missing isolation domain",
			mutate: func(values map[string]string) {
				delete(values, "DATAGROUND_DEVELOPMENT_ISOLATION_DOMAIN_ID")
			},
		},
		{
			name: "remote gateway",
			mutate: func(values map[string]string) {
				values["DATAGROUND_OPENSHELL_GATEWAY_ENDPOINT"] = "http://example.invalid:8080"
			},
		},
		{
			name: "alternate loopback gateway",
			mutate: func(values map[string]string) {
				values["DATAGROUND_OPENSHELL_GATEWAY_ENDPOINT"] = "http://127.0.0.1:8082"
			},
		},
		{
			name: "gateway path",
			mutate: func(values map[string]string) {
				values["DATAGROUND_OPENSHELL_GATEWAY_ENDPOINT"] = "http://127.0.0.1:8080/api"
			},
		},
		{
			name: "nested workspaces",
			mutate: func(values map[string]string) {
				values["DATAGROUND_OPENSHELL_EXPORT_WORKSPACE"] = "/tmp/dataground-policy/export"
			},
		},
		{
			name: "remote object store",
			mutate: func(values map[string]string) {
				values["DATAGROUND_S3_ENDPOINT"] = "https://s3.example.invalid"
			},
		},
		{
			name: "unbounded object request",
			mutate: func(values map[string]string) {
				values["DATAGROUND_S3_REQUEST_TIMEOUT"] = "0s"
			},
		},
		{
			name: "unbounded artifacts",
			mutate: func(values map[string]string) {
				values["DATAGROUND_INVOCATION_ARTIFACT_MAX_BYTES"] = "1073741825"
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			values := validGovernedEnvironment()
			test.mutate(values)
			if _, err := loadWorkerConfig(mapEnvironment(values)); err == nil {
				t.Fatal("invalid governed configuration was accepted")
			}
		})
	}
}

func TestGovernedDevelopmentPlanRequiresExactPinnedProfile(t *testing.T) {
	t.Parallel()

	plan := execution.ExecutionPlan{
		RuntimeProfile:          reconcile.CodexAppServerRuntimeProfileV1,
		ImageReference:          governedSandboxImage,
		EnforcementBundleDigest: governedEnforcementDigest,
		ProviderProfiles:        []string{governedProviderProfile},
		RequiredCapabilities:    []string{reconcile.CodexAppServerRuntimeProfileV1},
	}
	if !validGovernedDevelopmentPlan(plan, "") {
		t.Fatal("exact governed development plan was rejected")
	}
	tests := []struct {
		name   string
		mutate func(*execution.ExecutionPlan)
	}{
		{name: "runtime", mutate: func(value *execution.ExecutionPlan) { value.RuntimeProfile = "reference/v1" }},
		{name: "image", mutate: func(value *execution.ExecutionPlan) { value.ImageReference += "-other" }},
		{name: "missing enforcement", mutate: func(value *execution.ExecutionPlan) { value.EnforcementBundleDigest = "" }},
		{name: "different enforcement", mutate: func(value *execution.ExecutionPlan) {
			value.EnforcementBundleDigest = "sha256:d7f510e5332068cea5106de5351973dc60f15e22e970fa9352a75d3bbd32b95d"
		}},
		{name: "provider", mutate: func(value *execution.ExecutionPlan) { value.ProviderProfiles = nil }},
		{name: "capability", mutate: func(value *execution.ExecutionPlan) { value.RequiredCapabilities = nil }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			changed := plan
			changed.ProviderProfiles = slices.Clone(plan.ProviderProfiles)
			changed.RequiredCapabilities = slices.Clone(plan.RequiredCapabilities)
			test.mutate(&changed)
			if validGovernedDevelopmentPlan(changed, "") {
				t.Fatal("profile drift was accepted")
			}
		})
	}
}

func TestGovernedEnforcementDigestMatchesTheCertifiedFixture(t *testing.T) {
	policy, err := os.ReadFile("../../deploy/openshell/policies/deny-all.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.VerifyEnforcementPolicy(policy, governedEnforcementDigest); err != nil {
		t.Fatal("governed admission does not bind the checked deny-all fixture")
	}
}

func TestLoadWorkerConfigRejectsUnknownMode(t *testing.T) {
	t.Parallel()

	_, err := loadWorkerConfig(mapEnvironment(map[string]string{"DATAGROUND_WORKER_MODE": "production"}))
	if err == nil {
		t.Fatal("unknown worker mode was accepted")
	}
}

func TestWorkerResourcesCloseIsNilSafe(t *testing.T) {
	t.Parallel()

	var resources *workerResources
	if err := resources.Close(); err != nil {
		t.Fatalf("nil resources close: %v", err)
	}
	if err := (&workerResources{}).Close(); err != nil {
		t.Fatalf("empty resources close: %v", err)
	}
}

func validGovernedEnvironment() map[string]string {
	values := map[string]string{
		"DATAGROUND_WORKER_MODE":                     workerModeGovernedDevelopment,
		"DATAGROUND_DEVELOPMENT_ISOLATION_DOMAIN_ID": "iso_00000000000000000001",
		"DATAGROUND_OPENSHELL_GATEWAY_ID":            "gw_00000000000000000001",
		"DATAGROUND_OPENSHELL_GATEWAY_ENDPOINT":      "http://127.0.0.1:8080",
		"DATAGROUND_OPENSHELL_POLICY_WORKSPACE":      "/tmp/dataground-policy",
		"DATAGROUND_OPENSHELL_EXPORT_WORKSPACE":      "/tmp/dataground-export",
		"DATAGROUND_S3_ENDPOINT":                     "http://127.0.0.1:8333",
		"DATAGROUND_S3_BUCKET":                       "dataground-development",
		"DATAGROUND_S3_REQUEST_TIMEOUT":              "2m",
		"DATAGROUND_INVOCATION_ARTIFACT_MAX_BYTES":   "16777216",
	}
	values["DATAGROUND_CERTIFIED_SERVICE_ID"] = "svc_00000000000000000001"
	values["DATAGROUND_CERTIFIED_REVISION_ID"] = "rev_00000000000000000001"
	values["DATAGROUND_RUNTIME_CERTIFICATION_MANIFEST"] =
		"deploy/openshell/evidence/runtime-certification.json"
	values["DATAGROUND_RUNTIME_CONFORMANCE_EVIDENCE"] =
		"deploy/openshell/evidence/openshell-runtime-conformance-v1.json"
	values["DATAGROUND_RUNTIME_CONFORMANCE_ACCEPTANCE"] =
		"deploy/openshell/evidence/openshell-runtime-conformance-acceptance-v1.json"
	values["DATAGROUND_RUNTIME_CERTIFICATION_SHA256"] =
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	values["DATAGROUND_RUNTIME_CERTIFICATION_SOURCE_REVISION"] =
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	values["DATAGROUND_RUNTIME_CERTIFICATION_MINIMUM_GENERATION"] = "3"
	values["DATAGROUND_RUNTIME_CERTIFICATION_REJECTED_IDS"] =
		"rtcert_abcdefghij0123456789,rtcert_0123456789abcdefghij"
	return values
}

func mapEnvironment(values map[string]string) environmentLookup {
	return func(name string) (string, bool) {
		value, found := values[name]
		return value, found
	}
}
