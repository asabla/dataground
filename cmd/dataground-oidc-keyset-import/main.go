package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/authn"
)

const (
	oidcKeysetImportRequestContract     = "dataground.oidc-keyset-import/oidc-discovery/v4"
	oidcProviderRegistryContract        = "dataground.oidc-provider-registry/v2"
	maximumOIDCKeysetImportRequestBytes = 64 << 10
	maximumOIDCProviderRegistryBytes    = 256 << 10
	maximumOIDCKeysetImportRequestDepth = 16
	maximumOIDCProviderProfiles         = 64
)

type oidcKeysetImportRequest struct {
	Contract               string    `json:"contract"`
	IsolationDomainID      string    `json:"isolationDomainId"`
	ProviderID             string    `json:"providerId"`
	ProviderRegistryFile   string    `json:"providerRegistryFile"`
	ProviderRegistrySHA256 string    `json:"providerRegistrySha256"`
	Sequence               uint64    `json:"sequence"`
	ExpiresAt              time.Time `json:"expiresAt"`
	PublicationFile        string    `json:"publicationFile"`
	requestFile            string
}

type oidcProviderRegistry struct {
	Contract string                `json:"contract"`
	Profiles []oidcProviderProfile `json:"profiles"`
}

type oidcProviderProfile struct {
	ID                      string                             `json:"id"`
	Issuer                  string                             `json:"issuer"`
	DiscoveryURL            string                             `json:"discoveryUrl"`
	JWKSURL                 string                             `json:"jwksUrl"`
	Algorithms              []string                           `json:"algorithms"`
	DiscoveryAuthentication oidcProviderEndpointAuthentication `json:"discoveryAuthentication"`
	JWKSAuthentication      oidcProviderEndpointAuthentication `json:"jwksAuthentication"`
}

type oidcProviderEndpointAuthentication struct {
	Kind           string          `json:"kind"`
	CredentialFile json.RawMessage `json:"credentialFile,omitempty"`
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	return runWithTransport(ctx, arguments, nil)
}

func runWithTransport(ctx context.Context, arguments []string, transport *http.Transport) error {
	if ctx == nil {
		return errors.New("OIDC keyset import context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	flags := flag.NewFlagSet("dataground-oidc-keyset-import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var requestFile string
	flags.StringVar(&requestFile, "request-file", "", "owner-only OIDC keyset import request")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || requestFile == "" {
		return errors.New("exactly one request-file is required")
	}
	request, err := readOIDCKeysetImportRequest(requestFile)
	if err != nil {
		return err
	}
	profile, err := loadOIDCProviderProfile(request)
	if err != nil {
		return err
	}
	discoveryBearerToken, jwksBearerToken, err := loadOIDCProviderBearerTokens(ctx, profile, request)
	if err != nil {
		return err
	}
	defer clear(discoveryBearerToken)
	defer clear(jwksBearerToken)
	importer, err := authn.NewOIDCDiscoveryKeysetImporter(authn.OIDCDiscoveryKeysetImportConfig{
		Issuer:               profile.Issuer,
		DiscoveryURL:         profile.DiscoveryURL,
		JWKSURL:              profile.JWKSURL,
		Algorithms:           append([]string(nil), profile.Algorithms...),
		DiscoveryBearerToken: discoveryBearerToken,
		JWKSBearerToken:      jwksBearerToken,
		Transport:            transport,
	})
	if err != nil {
		return err
	}
	operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	jwks, err := importer.Import(operationCtx)
	if err != nil {
		return err
	}
	defer clear(jwks)
	return authn.PublishOIDCJWTKeysetFile(operationCtx, authn.OIDCJWTKeysetFilePublication{
		Path:                   request.PublicationFile,
		Sequence:               request.Sequence,
		ProviderID:             request.ProviderID,
		ProviderRegistrySHA256: request.ProviderRegistrySHA256,
		ExpiresAt:              request.ExpiresAt,
		Algorithms:             append([]string(nil), profile.Algorithms...),
		JWKS:                   jwks,
	})
}

func readOIDCKeysetImportRequest(path string) (oidcKeysetImportRequest, error) {
	var request oidcKeysetImportRequest
	content, err := readStableOIDCKeysetImportRequest(path)
	if err != nil {
		return request, err
	}
	defer clear(content)
	if err := requireUniqueOIDCKeysetImportJSON(content); err != nil {
		return request, errors.New("OIDC keyset import request is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, errors.New("OIDC keyset import request is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return request, errors.New("OIDC keyset import request is invalid")
	}
	if request.Contract != oidcKeysetImportRequestContract || request.Sequence == 0 ||
		request.ExpiresAt.IsZero() || !authn.ValidOIDCProviderIsolationDomain(request.IsolationDomainID) ||
		!canonicalAbsolutePath(request.ProviderRegistryFile) ||
		!authn.ValidOIDCProviderBinding(request.ProviderID, request.ProviderRegistrySHA256) ||
		!canonicalAbsolutePath(request.PublicationFile) ||
		request.ProviderRegistryFile == request.PublicationFile ||
		request.ProviderRegistryFile == path || request.PublicationFile == path {
		return request, errors.New("OIDC keyset import request is invalid")
	}
	request.requestFile = path
	return request, nil
}

func loadOIDCProviderProfile(request oidcKeysetImportRequest) (oidcProviderProfile, error) {
	var selected oidcProviderProfile
	content, err := readStableOIDCKeysetImportFile(
		request.ProviderRegistryFile,
		maximumOIDCProviderRegistryBytes,
		0o022,
		"OIDC provider registry",
		true,
	)
	if err != nil {
		return selected, err
	}
	defer clear(content)
	digest := sha256.Sum256(content)
	if fmt.Sprintf("%x", digest) != request.ProviderRegistrySHA256 {
		return selected, errors.New("OIDC provider registry digest does not match")
	}
	if err := requireUniqueOIDCKeysetImportJSON(content); err != nil {
		return selected, errors.New("OIDC provider registry is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var registry oidcProviderRegistry
	if err := decoder.Decode(&registry); err != nil {
		return selected, errors.New("OIDC provider registry is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) ||
		registry.Contract != oidcProviderRegistryContract || len(registry.Profiles) == 0 ||
		len(registry.Profiles) > maximumOIDCProviderProfiles {
		return selected, errors.New("OIDC provider registry is invalid")
	}
	seen := make(map[string]struct{}, len(registry.Profiles))
	for _, profile := range registry.Profiles {
		if err := validateOIDCProviderProfile(profile); err != nil {
			return selected, err
		}
		if _, exists := seen[profile.ID]; exists {
			return selected, errors.New("OIDC provider registry contains duplicate profiles")
		}
		seen[profile.ID] = struct{}{}
		if profile.ID == request.ProviderID {
			selected = profile
		}
	}
	if selected.ID == "" {
		return selected, errors.New("OIDC provider profile is not registered")
	}
	return selected, nil
}

func validateOIDCProviderProfile(profile oidcProviderProfile) error {
	if !authn.ValidOIDCProviderID(profile.ID) || profile.Issuer == "" ||
		profile.DiscoveryURL == "" || profile.JWKSURL == "" ||
		profile.DiscoveryURL == profile.JWKSURL || len(profile.Algorithms) == 0 ||
		len(profile.Algorithms) > 8 ||
		!validOIDCProviderAuthentication(profile.DiscoveryAuthentication) ||
		!validOIDCProviderAuthentication(profile.JWKSAuthentication) {
		return errors.New("OIDC provider profile is invalid")
	}
	discoveryCredential, discoveryAuthenticated := profile.DiscoveryAuthentication.credentialFile()
	jwksCredential, jwksAuthenticated := profile.JWKSAuthentication.credentialFile()
	if discoveryAuthenticated && jwksAuthenticated && discoveryCredential == jwksCredential {
		return errors.New("OIDC provider endpoint credentials must be independently scoped")
	}
	if _, err := authn.NewOIDCDiscoveryKeysetImporter(authn.OIDCDiscoveryKeysetImportConfig{
		Issuer:       profile.Issuer,
		DiscoveryURL: profile.DiscoveryURL,
		JWKSURL:      profile.JWKSURL,
		Algorithms:   append([]string(nil), profile.Algorithms...),
	}); err != nil {
		return errors.New("OIDC provider profile is invalid")
	}
	seenAlgorithms := make(map[string]struct{}, len(profile.Algorithms))
	for _, algorithm := range profile.Algorithms {
		if algorithm == "" {
			return errors.New("OIDC provider profile is invalid")
		}
		if _, exists := seenAlgorithms[algorithm]; exists {
			return errors.New("OIDC provider profile contains duplicate algorithms")
		}
		seenAlgorithms[algorithm] = struct{}{}
	}
	return nil
}

func validOIDCProviderAuthentication(authentication oidcProviderEndpointAuthentication) bool {
	switch authentication.Kind {
	case "none":
		return len(authentication.CredentialFile) == 0
	case "bearer-credential-file":
		_, valid := authentication.credentialFile()
		return valid
	default:
		return false
	}
}

func (authentication oidcProviderEndpointAuthentication) credentialFile() (string, bool) {
	if len(authentication.CredentialFile) == 0 || bytes.Equal(bytes.TrimSpace(authentication.CredentialFile), []byte("null")) {
		return "", false
	}
	var path string
	if err := json.Unmarshal(authentication.CredentialFile, &path); err != nil || !canonicalAbsolutePath(path) {
		return "", false
	}
	return path, true
}

func loadOIDCProviderBearerToken(
	ctx context.Context,
	authentication oidcProviderEndpointAuthentication,
	request oidcKeysetImportRequest,
	endpoint string,
) ([]byte, error) {
	if authentication.Kind == "none" {
		return nil, nil
	}
	credentialFile, valid := authentication.credentialFile()
	if !valid || credentialFile == request.requestFile ||
		credentialFile == request.ProviderRegistryFile || credentialFile == request.PublicationFile {
		return nil, errors.New("OIDC provider credential file is invalid")
	}
	token, err := authn.LoadOIDCProviderBearerCredential(
		ctx, credentialFile, request.IsolationDomainID,
		request.ProviderID, request.ProviderRegistrySHA256, endpoint,
	)
	if err != nil {
		return nil, errors.New("OIDC provider credential is unavailable")
	}
	return token, nil
}

func loadOIDCProviderBearerTokens(
	ctx context.Context,
	profile oidcProviderProfile,
	request oidcKeysetImportRequest,
) ([]byte, []byte, error) {
	discoveryToken, err := loadOIDCProviderBearerToken(ctx, profile.DiscoveryAuthentication, request, "discovery")
	if err != nil {
		return nil, nil, err
	}
	jwksToken, err := loadOIDCProviderBearerToken(ctx, profile.JWKSAuthentication, request, "jwks")
	if err != nil {
		clear(discoveryToken)
		return nil, nil, err
	}
	return discoveryToken, jwksToken, nil
}

func readStableOIDCKeysetImportRequest(path string) ([]byte, error) {
	return readStableOIDCKeysetImportFile(
		path,
		maximumOIDCKeysetImportRequestBytes,
		0o077,
		"OIDC keyset import request",
		true,
	)
}

func readStableOIDCKeysetImportFile(
	path string,
	maximumBytes int64,
	prohibitedPermissions os.FileMode,
	description string,
	requireResolvedPath bool,
) ([]byte, error) {
	if !canonicalAbsolutePath(path) {
		return nil, fmt.Errorf("%s path is invalid", description)
	}
	if requireResolvedPath {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || resolved != path {
			return nil, fmt.Errorf("%s path contains a symlink", description)
		}
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() ||
		pathInfo.Mode().Perm()&prohibitedPermissions != 0 ||
		pathInfo.Size() <= 0 || pathInfo.Size() > maximumBytes {
		return nil, fmt.Errorf("%s file is invalid", description)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s file is unavailable", description)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !sameOIDCKeysetImportRequestFile(pathInfo, before) {
		return nil, fmt.Errorf("%s changed before reading", description)
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil || len(content) == 0 || int64(len(content)) > maximumBytes ||
		int64(len(content)) != before.Size() {
		clear(content)
		return nil, fmt.Errorf("%s content is invalid", description)
	}
	after, err := file.Stat()
	if err != nil || !sameOIDCKeysetImportRequestFile(before, after) {
		clear(content)
		return nil, fmt.Errorf("%s changed while reading", description)
	}
	pathAfter, err := os.Lstat(path)
	if err != nil || !sameOIDCKeysetImportRequestFile(after, pathAfter) {
		clear(content)
		return nil, fmt.Errorf("%s path changed while reading", description)
	}
	if requireResolvedPath {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || resolved != path {
			clear(content)
			return nil, fmt.Errorf("%s path changed while reading", description)
		}
	}
	return content, nil
}

func sameOIDCKeysetImportRequestFile(expected os.FileInfo, actual os.FileInfo) bool {
	return expected != nil && actual != nil && os.SameFile(expected, actual) &&
		expected.Size() == actual.Size() && expected.ModTime().Equal(actual.ModTime()) &&
		expected.Mode() == actual.Mode()
}

func canonicalAbsolutePath(path string) bool {
	return path != "" && strings.IndexByte(path, 0) < 0 && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func requireUniqueOIDCKeysetImportJSON(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := validateOIDCKeysetImportJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("OIDC keyset import JSON has trailing data")
	}
	return nil
}

func validateOIDCKeysetImportJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maximumOIDCKeysetImportRequestDepth {
		return errors.New("OIDC keyset import JSON is too deeply nested")
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
				return errors.New("OIDC keyset import JSON object key is invalid")
			}
			if _, exists := seen[key]; exists {
				return errors.New("OIDC keyset import JSON contains a duplicate member")
			}
			seen[key] = struct{}{}
			if err := validateOIDCKeysetImportJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := validateOIDCKeysetImportJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("OIDC keyset import JSON delimiter is invalid")
	}
	closing, err := decoder.Token()
	if err != nil || closing != matchingOIDCKeysetImportJSONDelimiter(delimiter) {
		return errors.New("OIDC keyset import JSON delimiter is unbalanced")
	}
	return nil
}

func matchingOIDCKeysetImportJSONDelimiter(open json.Delim) json.Delim {
	if open == '{' {
		return '}'
	}
	return ']'
}
