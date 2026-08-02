package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/persistence"
)

const (
	oidcSecurityConfigurationContract = "dataground.api-security/oidc-dpop/v2"
	maximumSecurityConfigurationBytes = 64 << 10
	maximumAPICedarPolicyBytes        = 1 << 20
	maximumSecurityConfigurationDepth = 16
)

type durationValue struct {
	value time.Duration
	set   bool
}

func (value *durationValue) UnmarshalJSON(encoded []byte) error {
	var text string
	if err := json.Unmarshal(encoded, &text); err != nil || text == "" {
		return errors.New("duration must be a non-empty string")
	}
	duration, err := time.ParseDuration(text)
	if err != nil {
		return errors.New("duration is invalid")
	}
	value.value = duration
	value.set = true
	return nil
}

type oidcSecurityConfiguration struct {
	Contract              string   `json:"contract"`
	Issuer                string   `json:"issuer"`
	ExternalOrigin        string   `json:"externalOrigin"`
	KeysetPublicationFile string   `json:"keysetPublicationFile"`
	Algorithms            []string `json:"algorithms"`
	JWT                   struct {
		ClockSkew       durationValue `json:"clockSkew"`
		MaximumLifetime durationValue `json:"maximumLifetime"`
	} `json:"jwt"`
	DPoP struct {
		ClockSkew       durationValue `json:"clockSkew"`
		MaximumProofAge durationValue `json:"maximumProofAge"`
	} `json:"dpop"`
	KeysetRefresh struct {
		Interval durationValue `json:"interval"`
		Timeout  durationValue `json:"timeout"`
	} `json:"keysetRefresh"`
	Admission struct {
		Generation           uint64        `json:"generation"`
		Window               durationValue `json:"window"`
		GlobalBurst          uint32        `json:"globalBurst"`
		IsolationDomainBurst uint32        `json:"isolationDomainBurst"`
		CredentialBurst      uint32        `json:"credentialBurst"`
		DeploymentProfile    string        `json:"deploymentProfile"`
		CapacityEvidenceFile string        `json:"capacityEvidenceFile"`
		CapacityEvidenceHash string        `json:"capacityEvidenceSha256"`
	} `json:"admission"`
	Authorization struct {
		PolicySetID string `json:"policySetId"`
		PolicyFile  string `json:"policyFile"`
	} `json:"authorization"`
	capacityEvidence persistence.AuthenticationRateLimitCapacityEvidence
}

func loadOIDCSecurityConfiguration(path string) (oidcSecurityConfiguration, []byte, error) {
	sourceRevision, goVersion, err := currentOIDCSecurityBuild()
	if err != nil {
		return oidcSecurityConfiguration{}, nil, err
	}
	return loadOIDCSecurityConfigurationForBuild(path, sourceRevision, goVersion)
}

func loadOIDCSecurityConfigurationForBuild(
	path string,
	sourceRevision string,
	goVersion string,
) (oidcSecurityConfiguration, []byte, error) {
	var configuration oidcSecurityConfiguration
	encoded, err := readStableConfigurationFile(path, maximumSecurityConfigurationBytes)
	if err != nil {
		return configuration, nil, errors.New("OIDC security configuration file is invalid")
	}
	defer clear(encoded)
	if err := requireUniqueConfigurationJSON(encoded); err != nil {
		return configuration, nil, errors.New("OIDC security configuration file is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configuration); err != nil {
		return configuration, nil, errors.New("OIDC security configuration file is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return configuration, nil, errors.New("OIDC security configuration file is invalid")
	}
	if configuration.Contract != oidcSecurityConfigurationContract ||
		configuration.Issuer == "" || configuration.ExternalOrigin == "" ||
		len(configuration.Algorithms) == 0 || configuration.Authorization.PolicySetID == "" ||
		!configuration.JWT.ClockSkew.set || !configuration.JWT.MaximumLifetime.set ||
		!configuration.DPoP.ClockSkew.set || !configuration.DPoP.MaximumProofAge.set ||
		!configuration.KeysetRefresh.Interval.set || !configuration.KeysetRefresh.Timeout.set ||
		configuration.Admission.Generation == 0 ||
		configuration.Admission.Generation > math.MaxInt64 ||
		!configuration.Admission.Window.set ||
		configuration.Admission.DeploymentProfile == "" ||
		configuration.Admission.CapacityEvidenceFile == "" ||
		configuration.Admission.CapacityEvidenceHash == "" {
		return configuration, nil, errors.New("OIDC security configuration is incomplete")
	}
	if _, err := authn.NewOIDCJWTKeysetFileSource(configuration.KeysetPublicationFile); err != nil {
		return configuration, nil, errors.New("OIDC keyset publication configuration is invalid")
	}
	if !canonicalAbsolutePath(configuration.Authorization.PolicyFile) {
		return configuration, nil, errors.New("API authorization policy path is invalid")
	}
	policy, err := readStableConfigurationFile(
		configuration.Authorization.PolicyFile,
		maximumAPICedarPolicyBytes,
	)
	if err != nil {
		return configuration, nil, errors.New("API authorization policy file is invalid")
	}
	if _, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{
		PolicySetID: configuration.Authorization.PolicySetID,
		Schema:      authz.CanonicalAPICedarSchema(),
		Policies:    policy,
	}); err != nil {
		clear(policy)
		return configuration, nil, errors.New("API authorization policy is invalid")
	}
	if _, err := api.NewDPoPRequestBinder(configuration.ExternalOrigin); err != nil {
		clear(policy)
		return configuration, nil, errors.New("OIDC external origin is invalid")
	}
	if !configuration.keysetRefreshPolicy().Valid() || !configuration.admissionPolicy().Valid() {
		clear(policy)
		return configuration, nil, errors.New("OIDC security policy is invalid")
	}
	evidence, err := loadAuthenticationRateLimitCapacityEvidence(
		configuration.Admission.CapacityEvidenceFile,
		configuration.Admission.CapacityEvidenceHash,
		sourceRevision,
		goVersion,
		configuration.Admission.DeploymentProfile,
		configuration.admissionPolicy(),
	)
	if err != nil {
		clear(policy)
		return configuration, nil, err
	}
	configuration.capacityEvidence = evidence
	return configuration, policy, nil
}

func composeOIDCSecurity(
	ctx context.Context,
	repository *persistence.Repository,
	configuration oidcSecurityConfiguration,
	policy []byte,
) (*api.DurableOIDCDPoPAssembly, error) {
	if ctx == nil || repository == nil || !repository.Configured() {
		return nil, errors.New("durable OIDC security repository is required")
	}
	source, err := authn.NewOIDCJWTKeysetFileSource(configuration.KeysetPublicationFile)
	if err != nil {
		return nil, err
	}
	authorizer, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{
		PolicySetID: configuration.Authorization.PolicySetID,
		Schema:      authz.CanonicalAPICedarSchema(),
		Policies:    append([]byte(nil), policy...),
	})
	if err != nil {
		return nil, err
	}
	limiter, err := api.NewCapacityBoundPostgreSQLAuthenticationRateLimiter(
		repository,
		configuration.Admission.Generation,
		configuration.admissionPolicy(),
		configuration.capacityEvidence,
	)
	if err != nil {
		return nil, err
	}
	return api.NewDurableOIDCDPoPAssembly(ctx, api.DurableOIDCDPoPConfig{
		Repository:     repository,
		Authorizer:     authorizer,
		RateLimiter:    limiter,
		ExternalOrigin: configuration.ExternalOrigin,
		OIDC: authn.ReloadableOIDCJWTConfig{
			Issuer:          configuration.Issuer,
			Audience:        authn.APIAudience,
			Algorithms:      append([]string(nil), configuration.Algorithms...),
			ClockSkew:       configuration.JWT.ClockSkew.value,
			MaximumLifetime: configuration.JWT.MaximumLifetime.value,
			Source:          source,
		},
		KeysetRefresh:   configuration.keysetRefreshPolicy(),
		DPoPClockSkew:   configuration.DPoP.ClockSkew.value,
		MaximumProofAge: configuration.DPoP.MaximumProofAge.value,
	})
}

func (configuration oidcSecurityConfiguration) keysetRefreshPolicy() authn.OIDCJWTKeysetRefreshPolicy {
	return authn.OIDCJWTKeysetRefreshPolicy{
		Interval: configuration.KeysetRefresh.Interval.value,
		Timeout:  configuration.KeysetRefresh.Timeout.value,
	}
}

func (configuration oidcSecurityConfiguration) admissionPolicy() persistence.AuthenticationRateLimitPolicy {
	return persistence.AuthenticationRateLimitPolicy{
		Window:               configuration.Admission.Window.value,
		GlobalBurst:          configuration.Admission.GlobalBurst,
		IsolationDomainBurst: configuration.Admission.IsolationDomainBurst,
		CredentialBurst:      configuration.Admission.CredentialBurst,
	}
}

func readStableConfigurationFile(path string, maximumBytes int64) ([]byte, error) {
	return readStableConfigurationFileWithPermissions(path, maximumBytes, 0o022)
}

func readStablePrivateConfigurationFile(path string, maximumBytes int64) ([]byte, error) {
	return readStableConfigurationFileWithPermissions(path, maximumBytes, 0o077)
}

func readStableConfigurationFileWithPermissions(
	path string,
	maximumBytes int64,
	forbiddenPermissions os.FileMode,
) ([]byte, error) {
	if !canonicalAbsolutePath(path) || maximumBytes <= 0 {
		return nil, errors.New("configuration file path is invalid")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !safeConfigurationFile(pathInfo, maximumBytes, forbiddenPermissions) {
		return nil, errors.New("configuration file is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("configuration file is unavailable")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !os.SameFile(pathInfo, before) ||
		!safeConfigurationFile(before, maximumBytes, forbiddenPermissions) {
		return nil, errors.New("configuration file changed before reading")
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil || len(content) == 0 || int64(len(content)) > maximumBytes ||
		int64(len(content)) != before.Size() {
		clear(content)
		return nil, errors.New("configuration file content is invalid")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
		clear(content)
		return nil, errors.New("configuration file changed while reading")
	}
	pathAfter, err := os.Lstat(path)
	if err != nil || !os.SameFile(after, pathAfter) || after.Size() != pathAfter.Size() ||
		!after.ModTime().Equal(pathAfter.ModTime()) || after.Mode() != pathAfter.Mode() {
		clear(content)
		return nil, errors.New("configuration file path changed while reading")
	}
	return content, nil
}

func safeConfigurationFile(info os.FileInfo, maximumBytes int64, forbiddenPermissions os.FileMode) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&forbiddenPermissions == 0 &&
		info.Size() > 0 && info.Size() <= maximumBytes
}

func canonicalAbsolutePath(path string) bool {
	return path != "" && strings.IndexByte(path, 0) < 0 && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func requireUniqueConfigurationJSON(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := validateConfigurationJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("configuration JSON has trailing data")
	}
	return nil
}

func validateConfigurationJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maximumSecurityConfigurationDepth {
		return errors.New("configuration JSON is too deeply nested")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("configuration JSON object key is invalid")
			}
			if _, exists := seen[key]; exists {
				return errors.New("configuration JSON contains a duplicate member")
			}
			seen[key] = struct{}{}
			if err := validateConfigurationJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := validateConfigurationJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("configuration JSON delimiter is invalid")
	}
	closing, err := decoder.Token()
	if err != nil || closing != matchingJSONDelimiter(delimiter) {
		return errors.New("configuration JSON delimiter is unbalanced")
	}
	return nil
}

func matchingJSONDelimiter(open json.Delim) json.Delim {
	if open == '{' {
		return '}'
	}
	return ']'
}

func requireExplicitLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parse HTTP address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("OIDC security requires an explicit loopback IP address")
	}
	return nil
}
