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
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/persistence"
)

const maximumOIDCIdentityRequestBytes = 64 << 10

type oidcIdentityRequest struct {
	Operation         string              `json:"operation"`
	IsolationDomainID string              `json:"isolationDomainId"`
	Issuer            string              `json:"issuer"`
	Subject           string              `json:"subject"`
	PrincipalID       string              `json:"principalId"`
	PrincipalKind     authn.PrincipalKind `json:"principalKind,omitempty"`
	ActorID           string              `json:"actorId"`
	Reason            string              `json:"reason"`
	CorrelationID     string              `json:"correlationId"`
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("dataground-oidc-identity", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var requestFile string
	flags.StringVar(&requestFile, "request-file", "", "owner-only OIDC identity request file")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || requestFile == "" {
		return errors.New("exactly one request-file is required")
	}
	request, err := readOIDCIdentityRequest(requestFile)
	if err != nil {
		return err
	}
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATAGROUND_DATABASE_URL is required")
	}

	operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	database, err := persistence.OpenSQL(operationCtx, databaseURL)
	if err != nil {
		return err
	}
	if err := persistence.RequireCurrentSchema(operationCtx, database); err != nil {
		database.Close()
		return err
	}
	if err := database.Close(); err != nil {
		return err
	}
	pool, err := persistence.OpenPool(operationCtx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	reasonDigest := sha256.Sum256([]byte(request.Reason))
	repository := persistence.NewRepository(pool)
	externalIdentity := authn.OIDCIdentity{Issuer: request.Issuer, Subject: request.Subject}
	switch request.Operation {
	case "register":
		return repository.RegisterOIDCIdentity(
			operationCtx,
			persistence.OIDCIdentityRegistration{
				IsolationDomainID:         request.IsolationDomainID,
				Identity:                  externalIdentity,
				PrincipalID:               request.PrincipalID,
				PrincipalKind:             request.PrincipalKind,
				RegisteredBy:              request.ActorID,
				RegistrationCorrelationID: request.CorrelationID,
				ReasonDigest:              reasonDigest[:],
			},
		)
	case "revoke":
		return repository.RevokeOIDCIdentity(
			operationCtx,
			persistence.OIDCIdentityRevocation{
				IsolationDomainID:       request.IsolationDomainID,
				Identity:                externalIdentity,
				PrincipalID:             request.PrincipalID,
				RevokedBy:               request.ActorID,
				RevocationCorrelationID: request.CorrelationID,
				ReasonDigest:            reasonDigest[:],
			},
		)
	default:
		return errors.New("OIDC identity operation is invalid")
	}
}

func readOIDCIdentityRequest(path string) (oidcIdentityRequest, error) {
	file, err := os.Open(path)
	if err != nil {
		return oidcIdentityRequest{}, fmt.Errorf("open OIDC identity request: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return oidcIdentityRequest{}, fmt.Errorf("inspect OIDC identity request: %w", err)
	}
	if !info.Mode().IsRegular() ||
		info.Mode().Perm()&0o077 != 0 ||
		info.Size() <= 0 ||
		info.Size() > maximumOIDCIdentityRequestBytes {
		return oidcIdentityRequest{}, errors.New(
			"OIDC identity request must be an owner-only non-empty regular file no larger than 64 KiB",
		)
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumOIDCIdentityRequestBytes+1))
	if err != nil {
		return oidcIdentityRequest{}, fmt.Errorf("read OIDC identity request: %w", err)
	}
	defer clear(content)
	if len(content) == 0 || len(content) > maximumOIDCIdentityRequestBytes {
		return oidcIdentityRequest{}, errors.New(
			"OIDC identity request must be an owner-only non-empty regular file no larger than 64 KiB",
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var request oidcIdentityRequest
	if err := decoder.Decode(&request); err != nil {
		return oidcIdentityRequest{}, fmt.Errorf("decode OIDC identity request: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return oidcIdentityRequest{}, err
	}
	if err := request.validate(); err != nil {
		return oidcIdentityRequest{}, err
	}
	return request, nil
}

func (request oidcIdentityRequest) validate() error {
	if !validReason(request.Reason) {
		return errors.New("OIDC identity reason is invalid")
	}
	reasonDigest := sha256.Sum256([]byte(request.Reason))
	externalIdentity := authn.OIDCIdentity{Issuer: request.Issuer, Subject: request.Subject}
	switch request.Operation {
	case "register":
		record := persistence.OIDCIdentityRegistration{
			IsolationDomainID:         request.IsolationDomainID,
			Identity:                  externalIdentity,
			PrincipalID:               request.PrincipalID,
			PrincipalKind:             request.PrincipalKind,
			RegisteredBy:              request.ActorID,
			RegistrationCorrelationID: request.CorrelationID,
			ReasonDigest:              reasonDigest[:],
		}
		if !record.Valid() {
			return errors.New("OIDC identity registration is invalid")
		}
	case "revoke":
		if request.PrincipalKind != "" {
			return errors.New("OIDC identity revocation must not include principalKind")
		}
		record := persistence.OIDCIdentityRevocation{
			IsolationDomainID:       request.IsolationDomainID,
			Identity:                externalIdentity,
			PrincipalID:             request.PrincipalID,
			RevokedBy:               request.ActorID,
			RevocationCorrelationID: request.CorrelationID,
			ReasonDigest:            reasonDigest[:],
		}
		if !record.Valid() {
			return errors.New("OIDC identity revocation is invalid")
		}
	default:
		return errors.New("OIDC identity operation is invalid")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode trailing OIDC identity request data: %w", err)
	}
	return errors.New("OIDC identity request contains trailing data")
}

func validReason(reason string) bool {
	if reason == "" || len(reason) > 2048 || !utf8.ValidString(reason) || strings.TrimSpace(reason) != reason {
		return false
	}
	for _, character := range reason {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
