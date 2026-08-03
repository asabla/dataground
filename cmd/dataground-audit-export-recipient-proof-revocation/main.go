package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/auditseal"
	"github.com/asabla/dataground/internal/persistence"
)

type auditExportRecipientProofRevocationRepository interface {
	RecordAuditExportRecipientProofRevocation(
		context.Context,
		persistence.AuditExportRecipientProofRevocationRecord,
	) error
}

type commandRequest struct {
	isolationDomainID   string
	revocationFile      string
	revocationTrustFile string
	actorID             string
	reason              string
	correlationID       string
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "DataGround audit export recipient proof revocation failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	request, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("audit export recipient proof revocation context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	verified, err := auditseal.VerifyRecipientProofRevocationFile(
		request.revocationFile, request.revocationTrustFile,
		request.isolationDomainID, time.Now().UTC(),
	)
	if err != nil {
		return err
	}
	record := newRevocationRecord(request, verified)
	if !record.Valid() {
		return errors.New("audit export recipient proof revocation record is invalid")
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
	return executeRequest(operationCtx, persistence.NewRepository(pool), record)
}

func executeRequest(
	ctx context.Context,
	repository auditExportRecipientProofRevocationRepository,
	record persistence.AuditExportRecipientProofRevocationRecord,
) error {
	if repository == nil {
		return errors.New("audit export recipient proof revocation repository is required")
	}
	return repository.RecordAuditExportRecipientProofRevocation(ctx, record)
}

func newRevocationRecord(
	request commandRequest,
	verified auditseal.VerifiedRecipientProofRevocation,
) persistence.AuditExportRecipientProofRevocationRecord {
	reasonDigest := sha256.Sum256([]byte(request.reason))
	return persistence.AuditExportRecipientProofRevocationRecord{
		Contract:           persistence.AuditExportRecipientProofRevocationRecordContract,
		RevocationContract: verified.Contract, RevocationSHA256: verified.SHA256,
		IsolationDomainID: verified.IsolationDomainID, Scope: verified.Scope,
		ProofingAuthorityID:          verified.ProofingAuthorityID,
		ProofingTrustProfileSHA256:   verified.ProofingTrustProfileSHA256,
		ProofingSigningKeyID:         verified.ProofingSigningKeyID,
		ExternalReasonSHA256:         verified.ReasonSHA256,
		RevocationAuthorityID:        verified.RevocationAuthorityID,
		RevocationTrustProfileSHA256: verified.RevocationTrustProfileSHA256,
		RevocationSigningKeyID:       verified.RevocationSigningKeyID,
		IssuedAt:                     verified.IssuedAt, EffectiveAt: verified.EffectiveAt,
		ActorID: request.actorID, ReasonDigest: reasonDigest[:], CorrelationID: request.correlationID,
	}
}

func parseArguments(arguments []string) (commandRequest, error) {
	var request commandRequest
	flags := flag.NewFlagSet("dataground-audit-export-recipient-proof-revocation", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&request.isolationDomainID, "isolation-domain", "", "exact isolation domain identifier")
	flags.StringVar(&request.revocationFile, "revocation-file", "", "signed recipient proof revocation")
	flags.StringVar(&request.revocationTrustFile, "revocation-trust-file", "", "recipient revocation authority trust profile")
	flags.StringVar(&request.actorID, "actor", "", "authorized operator identifier")
	flags.StringVar(&request.reason, "reason", "", "operator-visible revocation intake reason")
	flags.StringVar(&request.correlationID, "correlation-id", "", "stable operation correlation identifier")
	if err := flags.Parse(arguments); err != nil {
		return commandRequest{}, errors.New("audit export recipient proof revocation arguments are invalid")
	}
	if flags.NArg() != 0 || request.isolationDomainID == "" || request.revocationFile == "" ||
		request.revocationTrustFile == "" || request.actorID == "" || request.reason == "" ||
		request.correlationID == "" {
		return commandRequest{}, errors.New("audit export recipient proof revocation arguments are incomplete")
	}
	if request.revocationFile == request.revocationTrustFile || !validReason(request.reason) {
		return commandRequest{}, errors.New("audit export recipient proof revocation evidence is invalid")
	}
	return request, nil
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
