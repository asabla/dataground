package runtimeevidence

const (
	SchemaVersion   = "dataground.dev.openshell-runtime-conformance-evidence/v2"
	VerifierName    = "dataground-openshell-runtime-conformance"
	VerifierVersion = "2.0.0"
	Workflow        = ".github/workflows/openshell-runtime-conformance.yml"
	ArtifactName    = "openshell-runtime-conformance"

	openShellCommit              = "d556748771c41cbbd4e4dd7cd9030c798afe2b7d"
	gatewayImage                 = "ghcr.io/nvidia/openshell/gateway@sha256:e21f520a0678ba3cfe749957338b5fa78c75e8e52de13e4559ccbb582f781a0b"
	supervisorImage              = "ghcr.io/nvidia/openshell/supervisor@sha256:a15222ac18c1afd0ee51b9dda785a29067c13f61a2002a29d41f691f5e817f19"
	sandboxImage                 = "ghcr.io/nvidia/openshell-community/sandboxes/base@sha256:aeef1c63f00e2913ea002ccb3aaf925f338b5c5d70e63576f0d95c16a138044e"
	runtimeVersion               = "0.117.0"
	runtimeSchemaCanonicalSHA256 = "0668eee0081dc5b643ecc7821938ad174f3d532a313a092f387fed3b469876ea"
	credentialEvidenceSHA256     = "35abeed048bed669cd14bfb6a777dd8d024cc7561815bd2314295e79fb55322f"
	gatewayEndpoint              = "http://127.0.0.1:8080"
	driver                       = "docker"
	runtimeComposeSHA256         = "23e7b8fefea1cb51ea3740b6f2c2b9b5a3cd341bbad873e8ace9bb5612c6ad53"
	runtimeGatewayConfigSHA256   = "16f5d7a5e7d1dad1be67ede22d7b9d70e76517c98c6c9a4a4c2eb89eead08651"
)

type profile struct {
	RuntimePolicySHA256          string `json:"runtimePolicySHA256,omitempty"`
	OpenShellCommit              string `json:"openshellCommit"`
	GatewayImage                 string `json:"gatewayImage"`
	SupervisorImage              string `json:"supervisorImage"`
	SandboxImage                 string `json:"sandboxImage"`
	RuntimeVersion               string `json:"runtimeVersion"`
	RuntimeSchemaCanonicalSHA256 string `json:"runtimeSchemaCanonicalSHA256"`
	CredentialEvidenceSHA256     string `json:"credentialEvidenceSHA256,omitempty"`
	GatewayEndpoint              string `json:"gatewayEndpoint"`
	Driver                       string `json:"driver"`
	ComposeSHA256                string `json:"composeSHA256"`
	GatewayConfigSHA256          string `json:"gatewayConfigSHA256"`
}

func currentProfile() profile {
	return profile{
		OpenShellCommit:              openShellCommit,
		GatewayImage:                 gatewayImage,
		SupervisorImage:              supervisorImage,
		SandboxImage:                 sandboxImage,
		RuntimeVersion:               runtimeVersion,
		RuntimeSchemaCanonicalSHA256: runtimeSchemaCanonicalSHA256,
		CredentialEvidenceSHA256:     credentialEvidenceSHA256,
		GatewayEndpoint:              gatewayEndpoint,
		Driver:                       driver,
		ComposeSHA256:                runtimeComposeSHA256,
		GatewayConfigSHA256:          runtimeGatewayConfigSHA256,
	}
}
