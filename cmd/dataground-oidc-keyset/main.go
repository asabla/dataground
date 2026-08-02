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
	oidcJWTKeysetPublicationRequestContract = "dataground.oidc-keyset-publication-request/v2"
	maximumOIDCJWTKeysetRequestBytes        = 64 << 10
	maximumOIDCJWTKeysetInputBytes          = 256 << 10
	maximumOIDCJWTKeysetRequestDepth        = 16
)

type oidcJWTKeysetPublicationRequest struct {
	Contract               string    `json:"contract"`
	Sequence               uint64    `json:"sequence"`
	ProviderID             string    `json:"providerId"`
	ProviderRegistrySHA256 string    `json:"providerRegistrySha256"`
	ExpiresAt              time.Time `json:"expiresAt"`
	Algorithms             []string  `json:"algorithms"`
	JWKSFile               string    `json:"jwksFile"`
	PublicationFile        string    `json:"publicationFile"`
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	if ctx == nil {
		return errors.New("OIDC keyset publication context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	flags := flag.NewFlagSet("dataground-oidc-keyset", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var requestFile string
	flags.StringVar(&requestFile, "request-file", "", "owner-only OIDC keyset publication request")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || requestFile == "" {
		return errors.New("exactly one request-file is required")
	}
	request, err := readOIDCJWTKeysetPublicationRequest(requestFile)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	jwks, err := readStableOIDCJWTKeysetFile(
		request.JWKSFile,
		maximumOIDCJWTKeysetInputBytes,
		0o022,
		"OIDC JWKS input",
	)
	if err != nil {
		return err
	}
	defer clear(jwks)
	if err := ctx.Err(); err != nil {
		return err
	}
	operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return authn.PublishOIDCJWTKeysetFile(operationCtx, authn.OIDCJWTKeysetFilePublication{
		Path:                   request.PublicationFile,
		Sequence:               request.Sequence,
		ProviderID:             request.ProviderID,
		ProviderRegistrySHA256: request.ProviderRegistrySHA256,
		ExpiresAt:              request.ExpiresAt,
		Algorithms:             append([]string(nil), request.Algorithms...),
		JWKS:                   jwks,
	})
}

func readOIDCJWTKeysetPublicationRequest(path string) (oidcJWTKeysetPublicationRequest, error) {
	content, err := readStableOIDCJWTKeysetFile(
		path,
		maximumOIDCJWTKeysetRequestBytes,
		0o077,
		"OIDC keyset publication request",
	)
	if err != nil {
		return oidcJWTKeysetPublicationRequest{}, err
	}
	defer clear(content)
	if err := requireUniqueOIDCJWTKeysetRequestJSON(content); err != nil {
		return oidcJWTKeysetPublicationRequest{}, fmt.Errorf("validate OIDC keyset publication request: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var request oidcJWTKeysetPublicationRequest
	if err := decoder.Decode(&request); err != nil {
		return oidcJWTKeysetPublicationRequest{}, fmt.Errorf("decode OIDC keyset publication request: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return oidcJWTKeysetPublicationRequest{}, errors.New("OIDC keyset publication request contains trailing data")
	}
	if request.Contract != oidcJWTKeysetPublicationRequestContract || request.Sequence == 0 ||
		request.ExpiresAt.IsZero() || len(request.Algorithms) == 0 ||
		!authn.ValidOIDCProviderBinding(request.ProviderID, request.ProviderRegistrySHA256) ||
		!validAbsoluteCanonicalPath(request.JWKSFile) ||
		!validAbsoluteCanonicalPath(request.PublicationFile) ||
		request.JWKSFile == request.PublicationFile {
		return oidcJWTKeysetPublicationRequest{}, errors.New("OIDC keyset publication request is invalid")
	}
	return request, nil
}

func readStableOIDCJWTKeysetFile(
	path string,
	maximumBytes int64,
	prohibitedPermissions os.FileMode,
	description string,
) ([]byte, error) {
	if !validAbsoluteCanonicalPath(path) {
		return nil, fmt.Errorf("%s path must be absolute and canonical", description)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", description, err)
	}
	if !pathInfo.Mode().IsRegular() || pathInfo.Mode().Perm()&prohibitedPermissions != 0 ||
		pathInfo.Size() <= 0 || pathInfo.Size() > maximumBytes {
		return nil, fmt.Errorf("%s is not a safe bounded regular file", description)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", description, err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !os.SameFile(pathInfo, before) {
		return nil, fmt.Errorf("%s changed before it could be read", description)
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil {
		clear(content)
		return nil, fmt.Errorf("read %s: %w", description, err)
	}
	after, err := file.Stat()
	pathAfter, pathErr := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() ||
		pathErr != nil || !os.SameFile(after, pathAfter) || pathAfter.Mode() != after.Mode() ||
		len(content) == 0 || int64(len(content)) > maximumBytes {
		clear(content)
		return nil, fmt.Errorf("%s changed while it was read", description)
	}
	return content, nil
}

func validAbsoluteCanonicalPath(path string) bool {
	return path != "" && strings.IndexByte(path, 0) < 0 && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func requireUniqueOIDCJWTKeysetRequestJSON(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return errors.New("request JSON must be an object")
	}
	if err := requireUniqueOIDCJWTKeysetRequestContainer(decoder, '}', 1); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("request JSON contains trailing data")
	}
	return nil
}

func requireUniqueOIDCJWTKeysetRequestContainer(
	decoder *json.Decoder,
	closing json.Delim,
	depth int,
) error {
	if depth > maximumOIDCJWTKeysetRequestDepth {
		return errors.New("request JSON is too deeply nested")
	}
	seen := map[string]struct{}{}
	for decoder.More() {
		if closing == '}' {
			memberToken, err := decoder.Token()
			if err != nil {
				return err
			}
			member, ok := memberToken.(string)
			if !ok {
				return errors.New("request JSON object member is invalid")
			}
			if _, duplicate := seen[member]; duplicate {
				return errors.New("request JSON contains a duplicate member")
			}
			seen[member] = struct{}{}
		}
		value, err := decoder.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := value.(json.Delim); ok {
			switch delimiter {
			case '{':
				if err := requireUniqueOIDCJWTKeysetRequestContainer(decoder, '}', depth+1); err != nil {
					return err
				}
			case '[':
				if err := requireUniqueOIDCJWTKeysetRequestContainer(decoder, ']', depth+1); err != nil {
					return err
				}
			default:
				return errors.New("request JSON delimiter is invalid")
			}
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := end.(json.Delim); !ok || delimiter != closing {
		return errors.New("request JSON container is not closed")
	}
	return nil
}
