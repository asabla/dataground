package rosetta

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	pathpkg "path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var isolationDomainPattern = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
var bindingResourcePattern = regexp.MustCompile(`^(rev|exe)_[0-9a-z]{20,32}$`)

func validateMaterializeRequest(request MaterializeRequest) error {
	if !isolationDomainPattern.MatchString(request.Context.IsolationDomainID) ||
		(request.Context.ResourceType != "service-revision" && request.Context.ResourceType != "execution") ||
		!bindingResourcePattern.MatchString(request.Context.ResourceID) ||
		request.Context.ResourceType == "service-revision" && !strings.HasPrefix(request.Context.ResourceID, "rev_") ||
		request.Context.ResourceType == "execution" && !strings.HasPrefix(request.Context.ResourceID, "exe_") {
		return safeFieldError("binding context")
	}
	if request.CedarSource == "" || !utf8.ValidString(request.CedarSource) || strings.ContainsRune(request.CedarSource, '\x00') {
		return safeFieldError("Cedar source")
	}
	if request.Catalog.Version != CatalogVersion {
		return safeFieldError("catalog version")
	}
	if !validPlainValue(request.Catalog.Principal.ID, 256) {
		return safeFieldError("catalog principal")
	}
	if request.Catalog.Principal.Type != "" && !validPlainValue(request.Catalog.Principal.Type, 256) ||
		!validPlainValues(request.Catalog.Principal.Roles, 256) {
		return safeFieldError("catalog principal attributes")
	}
	if len(request.Catalog.Capabilities) > maximumCapabilities {
		return safeFieldError("capability count")
	}
	seen := make(map[string]struct{}, len(request.Catalog.Capabilities))
	for _, capability := range request.Catalog.Capabilities {
		if !validPlainValue(capability.ID, 256) || !validPlainValue(capability.Kind, 32) ||
			!validPlainValue(capability.Action, 32) || !validPlainValue(capability.Selector, 4096) {
			return safeFieldError("catalog capability")
		}
		if !validCapabilityAction(capability.Kind, capability.Action) ||
			!validPlainValues(capability.Targets, 32) || !validPlainValues(capability.Binaries, 4096) ||
			!validOptionalPlainValue(capability.Access, 32) || !validOptionalPlainValue(capability.Protocol, 32) ||
			!validOptionalPlainValue(capability.Path, 4096) || !validOptionalPlainValue(capability.Server, 256) {
			return safeFieldError("catalog capability fields")
		}
		if len(capability.Targets) > 0 && !slices.Contains(capability.Targets, TargetOpenShell) {
			return safeFieldError("capability target")
		}
		if containsDuplicate(capability.Targets) || containsDuplicate(capability.Binaries) {
			return safeFieldError("capability list")
		}
		if _, exists := seen[capability.ID]; exists {
			return safeFieldError("capability identifier")
		}
		seen[capability.ID] = struct{}{}
	}
	if request.OpenShell.RunAsUser == "root" || request.OpenShell.RunAsUser == "0" ||
		request.OpenShell.RunAsGroup == "root" || request.OpenShell.RunAsGroup == "0" {
		return safeFieldError("OpenShell process identity")
	}
	if !validOptionalPlainValue(request.OpenShell.RunAsUser, 256) || !validOptionalPlainValue(request.OpenShell.RunAsGroup, 256) {
		return safeFieldError("OpenShell process identity")
	}
	return nil
}

func validateCompileResponse(
	request compileRequest,
	response compileResponse,
	expectedCompiler string,
	expectedContract string,
) (Materialization, error) {
	if response.Target != TargetOpenShell || response.Metadata.CompilerVersion != expectedCompiler ||
		response.Metadata.CatalogVersion != CatalogVersion || response.Metadata.TargetContractVersion != expectedContract ||
		response.Metadata.Mode != ModeStrict {
		return Materialization{}, ErrIncompatible
	}
	if !sha256Pattern.MatchString(response.Metadata.InputSHA256) || !sha256Pattern.MatchString(response.Metadata.ArtifactSHA256) {
		return Materialization{}, ErrProtocol
	}
	inputDigest, err := digestCompileInput(request)
	if err != nil || response.Metadata.InputSHA256 != inputDigest {
		return Materialization{}, ErrProtocol
	}
	if len(response.Artifacts) != 1 {
		return Materialization{}, ErrProtocol
	}
	artifact := response.Artifacts[0]
	if artifact.Name != "policy.yaml" || artifact.PathHint != "policy.yaml" || artifact.MediaType != "application/yaml" ||
		artifact.Target != TargetOpenShell || artifact.Encoding != "plain" || response.Output != artifact.Content {
		return Materialization{}, ErrProtocol
	}
	actualDigest := sha256.Sum256([]byte(artifact.Content))
	if hex.EncodeToString(actualDigest[:]) != response.Metadata.ArtifactSHA256 {
		return Materialization{}, ErrProtocol
	}
	if err := validateOpenShellPolicy([]byte(artifact.Content)); err != nil {
		return Materialization{}, fmt.Errorf("%w: OpenShell artifact failed independent validation", ErrProtocol)
	}
	mappings, err := validateDecisions(request.Catalog.Capabilities, response.Decisions)
	if err != nil {
		return Materialization{}, err
	}
	diagnostics := make([]Diagnostic, 0, len(response.Diagnostics))
	for _, candidate := range response.Diagnostics {
		if !validPlainValue(candidate.Code, 256) ||
			(candidate.Severity != "info" && candidate.Severity != "warning" && candidate.Severity != "error") {
			return Materialization{}, ErrProtocol
		}
		if candidate.Severity == "error" {
			return Materialization{}, ErrRejected
		}
		diagnostics = append(diagnostics, Diagnostic{
			Severity: candidate.Severity, Code: candidate.Code, Recoverable: candidate.Recoverable,
		})
	}
	digest := "sha256:" + response.Metadata.ArtifactSHA256
	bindingDigest := digestBinding(request.Context, response.Metadata.InputSHA256, response.Metadata.ArtifactSHA256)
	return Materialization{
		Context:   request.Context,
		BundleID:  "rosetta-" + strings.TrimPrefix(bindingDigest, "sha256:"),
		Content:   append([]byte(nil), artifact.Content...),
		MediaType: artifact.MediaType,
		Mappings:  mappings, Diagnostics: diagnostics,
		Provenance: Provenance{
			CompilerVersion: response.Metadata.CompilerVersion, CatalogVersion: response.Metadata.CatalogVersion,
			TargetContractVersion: response.Metadata.TargetContractVersion, Mode: response.Metadata.Mode,
			InputDigest: "sha256:" + response.Metadata.InputSHA256, ArtifactDigest: digest,
			BindingDigest: bindingDigest,
		},
	}, nil
}

func digestCompileInput(request compileRequest) (string, error) {
	catalog, err := json.Marshal(request.Catalog)
	if err != nil {
		return "", err
	}
	options, err := json.Marshal(request.Options)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, value := range [][]byte{
		[]byte(request.Source), []byte(request.Target), []byte(request.Mode), catalog, options,
	} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(value)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func digestBinding(context BindingContext, inputDigest, artifactDigest string) string {
	digest := sha256.Sum256([]byte(
		"dataground.rosetta-binding/v1\x00" + context.IsolationDomainID + "\x00" +
			context.ResourceType + "\x00" + context.ResourceID + "\x00" + inputDigest + "\x00" + artifactDigest,
	))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validateDecisions(capabilities []Capability, decisions []decision) ([]CapabilityMapping, error) {
	expected := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		expected[capability.ID] = struct{}{}
	}
	mappings := make([]CapabilityMapping, 0, len(decisions))
	seen := make(map[string]struct{}, len(decisions))
	for _, candidate := range decisions {
		if _, exists := expected[candidate.CapabilityID]; !exists {
			return nil, ErrProtocol
		}
		if _, exists := seen[candidate.CapabilityID]; exists {
			return nil, ErrProtocol
		}
		seen[candidate.CapabilityID] = struct{}{}
		status := "denied"
		if candidate.Allowed {
			status = "exact"
		}
		mappings = append(mappings, CapabilityMapping{CapabilityID: candidate.CapabilityID, Status: status})
	}
	if len(seen) != len(expected) {
		return nil, ErrProtocol
	}
	sort.Slice(mappings, func(left, right int) bool { return mappings[left].CapabilityID < mappings[right].CapabilityID })
	return mappings, nil
}

type openShellPolicy struct {
	Version          int `yaml:"version"`
	FilesystemPolicy *struct {
		IncludeWorkdir *bool    `yaml:"include_workdir"`
		ReadOnly       []string `yaml:"read_only"`
		ReadWrite      []string `yaml:"read_write"`
	} `yaml:"filesystem_policy"`
	Landlock *struct {
		Compatibility string `yaml:"compatibility"`
	} `yaml:"landlock"`
	Process *struct {
		RunAsUser  string `yaml:"run_as_user"`
		RunAsGroup string `yaml:"run_as_group"`
	} `yaml:"process"`
	NetworkPolicies map[string]struct {
		Name      string `yaml:"name"`
		Endpoints []struct {
			Host        string `yaml:"host"`
			Port        int    `yaml:"port"`
			Path        string `yaml:"path"`
			Protocol    string `yaml:"protocol"`
			Enforcement string `yaml:"enforcement"`
			Access      string `yaml:"access"`
		} `yaml:"endpoints"`
		Binaries []struct {
			Path string `yaml:"path"`
		} `yaml:"binaries"`
	} `yaml:"network_policies"`
}

func validateOpenShellPolicy(content []byte) error {
	var policy openShellPolicy
	decoder := yaml.NewDecoder(strings.NewReader(string(content)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&policy); err != nil {
		return errors.New("decode policy")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("multiple policy documents")
	}
	if policy.Version != 1 || policy.FilesystemPolicy == nil || policy.FilesystemPolicy.IncludeWorkdir == nil ||
		*policy.FilesystemPolicy.IncludeWorkdir || policy.FilesystemPolicy.ReadOnly == nil || policy.FilesystemPolicy.ReadWrite == nil ||
		policy.Landlock == nil || policy.Landlock.Compatibility != "hard_requirement" || policy.NetworkPolicies == nil {
		return errors.New("unsafe policy baseline")
	}
	if policy.Process == nil {
		return errors.New("missing process identity")
	}
	if !validNonRootIdentity(policy.Process.RunAsUser) || !validNonRootIdentity(policy.Process.RunAsGroup) {
		return errors.New("unsafe process identity")
	}
	for _, value := range append(slices.Clone(policy.FilesystemPolicy.ReadOnly), policy.FilesystemPolicy.ReadWrite...) {
		if !isCleanAbsolutePath(value) {
			return errors.New("unsafe filesystem path")
		}
	}
	if slices.Contains(policy.FilesystemPolicy.ReadWrite, "/") {
		return errors.New("writable filesystem root")
	}
	for _, network := range policy.NetworkPolicies {
		if network.Name == "" || len(network.Endpoints) == 0 || len(network.Binaries) == 0 {
			return errors.New("incomplete network policy")
		}
		for _, endpoint := range network.Endpoints {
			if endpoint.Host == "" || endpoint.Port < 1 || endpoint.Port > 65535 || endpoint.Enforcement != "enforce" {
				return errors.New("unsafe network endpoint")
			}
			if endpoint.Protocol != "" && endpoint.Protocol != "rest" && endpoint.Protocol != "websocket" && endpoint.Protocol != "graphql" {
				return errors.New("unsupported network protocol")
			}
			if endpoint.Protocol == "" && endpoint.Access != "" || endpoint.Protocol != "" &&
				endpoint.Access != "read-only" && endpoint.Access != "read-write" && endpoint.Access != "full" {
				return errors.New("unsafe network access preset")
			}
		}
		for _, binary := range network.Binaries {
			if !isCleanAbsolutePath(binary.Path) {
				return errors.New("unsafe network binary")
			}
		}
	}
	return nil
}

func isCleanAbsolutePath(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.Contains(value, "\\") && pathpkg.Clean(value) == value
}

func validNonRootIdentity(value string) bool {
	return validPlainValue(value, 256) && value != "root" && value != "0"
}

func validPlainValue(value string, maximumLength int) bool {
	if value == "" || len(value) > maximumLength || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validOptionalPlainValue(value string, maximumLength int) bool {
	return value == "" || validPlainValue(value, maximumLength)
}

func validPlainValues(values []string, maximumLength int) bool {
	for _, value := range values {
		if !validPlainValue(value, maximumLength) {
			return false
		}
	}
	return true
}

func validCapabilityAction(kind, action string) bool {
	switch kind {
	case "filesystem":
		return action == "read" || action == "write"
	case "tool":
		return action == "use"
	case "command":
		return action == "execute"
	case "network":
		return action == "connect"
	default:
		return false
	}
}

func containsDuplicate(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}
