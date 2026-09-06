package rosetta

const (
	CatalogVersion            = "rosetta/v1"
	TargetOpenShell           = "openshell"
	ModeStrict                = "strict"
	OpenShellTargetContractV1 = "rosetta/openshell-policy-v1"
	CompilerVersionV1         = "1.0.0"
	CandidateSourceRevisionV1 = "320158f1e4a4eea378d82c1527f4a7af5fb9855b"
	maximumRequestBytes       = 2 << 20
	maximumResponseBytes      = 4 << 20
	maximumCapabilities       = 4096
)

type EntityRef struct {
	Type  string   `json:"type,omitempty"`
	ID    string   `json:"id"`
	Roles []string `json:"roles,omitempty"`
}

type Catalog struct {
	Version      string       `json:"version"`
	Principal    EntityRef    `json:"principal"`
	Capabilities []Capability `json:"capabilities"`
}

type Capability struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Action   string   `json:"action"`
	Selector string   `json:"selector"`
	Targets  []string `json:"targets,omitempty"`
	Access   string   `json:"access,omitempty"`
	Port     int      `json:"port,omitempty"`
	Protocol string   `json:"protocol,omitempty"`
	Path     string   `json:"path,omitempty"`
	Binaries []string `json:"binaries,omitempty"`
	Server   string   `json:"server,omitempty"`
}

type OpenShellOptions struct {
	RunAsUser  string `json:"runAsUser,omitempty"`
	RunAsGroup string `json:"runAsGroup,omitempty"`
}

type BindingContext struct {
	IsolationDomainID string
	ResourceType      string
	ResourceID        string
}

type MaterializeRequest struct {
	CedarSource string
	Catalog     Catalog
	OpenShell   OpenShellOptions
	Context     BindingContext
}

type CapabilityMapping struct {
	CapabilityID string
	Status       string
}

type Diagnostic struct {
	Severity    string
	Code        string
	Recoverable bool
}

type Provenance struct {
	CompilerVersion       string
	CatalogVersion        string
	TargetContractVersion string
	Mode                  string
	InputDigest           string
	ArtifactDigest        string
	BindingDigest         string
}

type Materialization struct {
	Context     BindingContext
	BundleID    string
	Content     []byte
	MediaType   string
	Mappings    []CapabilityMapping
	Diagnostics []Diagnostic
	Provenance  Provenance
}

type Compatibility struct {
	CompilerVersion string
	TargetContract  string
	TargetMaturity  string
}

type compileRequest struct {
	Source  string         `json:"source"`
	Target  string         `json:"target"`
	Mode    string         `json:"mode"`
	Catalog Catalog        `json:"catalog"`
	Options targetOptions  `json:"options,omitempty"`
	Context BindingContext `json:"-"`
}

type targetOptions struct {
	OpenShell OpenShellOptions `json:"openShell,omitempty"`
	// Rosetta v1 hashes the complete options value, including this empty
	// non-pointer struct when compiling an OpenShell target.
	Codex struct{} `json:"codex,omitempty"`
}

type compileResponse struct {
	Output      string          `json:"output"`
	Target      string          `json:"target"`
	Artifacts   []artifact      `json:"artifacts"`
	Decisions   []decision      `json:"decisions"`
	Diagnostics []diagnostic    `json:"diagnostics,omitempty"`
	Metadata    compileMetadata `json:"metadata"`
}

type artifact struct {
	Name        string `json:"name"`
	PathHint    string `json:"pathHint,omitempty"`
	MediaType   string `json:"mediaType"`
	Target      string `json:"target"`
	Content     string `json:"content"`
	Encoding    string `json:"encoding"`
	Description string `json:"description,omitempty"`
}

type decision struct {
	CapabilityID string   `json:"capabilityId"`
	Allowed      bool     `json:"allowed"`
	PolicyIDs    []string `json:"policyIds,omitempty"`
}

type diagnostic struct {
	Severity         string         `json:"severity"`
	Code             string         `json:"code"`
	Message          string         `json:"message"`
	Details          map[string]any `json:"details,omitempty"`
	SourceSpan       *sourceSpan    `json:"sourceSpan,omitempty"`
	Target           string         `json:"target,omitempty"`
	RuleID           string         `json:"ruleId,omitempty"`
	Recoverable      bool           `json:"recoverable,omitempty"`
	DocumentationURL string         `json:"documentationUrl,omitempty"`
}

type sourceSpan struct {
	StartLine   int `json:"startLine,omitempty"`
	StartColumn int `json:"startColumn,omitempty"`
	EndLine     int `json:"endLine,omitempty"`
	EndColumn   int `json:"endColumn,omitempty"`
}

type compileMetadata struct {
	CompilerVersion       string `json:"compilerVersion"`
	CatalogVersion        string `json:"catalogVersion"`
	TargetContractVersion string `json:"targetContractVersion"`
	Mode                  string `json:"mode"`
	InputSHA256           string `json:"inputSha256"`
	ArtifactSHA256        string `json:"artifactSha256"`
}

type capabilitiesResponse struct {
	Version         string               `json:"version"`
	Capabilities    []string             `json:"capabilities"`
	Targets         []string             `json:"targets"`
	TargetContracts []targetContractInfo `json:"targetContracts"`
}

type targetContractInfo struct {
	Target   string `json:"target"`
	Version  string `json:"version"`
	Maturity string `json:"maturity"`
}
