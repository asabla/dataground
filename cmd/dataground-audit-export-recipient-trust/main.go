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

type auditExportRecipientTrustRepository interface {
	ChangeAuditExportRecipientTrust(context.Context, persistence.AuditExportRecipientTrustChange) error
}

type commandRequest struct {
	operation         string
	isolationDomainID string
	recipientID       string
	generation        int64
	trustFile         string
	trustSHA256       string
	actorID           string
	reason            string
	correlationID     string
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "DataGround audit export recipient trust failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	request, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("audit export recipient trust context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	profile := auditseal.RecipientTrustEvidence{
		Contract:    "dataground.audit-export-recipient-trust/ed25519/v1",
		RecipientID: request.recipientID,
		SHA256:      request.trustSHA256,
	}
	if request.operation == "activate" {
		profile, err = auditseal.InspectRecipientTrustProfileFile(request.trustFile)
		if err != nil {
			return err
		}
		if profile.RecipientID != request.recipientID {
			return errors.New("audit export recipient trust scope is invalid")
		}
	}
	change := newTrustChange(request, profile)
	if !change.Valid() {
		return errors.New("audit export recipient trust change is invalid")
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
	repository auditExportRecipientTrustRepository,
	change persistence.AuditExportRecipientTrustChange,
) error {
	if repository == nil {
		return errors.New("audit export recipient trust repository is required")
	}
	return repository.ChangeAuditExportRecipientTrust(ctx, change)
}

func newTrustChange(
	request commandRequest,
	profile auditseal.RecipientTrustEvidence,
) persistence.AuditExportRecipientTrustChange {
	reasonDigest := sha256.Sum256([]byte(request.reason))
	return persistence.AuditExportRecipientTrustChange{
		Contract:           persistence.AuditExportRecipientTrustAuthorizationContract,
		Operation:          request.operation,
		IsolationDomainID:  request.isolationDomainID,
		RecipientID:        request.recipientID,
		Generation:         request.generation,
		TrustContract:      profile.Contract,
		TrustProfileSHA256: profile.SHA256,
		KeyIDs:             append([]string(nil), profile.KeyIDs...),
		ActorID:            request.actorID,
		ReasonDigest:       reasonDigest[:],
		CorrelationID:      request.correlationID,
	}
}

func parseArguments(arguments []string) (commandRequest, error) {
	var request commandRequest
	flags := flag.NewFlagSet("dataground-audit-export-recipient-trust", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&request.operation, "operation", "", "activate or revoke")
	flags.StringVar(&request.isolationDomainID, "isolation-domain", "", "exact isolation domain identifier")
	flags.StringVar(&request.recipientID, "recipient", "", "deployment-owned recipient identifier")
	flags.Int64Var(&request.generation, "generation", 0, "sequential trust generation")
	flags.StringVar(&request.trustFile, "trust-file", "", "canonical recipient trust profile")
	flags.StringVar(&request.trustSHA256, "trust-sha256", "", "exact active trust profile digest to revoke")
	flags.StringVar(&request.actorID, "actor", "", "authorized operator identifier")
	flags.StringVar(&request.reason, "reason", "", "operator-visible trust change reason")
	flags.StringVar(&request.correlationID, "correlation-id", "", "stable operation correlation identifier")
	if err := flags.Parse(arguments); err != nil {
		return commandRequest{}, errors.New("audit export recipient trust arguments are invalid")
	}
	if flags.NArg() != 0 || request.isolationDomainID == "" || request.recipientID == "" ||
		request.generation < 1 || request.actorID == "" ||
		request.reason == "" || request.correlationID == "" {
		return commandRequest{}, errors.New("audit export recipient trust arguments are incomplete")
	}
	if request.operation != "activate" && request.operation != "revoke" {
		return commandRequest{}, errors.New("audit export recipient trust operation is invalid")
	}
	if (request.operation == "activate" && (request.trustFile == "" || request.trustSHA256 != "")) ||
		(request.operation == "revoke" && (request.trustFile != "" || !validTrustDigest(request.trustSHA256))) {
		return commandRequest{}, errors.New("audit export recipient trust evidence is invalid")
	}
	if !validReason(request.reason) {
		return commandRequest{}, errors.New("audit export recipient trust reason is invalid")
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
