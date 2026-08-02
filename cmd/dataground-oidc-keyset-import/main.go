package main

import (
	"bytes"
	"context"
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
	oidcKeysetImportRequestContract     = "dataground.oidc-keyset-import/oidc-discovery/v1"
	maximumOIDCKeysetImportRequestBytes = 64 << 10
	maximumOIDCKeysetImportRequestDepth = 16
)

type oidcKeysetImportRequest struct {
	Contract        string    `json:"contract"`
	Issuer          string    `json:"issuer"`
	DiscoveryURL    string    `json:"discoveryUrl"`
	JWKSURL         string    `json:"jwksUrl"`
	Sequence        uint64    `json:"sequence"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Algorithms      []string  `json:"algorithms"`
	PublicationFile string    `json:"publicationFile"`
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
	importer, err := authn.NewOIDCDiscoveryKeysetImporter(authn.OIDCDiscoveryKeysetImportConfig{
		Issuer:       request.Issuer,
		DiscoveryURL: request.DiscoveryURL,
		JWKSURL:      request.JWKSURL,
		Algorithms:   append([]string(nil), request.Algorithms...),
		Transport:    transport,
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
		Path:       request.PublicationFile,
		Sequence:   request.Sequence,
		ExpiresAt:  request.ExpiresAt,
		Algorithms: append([]string(nil), request.Algorithms...),
		JWKS:       jwks,
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
		request.ExpiresAt.IsZero() || len(request.Algorithms) == 0 ||
		!canonicalAbsolutePath(request.PublicationFile) {
		return request, errors.New("OIDC keyset import request is invalid")
	}
	return request, nil
}

func readStableOIDCKeysetImportRequest(path string) ([]byte, error) {
	if !canonicalAbsolutePath(path) {
		return nil, errors.New("OIDC keyset import request path is invalid")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode().Perm()&0o077 != 0 ||
		pathInfo.Size() <= 0 || pathInfo.Size() > maximumOIDCKeysetImportRequestBytes {
		return nil, errors.New("OIDC keyset import request file is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("OIDC keyset import request file is unavailable")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !sameOIDCKeysetImportRequestFile(pathInfo, before) {
		return nil, errors.New("OIDC keyset import request changed before reading")
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumOIDCKeysetImportRequestBytes+1))
	if err != nil || len(content) == 0 || len(content) > maximumOIDCKeysetImportRequestBytes ||
		int64(len(content)) != before.Size() {
		clear(content)
		return nil, errors.New("OIDC keyset import request content is invalid")
	}
	after, err := file.Stat()
	if err != nil || !sameOIDCKeysetImportRequestFile(before, after) {
		clear(content)
		return nil, errors.New("OIDC keyset import request changed while reading")
	}
	pathAfter, err := os.Lstat(path)
	if err != nil || !sameOIDCKeysetImportRequestFile(after, pathAfter) {
		clear(content)
		return nil, errors.New("OIDC keyset import request path changed while reading")
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
