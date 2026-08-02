package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/authn"
)

const (
	oidcProviderCredentialRequestContract     = "dataground.oidc-provider-credential-request/v1"
	maximumOIDCProviderCredentialRequestBytes = 64 << 10
	maximumOIDCProviderCredentialTokenBytes   = 8 << 10
	maximumOIDCProviderCredentialRequestDepth = 16
)

type oidcProviderCredentialRequest struct {
	Contract               string          `json:"contract"`
	Operation              string          `json:"operation"`
	Generation             uint64          `json:"generation"`
	ProviderID             string          `json:"providerId"`
	ProviderRegistrySHA256 string          `json:"providerRegistrySha256"`
	Endpoint               string          `json:"endpoint"`
	ActivatedAt            json.RawMessage `json:"activatedAt,omitempty"`
	ExpiresAt              json.RawMessage `json:"expiresAt,omitempty"`
	RevokedAt              json.RawMessage `json:"revokedAt,omitempty"`
	BearerTokenFile        json.RawMessage `json:"bearerTokenFile,omitempty"`
	PublicationFile        string          `json:"publicationFile"`
	activation             time.Time
	expiry                 time.Time
	revocation             time.Time
	tokenFile              string
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	if ctx == nil {
		return errors.New("OIDC provider credential context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	flags := flag.NewFlagSet("dataground-oidc-provider-credential", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var requestFile string
	flags.StringVar(&requestFile, "request-file", "", "owner-only OIDC provider credential request")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || requestFile == "" {
		return errors.New("exactly one request-file is required")
	}
	request, err := readOIDCProviderCredentialRequest(requestFile)
	if err != nil {
		return err
	}
	var token []byte
	if request.Operation == "activate" {
		token, err = readStableOIDCProviderCredentialCommandFile(
			request.tokenFile, maximumOIDCProviderCredentialTokenBytes, "OIDC provider bearer token",
		)
		if err != nil {
			return err
		}
		defer clear(token)
		if !authn.ValidOIDCProviderBearerToken(token) {
			return errors.New("OIDC provider bearer token is invalid")
		}
	}
	operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return authn.PublishOIDCProviderCredential(operationCtx, authn.OIDCProviderCredentialPublication{
		Path: request.PublicationFile, Generation: request.Generation, ProviderID: request.ProviderID,
		ProviderRegistrySHA256: request.ProviderRegistrySHA256, Endpoint: request.Endpoint,
		ActivatedAt: request.activation, ExpiresAt: request.expiry,
		RevokedAt: request.revocation, Revoked: request.Operation == "revoke", BearerToken: token,
	})
}

func readOIDCProviderCredentialRequest(path string) (oidcProviderCredentialRequest, error) {
	var request oidcProviderCredentialRequest
	content, err := readStableOIDCProviderCredentialCommandFile(
		path, maximumOIDCProviderCredentialRequestBytes, "OIDC provider credential request",
	)
	if err != nil {
		return request, err
	}
	defer clear(content)
	if err := requireUniqueOIDCProviderCredentialJSON(content); err != nil {
		return request, errors.New("OIDC provider credential request is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, errors.New("OIDC provider credential request is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) ||
		request.Contract != oidcProviderCredentialRequestContract || request.Generation == 0 ||
		!authn.ValidOIDCProviderBinding(request.ProviderID, request.ProviderRegistrySHA256) ||
		!authn.ValidOIDCProviderEndpoint(request.Endpoint) || !canonicalOIDCProviderCredentialPath(request.PublicationFile) ||
		request.PublicationFile == path {
		return request, errors.New("OIDC provider credential request is invalid")
	}
	switch request.Operation {
	case "activate":
		if rawJSONNullOrMissing(request.ActivatedAt) || rawJSONNullOrMissing(request.ExpiresAt) ||
			rawJSONNullOrMissing(request.BearerTokenFile) || len(request.RevokedAt) != 0 ||
			json.Unmarshal(request.ActivatedAt, &request.activation) != nil ||
			json.Unmarshal(request.ExpiresAt, &request.expiry) != nil ||
			json.Unmarshal(request.BearerTokenFile, &request.tokenFile) != nil ||
			request.activation.IsZero() || request.expiry.IsZero() ||
			!canonicalOIDCProviderCredentialPath(request.tokenFile) || request.tokenFile == path ||
			request.tokenFile == request.PublicationFile {
			return request, errors.New("OIDC provider credential activation request is invalid")
		}
	case "revoke":
		if len(request.ActivatedAt) != 0 || len(request.ExpiresAt) != 0 || len(request.BearerTokenFile) != 0 ||
			rawJSONNullOrMissing(request.RevokedAt) || json.Unmarshal(request.RevokedAt, &request.revocation) != nil ||
			request.revocation.IsZero() {
			return request, errors.New("OIDC provider credential revocation request is invalid")
		}
	default:
		return request, errors.New("OIDC provider credential operation is invalid")
	}
	return request, nil
}

func rawJSONNullOrMissing(value json.RawMessage) bool {
	return len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func readStableOIDCProviderCredentialCommandFile(path string, maximumBytes int64, description string) ([]byte, error) {
	if !canonicalOIDCProviderCredentialPath(path) {
		return nil, fmt.Errorf("%s path is invalid", description)
	}
	directoryPath := filepath.Dir(path)
	directoryInfo, err := os.Lstat(directoryPath)
	resolvedDirectory, directoryResolveErr := filepath.EvalSymlinks(directoryPath)
	if err != nil || directoryResolveErr != nil || resolvedDirectory != directoryPath ||
		!directoryInfo.IsDir() || directoryInfo.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s directory is invalid", description)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return nil, fmt.Errorf("%s path contains a symlink", description)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode().Perm()&0o177 != 0 ||
		pathInfo.Size() <= 0 || pathInfo.Size() > maximumBytes {
		return nil, fmt.Errorf("%s file is invalid", description)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s file is unavailable", description)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !sameOIDCProviderCredentialCommandFile(pathInfo, before) {
		return nil, fmt.Errorf("%s changed before reading", description)
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil || len(content) == 0 || int64(len(content)) > maximumBytes || int64(len(content)) != before.Size() {
		clear(content)
		return nil, fmt.Errorf("%s content is invalid", description)
	}
	after, statErr := file.Stat()
	pathAfter, pathErr := os.Lstat(path)
	resolvedAfter, resolveErr := filepath.EvalSymlinks(path)
	directoryAfter, directoryErr := os.Lstat(directoryPath)
	if statErr != nil || pathErr != nil || resolveErr != nil || resolvedAfter != path ||
		!sameOIDCProviderCredentialCommandFile(before, after) ||
		!sameOIDCProviderCredentialCommandFile(after, pathAfter) || directoryErr != nil ||
		!os.SameFile(directoryInfo, directoryAfter) || directoryInfo.Mode() != directoryAfter.Mode() {
		clear(content)
		return nil, fmt.Errorf("%s changed while reading", description)
	}
	return content, nil
}

func sameOIDCProviderCredentialCommandFile(expected, actual os.FileInfo) bool {
	return expected != nil && actual != nil && os.SameFile(expected, actual) && expected.Size() == actual.Size() &&
		expected.ModTime().Equal(actual.ModTime()) && expected.Mode() == actual.Mode()
}

func canonicalOIDCProviderCredentialPath(path string) bool {
	return path != "" && strings.IndexByte(path, 0) < 0 && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func requireUniqueOIDCProviderCredentialJSON(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := validateOIDCProviderCredentialJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("OIDC provider credential JSON has trailing data")
	}
	return nil
}

func validateOIDCProviderCredentialJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maximumOIDCProviderCredentialRequestDepth {
		return errors.New("OIDC provider credential JSON is too deeply nested")
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
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("OIDC provider credential JSON key is invalid")
			}
			if _, exists := seen[key]; exists {
				return errors.New("OIDC provider credential JSON contains a duplicate member")
			}
			seen[key] = struct{}{}
			if err := validateOIDCProviderCredentialJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := validateOIDCProviderCredentialJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("OIDC provider credential JSON delimiter is invalid")
	}
	closing, err := decoder.Token()
	if err != nil || (delimiter == '{' && closing != json.Delim('}')) || (delimiter == '[' && closing != json.Delim(']')) {
		return errors.New("OIDC provider credential JSON delimiter is unbalanced")
	}
	return nil
}
