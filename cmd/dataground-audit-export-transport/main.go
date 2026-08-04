package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/auditseal"
	"github.com/asabla/dataground/internal/audittransport"
	"github.com/asabla/dataground/internal/execution/s3store"
	"github.com/asabla/dataground/internal/persistence"
)

type transportRepository interface {
	ReserveAuditExportDeliveryTransport(
		context.Context,
		persistence.AuditExportDelivery,
		persistence.AuditExportDeliveryAttribution,
	) error
	CompleteAuditExportDeliveryTransport(
		context.Context,
		persistence.AuditExportDelivery,
		persistence.AuditExportDeliveryAttribution,
	) error
}

type commandRequest struct {
	deliveryID         string
	isolationDomainID  string
	envelopeFile       string
	encryptedFile      string
	trustFile          string
	recipientTrustFile string
	recipientID        string
	destinationFile    string
	destinationDigest  []byte
	actorID            string
	reason             string
	correlationID      string
	allowLoopbackHTTP  bool
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "DataGround audit export transport failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	request, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("audit export transport context is required")
	}
	evidence, err := auditseal.VerifyEvidenceFile(request.envelopeFile, request.trustFile)
	if err != nil {
		return err
	}
	encrypted, err := auditseal.VerifyEncryptedPackageFile(
		request.encryptedFile,
		request.envelopeFile,
		request.trustFile,
		request.recipientTrustFile,
	)
	if err != nil {
		return err
	}
	delivery := persistence.AuditExportDelivery{
		Contract:   persistence.AuditExportTransportedDeliveryContract,
		DeliveryID: request.deliveryID, IsolationDomainID: request.isolationDomainID,
		ExportKind: evidence.ExportKind, ExportID: evidence.ExportID,
		EnvelopeDigest: evidence.EnvelopeSHA256[:], ExportSHA256: evidence.ExportSHA256,
		TrustProfileSHA256: evidence.TrustProfileSHA256, SigningKeyID: evidence.SigningKeyID,
		RecipientID: request.recipientID, DestinationDigest: request.destinationDigest,
		EncryptedPackageDigest:      encrypted.PackageSHA256[:],
		RecipientTrustProfileSHA256: encrypted.RecipientTrustProfileSHA256,
		RecipientEncryptionKeyID:    encrypted.EncryptionKeyID,
	}
	if !delivery.Valid() || delivery.IsolationDomainID != evidence.IsolationDomainID ||
		delivery.IsolationDomainID != encrypted.IsolationDomainID ||
		delivery.RecipientID != encrypted.RecipientID {
		return errors.New("audit export transport scope is invalid")
	}
	destination, err := auditseal.VerifyDeliveryDestinationFile(request.destinationFile, delivery)
	if err != nil {
		return err
	}
	if !request.allowLoopbackHTTP || !isHTTPStyleLoopback(destination.Endpoint) ||
		destination.AddressingStyle != string(s3store.PathStyle) {
		return errors.New("audit export transport requires an explicit loopback development destination")
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return errors.New("audit export transport HTTP configuration is invalid")
	}
	transport := defaultTransport.Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	store, err := s3store.New(s3store.Config{
		Endpoint: destination.Endpoint, Bucket: destination.Bucket,
		AddressingStyle:      s3store.AddressingStyle(destination.AddressingStyle),
		AllowHTTPForLoopback: true,
		HTTPClient:           &http.Client{Transport: transport, Timeout: 30 * time.Second},
	})
	if err != nil {
		return errors.New("audit export transport destination is invalid")
	}
	objects, err := s3store.NewAuditExportStore(store)
	if err != nil {
		return err
	}
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATAGROUND_DATABASE_URL is required")
	}
	operationCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
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
	reasonDigest := sha256.Sum256([]byte(request.reason))
	attribution := persistence.AuditExportDeliveryAttribution{
		ActorID: request.actorID, ReasonDigest: reasonDigest[:], CorrelationID: request.correlationID,
	}
	return executeTransport(
		operationCtx,
		persistence.NewRepository(pool),
		objects,
		delivery,
		attribution,
		request.encryptedFile,
		destination.ObjectKey,
	)
}

func executeTransport(
	ctx context.Context,
	repository transportRepository,
	objects audittransport.ObjectStore,
	delivery persistence.AuditExportDelivery,
	attribution persistence.AuditExportDeliveryAttribution,
	encryptedFile string,
	objectKey string,
) error {
	if repository == nil || objects == nil || !delivery.Valid() || !attribution.Valid() {
		return errors.New("audit export transport dependencies are invalid")
	}
	if err := repository.ReserveAuditExportDeliveryTransport(ctx, delivery, attribution); err != nil {
		return err
	}
	var digest [sha256.Size]byte
	copy(digest[:], delivery.EncryptedPackageDigest)
	content, err := auditseal.ReadEncryptedPackageFile(encryptedFile, digest)
	if err != nil {
		return err
	}
	defer clear(content)
	if err := audittransport.Execute(ctx, objects, objectKey, content, digest); err != nil {
		return err
	}
	return repository.CompleteAuditExportDeliveryTransport(ctx, delivery, attribution)
}

func parseArguments(arguments []string) (commandRequest, error) {
	var request commandRequest
	flags := flag.NewFlagSet("dataground-audit-export-transport", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var destinationSHA256 string
	flags.StringVar(&request.deliveryID, "delivery-id", "", "stable audit export delivery identifier")
	flags.StringVar(&request.isolationDomainID, "isolation-domain", "", "exact isolation domain identifier")
	flags.StringVar(&request.envelopeFile, "envelope-file", "", "canonical signed audit export envelope")
	flags.StringVar(&request.encryptedFile, "encrypted-file", "", "recipient-encrypted audit export package")
	flags.StringVar(&request.trustFile, "trust-file", "", "pinned audit export trust profile")
	flags.StringVar(&request.recipientTrustFile, "recipient-trust-file", "", "pinned recipient trust profile")
	flags.StringVar(&request.recipientID, "recipient", "", "deployment-owned recipient identifier")
	flags.StringVar(&request.destinationFile, "destination-file", "", "canonical S3 delivery destination")
	flags.StringVar(&destinationSHA256, "destination-sha256", "", "digest of the canonical destination binding")
	flags.StringVar(&request.actorID, "actor", "", "authorized operator identifier")
	flags.StringVar(&request.reason, "reason", "", "operator-visible transport reason")
	flags.StringVar(&request.correlationID, "correlation-id", "", "stable transport correlation identifier")
	flags.BoolVar(&request.allowLoopbackHTTP, "allow-loopback-http", false, "allow explicit plaintext loopback development endpoint")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 ||
		request.deliveryID == "" || request.isolationDomainID == "" ||
		request.envelopeFile == "" || request.encryptedFile == "" || request.trustFile == "" ||
		request.recipientTrustFile == "" || request.recipientID == "" ||
		request.destinationFile == "" || destinationSHA256 == "" || request.actorID == "" ||
		request.reason == "" || request.correlationID == "" {
		return commandRequest{}, errors.New("audit export transport arguments are incomplete")
	}
	if !validReason(request.reason) {
		return commandRequest{}, errors.New("audit export transport reason is invalid")
	}
	request.destinationDigest = parseDigest(destinationSHA256)
	if len(request.destinationDigest) != sha256.Size {
		return commandRequest{}, errors.New("audit export transport destination digest is invalid")
	}
	paths := []string{
		request.envelopeFile, request.encryptedFile, request.trustFile,
		request.recipientTrustFile, request.destinationFile,
	}
	for left := range paths {
		for right := left + 1; right < len(paths); right++ {
			if paths[left] == paths[right] {
				return commandRequest{}, errors.New("audit export transport evidence files are invalid")
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

func isHTTPStyleLoopback(raw string) bool {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "http" {
		return false
	}
	address := net.ParseIP(endpoint.Hostname())
	return address != nil && address.IsLoopback()
}
