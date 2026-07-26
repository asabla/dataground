package canaryprofile

const (
	SchemaVersion         = "dataground.dev.openshell-credential-non-exposure-evidence/v2"
	VerifierName          = "dataground-openshell-canary"
	VerifierVersion       = "2.0.0"
	OpenShellVersion      = "0.0.86"
	OpenShellCommit       = "d556748771c41cbbd4e4dd7cd9030c798afe2b7d"
	GatewayImage          = "ghcr.io/nvidia/openshell/gateway@sha256:e21f520a0678ba3cfe749957338b5fa78c75e8e52de13e4559ccbb582f781a0b"
	SupervisorImage       = "ghcr.io/nvidia/openshell/supervisor@sha256:a15222ac18c1afd0ee51b9dda785a29067c13f61a2002a29d41f691f5e817f19"
	SandboxImage          = "ghcr.io/nvidia/openshell-community/sandboxes/base@sha256:aeef1c63f00e2913ea002ccb3aaf925f338b5c5d70e63576f0d95c16a138044e"
	ProviderProfileSHA256 = "d9c7f48d96916dcaca319e396d75e30ff6ad3bf2474f38f54ab37f37cabbca8f"
	RuntimeVersion        = "0.117.0"
	GatewayEndpoint       = "http://127.0.0.1:8080"
	HealthEndpoint        = "http://127.0.0.1:8081"
	Driver                = "docker"

	ComposePath         = "deploy/openshell/docker-compose.yml"
	ComposeSHA256       = "3cb3c9bfdba656c24cefb53baf65097c231a9a0e950630c5a4a6f6c1a83bd731"
	GatewayConfigPath   = "deploy/openshell/gateway.toml"
	GatewayConfigSHA256 = "f34f9bc33c5e3e777b6b22c14c22f4a6d474e0af717e7989c45ec5d0f8ea2da9"
	PolicyPath          = "deploy/openshell/policies/deny-all.yaml"
	PolicySHA256        = "a193c3421b98a1640aa099d91b528beaee91af2a14980ba423ac3050c40649a9"
)

// Identity is the immutable, content-free profile bound into every evidence record.
type Identity struct {
	OpenShellCommit       string
	GatewayImage          string
	SupervisorImage       string
	SandboxImage          string
	ProviderProfileSHA256 string
	RuntimeVersion        string
	GatewayEndpoint       string
	Driver                string
	ComposeSHA256         string
	GatewayConfigSHA256   string
}

func Current() Identity {
	return Identity{
		OpenShellCommit:       OpenShellCommit,
		GatewayImage:          GatewayImage,
		SupervisorImage:       SupervisorImage,
		SandboxImage:          SandboxImage,
		ProviderProfileSHA256: ProviderProfileSHA256,
		RuntimeVersion:        RuntimeVersion,
		GatewayEndpoint:       GatewayEndpoint,
		Driver:                Driver,
		ComposeSHA256:         ComposeSHA256,
		GatewayConfigSHA256:   GatewayConfigSHA256,
	}
}
