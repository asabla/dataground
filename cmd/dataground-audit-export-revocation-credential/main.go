package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/auditseal"
	"github.com/asabla/dataground/internal/persistence"
)

type auditExportRevocationCredentialRepository interface {
	ChangeAuditExportRevocationCredential(
		context.Context,
		persistence.AuditExportRevocationCredentialChange,
	) error
}

type commandRequest struct {
	operation            string
	isolationDomainID    string
	purpose              string
	sourceID             string
	sourceRegistrySHA256 string
	endpoint             string
	generation           int64
	credentialFile       string
	credentialSHA256     string
	actorID              string
	reason               string
	correlationID        string
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "DataGround audit export revocation credential change failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	request, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("audit export revocation credential context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATAGROUND_DATABASE_URL is required")
	}
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
	change, err := newCredentialChange(request, time.Now().UTC())
	if err != nil {
		return err
	}
	return executeRequest(operationCtx, persistence.NewRepository(pool), change)
}

func newCredentialChange(
	request commandRequest,
	now time.Time,
) (persistence.AuditExportRevocationCredentialChange, error) {
	change := persistence.AuditExportRevocationCredentialChange{
		Contract: persistence.AuditExportRevocationCredentialAuthorizationContract,
		Operation: request.operation, IsolationDomainID: request.isolationDomainID,
		Purpose: request.purpose, SourceID: request.sourceID,
		SourceRegistrySHA256: request.sourceRegistrySHA256,
		Endpoint: request.endpoint, Generation: request.generation,
		CredentialSHA256: request.credentialSHA256,
		ActorID: request.actorID, CorrelationID: request.correlationID,
	}
	reasonDigest := sha256.Sum256([]byte(request.reason))
	change.ReasonDigest = reasonDigest[:]
	if request.operation == "activate" {
		evidence, err := auditseal.InspectRevocationSourceCredentialFile(
			request.credentialFile,
			auditseal.RevocationNoticeAcquisitionConfig{
				IsolationDomainID: request.isolationDomainID,
				Purpose: request.purpose, SourceID: request.sourceID,
				SourceRegistrySHA256: request.sourceRegistrySHA256,
			},
			request.endpoint,
			now,
		)
		if err != nil {
			return persistence.AuditExportRevocationCredentialChange{}, err
		}
		change.CredentialSHA256 = evidence.CredentialSHA256
		change.ActivatedAt = evidence.ActivatedAt
		change.ExpiresAt = evidence.ExpiresAt
	}
	if !change.Valid() {
		return persistence.AuditExportRevocationCredentialChange{},
			errors.New("audit export revocation credential change is invalid")
	}
	return change, nil
}

func executeRequest(
	ctx context.Context,
	repository auditExportRevocationCredentialRepository,
	change persistence.AuditExportRevocationCredentialChange,
) error {
	if repository == nil {
		return errors.New("audit export revocation credential repository is required")
	}
	return repository.ChangeAuditExportRevocationCredential(ctx, change)
}

func parseArguments(arguments []string) (commandRequest, error) {
	var request commandRequest
	flags := flag.NewFlagSet("dataground-audit-export-revocation-credential", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&request.operation, "operation", "", "activate or revoke")
	flags.StringVar(&request.isolationDomainID, "isolation-domain", "", "exact isolation domain identifier")
	flags.StringVar(&request.purpose, "purpose", "", "recipient-proof or workload-identity")
	flags.StringVar(&request.sourceID, "source", "", "activated revocation source identifier")
	flags.StringVar(&request.sourceRegistrySHA256, "source-registry-sha256", "", "exact source registry digest")
	flags.StringVar(&request.endpoint, "endpoint", "", "notice or trust")
	flags.Int64Var(&request.generation, "generation", 0, "sequential endpoint credential generation")
	flags.StringVar(&request.credentialFile, "credential-file", "", "owner-only canonical endpoint credential")
	flags.StringVar(&request.credentialSHA256, "credential-sha256", "", "exact active credential digest to revoke")
	flags.StringVar(&request.actorID, "actor", "", "authorized operator identifier")
	flags.StringVar(&request.reason, "reason", "", "operator-visible credential change reason")
	flags.StringVar(&request.correlationID, "correlation-id", "", "stable operation correlation identifier")
	if err := flags.Parse(arguments); err != nil {
		return commandRequest{}, errors.New("audit export revocation credential arguments are invalid")
	}
	if flags.NArg() != 0 || request.isolationDomainID == "" || request.purpose == "" ||
		request.sourceID == "" || request.sourceRegistrySHA256 == "" ||
		request.endpoint == "" || request.generation <= 0 || request.actorID == "" ||
		request.reason == "" || request.correlationID == "" {
		return commandRequest{}, errors.New("audit export revocation credential arguments are incomplete")
	}
	if request.operation != "activate" && request.operation != "revoke" {
		return commandRequest{}, errors.New("audit export revocation credential operation is invalid")
	}
	if request.purpose != auditseal.RevocationNoticePurposeRecipientProof &&
		request.purpose != auditseal.RevocationNoticePurposeWorkloadIdentity {
		return commandRequest{}, errors.New("audit export revocation credential purpose is invalid")
	}
	if request.endpoint != "notice" && request.endpoint != "trust" {
		return commandRequest{}, errors.New("audit export revocation credential endpoint is invalid")
	}
	if (request.operation == "activate" &&
		(request.credentialFile == "" || request.credentialSHA256 != "")) ||
		(request.operation == "revoke" &&
			(request.credentialFile != "" || !validDigest(request.credentialSHA256))) {
		return commandRequest{}, errors.New("audit export revocation credential evidence is invalid")
	}
	if !validDigest(request.sourceRegistrySHA256) || !validReason(request.reason) {
		return commandRequest{}, errors.New("audit export revocation credential attribution is invalid")
	}
	return request, nil
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validReason(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
