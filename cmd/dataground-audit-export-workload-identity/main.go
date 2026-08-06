package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type workloadIdentityRepository interface {
	ChangeAuditExportWorkloadIdentity(context.Context, persistence.AuditExportWorkloadIdentityChange) error
}

type commandRequest struct {
	operation               string
	isolationDomainID       string
	workloadID              string
	generation              int64
	clientCertificateSHA256 string
	grantSHA256             string
	grantFile               string
	issuerTrustFile         string
	actorID                 string
	reason                  string
	correlationID           string
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "DataGround audit export workload identity failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	request, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("audit export workload identity context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var grant auditseal.VerifiedWorkloadIdentityGrant
	if request.operation == "activate" {
		grant, err = auditseal.VerifyWorkloadIdentityGrantFile(
			request.grantFile, request.issuerTrustFile, request.isolationDomainID,
			request.workloadID, request.clientCertificateSHA256, time.Now().UTC(),
		)
		if err != nil {
			return err
		}
	}
	change := newWorkloadIdentityChange(request, grant)
	if !change.Valid() {
		return errors.New("audit export workload identity change is invalid")
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
	return executeRequest(operationCtx, persistence.NewRepository(pool), change)
}

func executeRequest(
	ctx context.Context,
	repository workloadIdentityRepository,
	change persistence.AuditExportWorkloadIdentityChange,
) error {
	if repository == nil {
		return errors.New("audit export workload identity repository is required")
	}
	return repository.ChangeAuditExportWorkloadIdentity(ctx, change)
}

func newWorkloadIdentityChange(
	request commandRequest,
	grant auditseal.VerifiedWorkloadIdentityGrant,
) persistence.AuditExportWorkloadIdentityChange {
	reasonDigest := sha256.Sum256([]byte(request.reason))
	change := persistence.AuditExportWorkloadIdentityChange{
		Contract:  persistence.AuditExportWorkloadIdentityAuthorizationContract,
		Operation: request.operation, IsolationDomainID: request.isolationDomainID,
		WorkloadID: request.workloadID, Generation: request.generation,
		GrantSHA256: request.grantSHA256, ClientCertificateSHA256: request.clientCertificateSHA256,
		ActorID: request.actorID, ReasonDigest: reasonDigest[:], CorrelationID: request.correlationID,
	}
	if request.operation == "activate" {
		change.GrantContract = grant.Contract
		change.GrantSHA256 = grant.SHA256
		change.Audience = grant.Audience
		change.ClientCertificateSHA256 = grant.ClientCertificateSHA256
		change.AuthorityID = grant.AuthorityID
		change.IssuerTrustProfileSHA256 = grant.IssuerTrustProfileSHA256
		change.IssuerSigningKeyID = grant.IssuerSigningKeyID
		change.IssuedAt = grant.IssuedAt
		change.NotBefore = grant.NotBefore
		change.ExpiresAt = grant.ExpiresAt
	}
	return change
}

func parseArguments(arguments []string) (commandRequest, error) {
	var request commandRequest
	flags := flag.NewFlagSet("dataground-audit-export-workload-identity", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&request.operation, "operation", "", "activate or revoke")
	flags.StringVar(&request.isolationDomainID, "isolation-domain", "", "exact isolation domain identifier")
	flags.StringVar(&request.workloadID, "workload", "", "deployment-owned workload identifier")
	flags.Int64Var(&request.generation, "generation", 0, "sequential workload identity generation")
	flags.StringVar(&request.clientCertificateSHA256, "client-certificate-sha256", "", "exact client certificate file digest")
	flags.StringVar(&request.grantSHA256, "grant-sha256", "", "exact active grant digest to revoke")
	flags.StringVar(&request.grantFile, "grant-file", "", "signed workload identity grant")
	flags.StringVar(&request.issuerTrustFile, "issuer-trust-file", "", "workload identity issuer trust profile")
	flags.StringVar(&request.actorID, "actor", "", "authorized operator identifier")
	flags.StringVar(&request.reason, "reason", "", "operator-visible identity change reason")
	flags.StringVar(&request.correlationID, "correlation-id", "", "stable operation correlation identifier")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 ||
		request.isolationDomainID == "" || request.workloadID == "" || request.generation < 1 ||
		request.clientCertificateSHA256 == "" || request.actorID == "" || request.reason == "" ||
		request.correlationID == "" {
		return commandRequest{}, errors.New("audit export workload identity arguments are incomplete")
	}
	if request.operation != "activate" && request.operation != "revoke" {
		return commandRequest{}, errors.New("audit export workload identity operation is invalid")
	}
	if !validDigest(request.clientCertificateSHA256) || !validReason(request.reason) ||
		(request.operation == "activate" &&
			(request.grantFile == "" || request.issuerTrustFile == "" || request.grantSHA256 != "")) ||
		(request.operation == "revoke" &&
			(request.grantFile != "" || request.issuerTrustFile != "" || !validDigest(request.grantSHA256))) ||
		(request.grantFile != "" && request.grantFile == request.issuerTrustFile) {
		return commandRequest{}, errors.New("audit export workload identity evidence is invalid")
	}
	return request, nil
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	encoded := strings.TrimPrefix(value, "sha256:")
	decoded, err := hex.DecodeString(encoded)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == encoded
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
