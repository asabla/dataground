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

type auditExportRevocationAuthorityRepository interface {
	ChangeAuditExportRevocationAuthority(
		context.Context,
		persistence.AuditExportRevocationAuthorityChange,
	) error
}

type commandRequest struct {
	operation         string
	isolationDomainID string
	purpose           string
	authorityID       string
	generation        int64
	trustFile         string
	trustSHA256       string
	actorID           string
	reason            string
	correlationID     string
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "DataGround audit export revocation authority failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	request, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("audit export revocation authority context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	evidence, err := authorityEvidence(request)
	if err != nil {
		return err
	}
	change := newAuthorityChange(request, evidence)
	if !change.Valid() {
		return errors.New("audit export revocation authority change is invalid")
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

func authorityEvidence(request commandRequest) (auditseal.RevocationAuthorityTrustEvidence, error) {
	if request.operation == "activate" {
		evidence, err := auditseal.InspectRevocationAuthorityTrustFile(request.purpose, request.trustFile)
		if err != nil {
			return auditseal.RevocationAuthorityTrustEvidence{}, err
		}
		if evidence.AuthorityID != request.authorityID {
			return auditseal.RevocationAuthorityTrustEvidence{},
				errors.New("audit export revocation authority profile does not match")
		}
		return evidence, nil
	}
	contract := auditseal.RecipientRevocationTrustContract
	if request.purpose == auditseal.RevocationAuthorityPurposeWorkloadIdentity {
		contract = auditseal.WorkloadIdentityRevocationTrustContract
	}
	return auditseal.RevocationAuthorityTrustEvidence{
		Purpose: request.purpose, Contract: contract, SHA256: request.trustSHA256,
		AuthorityID: request.authorityID,
	}, nil
}

func executeRequest(
	ctx context.Context,
	repository auditExportRevocationAuthorityRepository,
	change persistence.AuditExportRevocationAuthorityChange,
) error {
	if repository == nil {
		return errors.New("audit export revocation authority repository is required")
	}
	return repository.ChangeAuditExportRevocationAuthority(ctx, change)
}

func newAuthorityChange(
	request commandRequest,
	evidence auditseal.RevocationAuthorityTrustEvidence,
) persistence.AuditExportRevocationAuthorityChange {
	reasonDigest := sha256.Sum256([]byte(request.reason))
	return persistence.AuditExportRevocationAuthorityChange{
		Contract:  persistence.AuditExportRevocationAuthorityAuthorizationContract,
		Operation: request.operation, IsolationDomainID: request.isolationDomainID,
		Purpose: evidence.Purpose, AuthorityID: evidence.AuthorityID,
		Generation: request.generation, TrustContract: evidence.Contract,
		TrustProfileSHA256: evidence.SHA256,
		KeyIDs:             append([]string(nil), evidence.KeyIDs...), ActorID: request.actorID,
		ReasonDigest: reasonDigest[:], CorrelationID: request.correlationID,
	}
}

func parseArguments(arguments []string) (commandRequest, error) {
	var request commandRequest
	flags := flag.NewFlagSet("dataground-audit-export-revocation-authority", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&request.operation, "operation", "", "activate or revoke")
	flags.StringVar(&request.isolationDomainID, "isolation-domain", "", "exact isolation domain identifier")
	flags.StringVar(&request.purpose, "purpose", "", "recipient-proof or workload-identity")
	flags.StringVar(&request.authorityID, "authority", "", "deployment-owned revocation authority identifier")
	flags.Int64Var(&request.generation, "generation", 0, "sequential authority generation")
	flags.StringVar(&request.trustFile, "trust-file", "", "canonical revocation authority trust profile")
	flags.StringVar(&request.trustSHA256, "trust-sha256", "", "exact active trust profile digest to revoke")
	flags.StringVar(&request.actorID, "actor", "", "authorized operator identifier")
	flags.StringVar(&request.reason, "reason", "", "operator-visible authority change reason")
	flags.StringVar(&request.correlationID, "correlation-id", "", "stable operation correlation identifier")
	if err := flags.Parse(arguments); err != nil {
		return commandRequest{}, errors.New("audit export revocation authority arguments are invalid")
	}
	if flags.NArg() != 0 || request.isolationDomainID == "" || request.authorityID == "" ||
		request.generation < 1 || request.actorID == "" || request.reason == "" ||
		request.correlationID == "" {
		return commandRequest{}, errors.New("audit export revocation authority arguments are incomplete")
	}
	if request.operation != "activate" && request.operation != "revoke" {
		return commandRequest{}, errors.New("audit export revocation authority operation is invalid")
	}
	if request.purpose != auditseal.RevocationAuthorityPurposeRecipientProof &&
		request.purpose != auditseal.RevocationAuthorityPurposeWorkloadIdentity {
		return commandRequest{}, errors.New("audit export revocation authority purpose is invalid")
	}
	if (request.operation == "activate" && (request.trustFile == "" || request.trustSHA256 != "")) ||
		(request.operation == "revoke" && (request.trustFile != "" || !validTrustDigest(request.trustSHA256))) {
		return commandRequest{}, errors.New("audit export revocation authority evidence is invalid")
	}
	if !validReason(request.reason) {
		return commandRequest{}, errors.New("audit export revocation authority reason is invalid")
	}
	return request, nil
}

func validTrustDigest(value string) bool {
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
