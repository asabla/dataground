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

type auditExportRevocationSourceRepository interface {
	ChangeAuditExportRevocationSource(context.Context, persistence.AuditExportRevocationSourceChange) error
}

type commandRequest struct {
	operation            string
	isolationDomainID    string
	purpose              string
	sourceID             string
	generation           int64
	sourceRegistryFile   string
	sourceRegistrySHA256 string
	actorID              string
	reason               string
	correlationID        string
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "DataGround audit export revocation source failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	request, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("audit export revocation source context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	evidence, err := sourceEvidence(request)
	if err != nil {
		return err
	}
	change := newSourceChange(request, evidence)
	if !change.Valid() {
		return errors.New("audit export revocation source change is invalid")
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

func sourceEvidence(request commandRequest) (auditseal.RevocationSourceEvidence, error) {
	if request.operation == "activate" {
		return auditseal.InspectRevocationSourceRegistryFile(
			request.sourceRegistryFile, request.purpose, request.sourceID,
		)
	}
	return auditseal.RevocationSourceEvidence{
		Purpose: request.purpose, SourceID: request.sourceID,
		SourceRegistrySHA256: request.sourceRegistrySHA256,
	}, nil
}

func executeRequest(
	ctx context.Context,
	repository auditExportRevocationSourceRepository,
	change persistence.AuditExportRevocationSourceChange,
) error {
	if repository == nil {
		return errors.New("audit export revocation source repository is required")
	}
	return repository.ChangeAuditExportRevocationSource(ctx, change)
}

func newSourceChange(
	request commandRequest,
	evidence auditseal.RevocationSourceEvidence,
) persistence.AuditExportRevocationSourceChange {
	reasonDigest := sha256.Sum256([]byte(request.reason))
	return persistence.AuditExportRevocationSourceChange{
		Contract:  persistence.AuditExportRevocationSourceAuthorizationContract,
		Operation: request.operation, IsolationDomainID: request.isolationDomainID,
		Purpose: evidence.Purpose, SourceID: evidence.SourceID, Generation: request.generation,
		SourceRegistrySHA256: evidence.SourceRegistrySHA256, ActorID: request.actorID,
		ReasonDigest: reasonDigest[:], CorrelationID: request.correlationID,
	}
}

func parseArguments(arguments []string) (commandRequest, error) {
	var request commandRequest
	flags := flag.NewFlagSet("dataground-audit-export-revocation-source", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&request.operation, "operation", "", "activate or revoke")
	flags.StringVar(&request.isolationDomainID, "isolation-domain", "", "exact isolation domain identifier")
	flags.StringVar(&request.purpose, "purpose", "", "recipient-proof or workload-identity")
	flags.StringVar(&request.sourceID, "source", "", "deployment-owned revocation source identifier")
	flags.Int64Var(&request.generation, "generation", 0, "sequential source generation")
	flags.StringVar(&request.sourceRegistryFile, "source-registry-file", "", "canonical source registry to activate")
	flags.StringVar(&request.sourceRegistrySHA256, "source-registry-sha256", "", "exact active registry digest to revoke")
	flags.StringVar(&request.actorID, "actor", "", "authorized operator identifier")
	flags.StringVar(&request.reason, "reason", "", "operator-visible source change reason")
	flags.StringVar(&request.correlationID, "correlation-id", "", "stable operation correlation identifier")
	if err := flags.Parse(arguments); err != nil {
		return commandRequest{}, errors.New("audit export revocation source arguments are invalid")
	}
	if flags.NArg() != 0 || request.isolationDomainID == "" || request.sourceID == "" ||
		request.generation < 1 || request.actorID == "" || request.reason == "" ||
		request.correlationID == "" {
		return commandRequest{}, errors.New("audit export revocation source arguments are incomplete")
	}
	if request.operation != "activate" && request.operation != "revoke" {
		return commandRequest{}, errors.New("audit export revocation source operation is invalid")
	}
	if request.purpose != auditseal.RevocationNoticePurposeRecipientProof &&
		request.purpose != auditseal.RevocationNoticePurposeWorkloadIdentity {
		return commandRequest{}, errors.New("audit export revocation source purpose is invalid")
	}
	if (request.operation == "activate" &&
		(request.sourceRegistryFile == "" || request.sourceRegistrySHA256 != "")) ||
		(request.operation == "revoke" &&
			(request.sourceRegistryFile != "" || !validDigest(request.sourceRegistrySHA256))) {
		return commandRequest{}, errors.New("audit export revocation source evidence is invalid")
	}
	if !validReason(request.reason) {
		return commandRequest{}, errors.New("audit export revocation source reason is invalid")
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
