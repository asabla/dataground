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

type auditExportDeliveryRepository interface {
	PrepareAuditExportDelivery(
		context.Context,
		persistence.AuditExportDelivery,
		persistence.AuditExportDeliveryAttribution,
	) error
	AcknowledgeAuditExportDelivery(
		context.Context,
		persistence.AuditExportDelivery,
		persistence.AuditExportDeliveryAcknowledgement,
	) error
}

type commandRequest struct {
	operation          string
	deliveryContract   string
	deliveryID         string
	isolationDomainID  string
	envelopeFile       string
	encryptedFile      string
	trustFile          string
	recipientID        string
	destinationDigest  []byte
	receiptFile        string
	recipientTrustFile string
	acknowledgement    persistence.AuditExportDeliveryAcknowledgement
	actorID            string
	reason             string
	correlationID      string
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "DataGround audit export delivery failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	request, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("audit export delivery context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	evidence, err := auditseal.VerifyEvidenceFile(request.envelopeFile, request.trustFile)
	if err != nil {
		return err
	}
	delivery := persistence.AuditExportDelivery{
		Contract:           persistence.AuditExportDeliveryContract,
		DeliveryID:         request.deliveryID,
		IsolationDomainID:  request.isolationDomainID,
		ExportKind:         evidence.ExportKind,
		ExportID:           evidence.ExportID,
		EnvelopeDigest:     evidence.EnvelopeSHA256[:],
		ExportSHA256:       evidence.ExportSHA256,
		TrustProfileSHA256: evidence.TrustProfileSHA256,
		SigningKeyID:       evidence.SigningKeyID,
		RecipientID:        request.recipientID,
		DestinationDigest:  request.destinationDigest,
	}
	if request.encryptedFile != "" {
		encrypted, err := auditseal.VerifyEncryptedPackageFile(
			request.encryptedFile,
			request.envelopeFile,
			request.trustFile,
			request.recipientTrustFile,
		)
		if err != nil {
			return err
		}
		delivery.Contract = request.deliveryContract
		delivery.EncryptedPackageDigest = encrypted.PackageSHA256[:]
		delivery.RecipientTrustProfileSHA256 = encrypted.RecipientTrustProfileSHA256
		delivery.RecipientEncryptionKeyID = encrypted.EncryptionKeyID
	}
	if !delivery.Valid() || delivery.IsolationDomainID != evidence.IsolationDomainID {
		return errors.New("audit export delivery scope is invalid")
	}
	if request.operation == "acknowledge" {
		receipt, err := auditseal.VerifyDeliveryReceiptFile(request.receiptFile, request.recipientTrustFile, delivery)
		if err != nil {
			return err
		}
		request.acknowledgement = persistence.AuditExportDeliveryAcknowledgement{
			AcknowledgementDigest:       receipt.ReceiptSHA256[:],
			DeliveryContract:            receipt.DeliveryContract,
			ReceiptContract:             receipt.Contract,
			RecipientTrustProfileSHA256: receipt.RecipientTrustProfileSHA256,
			RecipientSigningKeyID:       receipt.SigningKeyID,
			AcceptedAt:                  receipt.AcceptedAt,
			RecipientTrustGeneration:    receipt.RecipientTrustGeneration,
		}
	}
	reasonDigest := sha256.Sum256([]byte(request.reason))
	if !(persistence.AuditExportDeliveryAttribution{
		ActorID: request.actorID, ReasonDigest: reasonDigest[:], CorrelationID: request.correlationID,
	}).Valid() {
		return errors.New("audit export delivery attribution is invalid")
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
	return executeRequest(operationCtx, persistence.NewRepository(pool), delivery, request)
}

func executeRequest(
	ctx context.Context,
	repository auditExportDeliveryRepository,
	delivery persistence.AuditExportDelivery,
	request commandRequest,
) error {
	if repository == nil {
		return errors.New("audit export delivery repository is required")
	}
	reasonDigest := sha256.Sum256([]byte(request.reason))
	attribution := persistence.AuditExportDeliveryAttribution{
		ActorID: request.actorID, ReasonDigest: reasonDigest[:], CorrelationID: request.correlationID,
	}
	switch request.operation {
	case "prepare":
		return repository.PrepareAuditExportDelivery(ctx, delivery, attribution)
	case "acknowledge":
		acknowledgement := request.acknowledgement
		acknowledgement.Attribution = attribution
		return repository.AcknowledgeAuditExportDelivery(
			ctx,
			delivery,
			acknowledgement,
		)
	default:
		return errors.New("audit export delivery operation is invalid")
	}
}

func parseArguments(arguments []string) (commandRequest, error) {
	var request commandRequest
	flags := flag.NewFlagSet("dataground-audit-export-delivery", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var destinationSHA256 string
	flags.StringVar(&request.operation, "operation", "", "prepare or acknowledge")
	flags.StringVar(
		&request.deliveryContract,
		"delivery-contract",
		persistence.AuditExportWorkloadDeliveryContract,
		"versioned encrypted delivery contract",
	)
	flags.StringVar(&request.deliveryID, "delivery-id", "", "stable audit export delivery identifier")
	flags.StringVar(&request.isolationDomainID, "isolation-domain", "", "exact isolation domain identifier")
	flags.StringVar(&request.envelopeFile, "envelope-file", "", "canonical signed audit export envelope")
	flags.StringVar(&request.encryptedFile, "encrypted-file", "", "recipient-encrypted audit export package")
	flags.StringVar(&request.trustFile, "trust-file", "", "pinned audit export trust profile")
	flags.StringVar(&request.recipientID, "recipient", "", "deployment-owned recipient identifier")
	flags.StringVar(&destinationSHA256, "destination-sha256", "", "digest of the canonical destination binding")
	flags.StringVar(&request.receiptFile, "receipt-file", "", "canonical signed recipient acknowledgement receipt")
	flags.StringVar(&request.recipientTrustFile, "recipient-trust-file", "", "pinned recipient acknowledgement trust profile")
	flags.StringVar(&request.actorID, "actor", "", "authorized operator identifier")
	flags.StringVar(&request.reason, "reason", "", "operator-visible delivery reason")
	flags.StringVar(&request.correlationID, "correlation-id", "", "stable operation correlation identifier")
	if err := flags.Parse(arguments); err != nil {
		return commandRequest{}, errors.New("audit export delivery arguments are invalid")
	}
	if flags.NArg() != 0 || request.deliveryID == "" || request.isolationDomainID == "" ||
		request.envelopeFile == "" || request.trustFile == "" || request.recipientID == "" ||
		destinationSHA256 == "" || request.actorID == "" || request.reason == "" || request.correlationID == "" {
		return commandRequest{}, errors.New("audit export delivery arguments are incomplete")
	}
	if !validReason(request.reason) {
		return commandRequest{}, errors.New("audit export delivery reason is invalid")
	}
	request.destinationDigest = parseDigest(destinationSHA256)
	if len(request.destinationDigest) != sha256.Size {
		return commandRequest{}, errors.New("audit export delivery destination digest is invalid")
	}
	switch request.operation {
	case "prepare":
		if request.deliveryContract != persistence.AuditExportWorkloadDeliveryContract ||
			request.receiptFile != "" || request.encryptedFile == "" || request.recipientTrustFile == "" {
			return commandRequest{}, errors.New("audit export delivery preparation arguments are invalid")
		}
	case "acknowledge":
		if (request.deliveryContract != persistence.AuditExportEncryptedDeliveryContract &&
			request.deliveryContract != persistence.AuditExportTransportedDeliveryContract &&
			request.deliveryContract != persistence.AuditExportWorkloadDeliveryContract) ||
			request.encryptedFile == "" || request.receiptFile == "" || request.recipientTrustFile == "" ||
			request.receiptFile == request.recipientTrustFile || request.receiptFile == request.envelopeFile ||
			request.receiptFile == request.trustFile || request.recipientTrustFile == request.envelopeFile ||
			request.recipientTrustFile == request.trustFile {
			return commandRequest{}, errors.New("audit export delivery acknowledgement files are invalid")
		}
	default:
		return commandRequest{}, errors.New("audit export delivery operation is invalid")
	}
	paths := []string{request.envelopeFile, request.trustFile, request.recipientTrustFile}
	if request.encryptedFile != "" {
		paths = append(paths, request.encryptedFile)
	}
	if request.receiptFile != "" {
		paths = append(paths, request.receiptFile)
	}
	for left := range paths {
		for right := left + 1; right < len(paths); right++ {
			if paths[left] == paths[right] {
				return commandRequest{}, errors.New("audit export delivery evidence files are invalid")
			}
		}
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

func parseDigest(value string) []byte {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return nil
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	if err != nil || len(decoded) != sha256.Size ||
		hex.EncodeToString(decoded) != strings.TrimPrefix(value, "sha256:") {
		return nil
	}
	return decoded
}
