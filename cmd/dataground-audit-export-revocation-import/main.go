package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/auditseal"
	"github.com/asabla/dataground/internal/persistence"
)

type auditExportRevocationImportRepository interface {
	ReplayAuditExportRevocationAcquisition(
		context.Context,
		persistence.AuditExportRevocationAcquisitionReplay,
	) (bool, error)
	AuthorizeAuditExportRevocationSource(
		context.Context, string, string, string, string,
	) (int64, error)
	AuthorizeAuditExportRevocationCredentials(
		context.Context, string, string, string, string,
		persistence.AuditExportRevocationCredentialEvidence,
		persistence.AuditExportRevocationCredentialEvidence,
	) (persistence.AuditExportRevocationCredentialGenerations, error)
	RecordAuditExportRecipientProofRevocation(
		context.Context,
		persistence.AuditExportRecipientProofRevocationRecord,
	) error
	RecordAuditExportWorkloadIdentityRevocation(
		context.Context,
		persistence.AuditExportWorkloadIdentityRevocationRecord,
	) error
}

type commandRequest struct {
	isolationDomainID    string
	purpose              string
	sourceID             string
	sourceRegistryFile   string
	sourceRegistrySHA256 string
	actorID              string
	reason               string
	correlationID        string
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "DataGround audit export revocation import failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	return runWithTransport(ctx, arguments, nil)
}

func runWithTransport(ctx context.Context, arguments []string, transport *http.Transport) error {
	request, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("audit export revocation import context is required")
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
	repository := persistence.NewRepository(pool)
	replayed, err := replayRequest(operationCtx, repository, request)
	if err != nil || replayed {
		return err
	}
	sourceGeneration, err := repository.AuthorizeAuditExportRevocationSource(
		operationCtx, request.isolationDomainID, request.purpose,
		request.sourceID, request.sourceRegistrySHA256,
	)
	if err != nil {
		return err
	}
	acquirer, err := auditseal.NewRevocationNoticeAcquirer(auditseal.RevocationNoticeAcquisitionConfig{
		IsolationDomainID: request.isolationDomainID, Purpose: request.purpose,
		SourceID: request.sourceID, SourceRegistryFile: request.sourceRegistryFile,
		SourceRegistrySHA256: request.sourceRegistrySHA256, Transport: transport,
	})
	if err != nil {
		return err
	}
	defer acquirer.Close()
	noticeCredential, trustCredential, err := acquirer.CredentialEvidence()
	if err != nil {
		return err
	}
	credentialGenerations, err := repository.AuthorizeAuditExportRevocationCredentials(
		operationCtx, request.isolationDomainID, request.purpose,
		request.sourceID, request.sourceRegistrySHA256,
		persistenceCredentialEvidence(noticeCredential),
		persistenceCredentialEvidence(trustCredential),
	)
	if err != nil {
		return err
	}
	acquired, err := acquirer.Acquire(operationCtx, time.Now().UTC())
	if err != nil {
		return err
	}
	return executeRequest(
		operationCtx, repository, request, acquired, sourceGeneration, credentialGenerations,
	)
}

func replayRequest(
	ctx context.Context,
	repository auditExportRevocationImportRepository,
	request commandRequest,
) (bool, error) {
	if repository == nil {
		return false, errors.New("audit export revocation import repository is required")
	}
	reasonDigest := sha256.Sum256([]byte(request.reason))
	return repository.ReplayAuditExportRevocationAcquisition(ctx, persistence.AuditExportRevocationAcquisitionReplay{
		Purpose: request.purpose, IsolationDomainID: request.isolationDomainID,
		SourceID: request.sourceID, SourceRegistrySHA256: request.sourceRegistrySHA256,
		ActorID: request.actorID, ReasonDigest: reasonDigest[:], CorrelationID: request.correlationID,
	})
}

func executeRequest(
	ctx context.Context,
	repository auditExportRevocationImportRepository,
	request commandRequest,
	acquired auditseal.AcquiredRevocationNotice,
	sourceGeneration int64,
	credentialGenerations persistence.AuditExportRevocationCredentialGenerations,
) error {
	if repository == nil {
		return errors.New("audit export revocation import repository is required")
	}
	switch request.purpose {
	case auditseal.RevocationNoticePurposeRecipientProof:
		record := newRecipientProofRecord(request, acquired, sourceGeneration, credentialGenerations)
		if !record.Valid() {
			return errors.New("acquired recipient proof revocation record is invalid")
		}
		return repository.RecordAuditExportRecipientProofRevocation(ctx, record)
	case auditseal.RevocationNoticePurposeWorkloadIdentity:
		record := newWorkloadIdentityRecord(request, acquired, sourceGeneration, credentialGenerations)
		if !record.Valid() {
			return errors.New("acquired workload identity revocation record is invalid")
		}
		return repository.RecordAuditExportWorkloadIdentityRevocation(ctx, record)
	default:
		return errors.New("audit export revocation import purpose is invalid")
	}
}

func newRecipientProofRecord(
	request commandRequest,
	acquired auditseal.AcquiredRevocationNotice,
	sourceGeneration int64,
	credentialGenerations persistence.AuditExportRevocationCredentialGenerations,
) persistence.AuditExportRecipientProofRevocationRecord {
	if acquired.RecipientProof == nil || acquired.Purpose != request.purpose ||
		acquired.SourceID != request.sourceID || acquired.SourceRegistrySHA256 != request.sourceRegistrySHA256 {
		return persistence.AuditExportRecipientProofRevocationRecord{}
	}
	verified := acquired.RecipientProof
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
		Acquisition: &persistence.AuditExportRevocationAcquisition{
			Contract: persistence.AuditExportRevocationAcquisitionContract,
			Purpose:  acquired.Purpose, SourceID: acquired.SourceID,
			SourceRegistrySHA256:       acquired.SourceRegistrySHA256,
			SourceGeneration:           sourceGeneration,
			NoticeCredentialSHA256:     acquired.NoticeCredential.CredentialSHA256,
			NoticeCredentialGeneration: credentialGenerations.Notice,
			TrustCredentialSHA256:      acquired.TrustCredential.CredentialSHA256,
			TrustCredentialGeneration:  credentialGenerations.Trust,
		},
	}
}

func newWorkloadIdentityRecord(
	request commandRequest,
	acquired auditseal.AcquiredRevocationNotice,
	sourceGeneration int64,
	credentialGenerations persistence.AuditExportRevocationCredentialGenerations,
) persistence.AuditExportWorkloadIdentityRevocationRecord {
	if acquired.WorkloadIdentity == nil || acquired.Purpose != request.purpose ||
		acquired.SourceID != request.sourceID || acquired.SourceRegistrySHA256 != request.sourceRegistrySHA256 {
		return persistence.AuditExportWorkloadIdentityRevocationRecord{}
	}
	verified := acquired.WorkloadIdentity
	reasonDigest := sha256.Sum256([]byte(request.reason))
	return persistence.AuditExportWorkloadIdentityRevocationRecord{
		Contract:           persistence.AuditExportWorkloadIdentityRevocationRecordContract,
		RevocationContract: verified.Contract, RevocationSHA256: verified.SHA256,
		IsolationDomainID: verified.IsolationDomainID, Scope: verified.Scope,
		WorkloadIdentityAuthorityID:        verified.WorkloadIdentityAuthorityID,
		WorkloadIdentityTrustProfileSHA256: verified.WorkloadIdentityTrustProfileSHA256,
		WorkloadIdentitySigningKeyID:       verified.WorkloadIdentitySigningKeyID,
		ExternalReasonSHA256:               verified.ReasonSHA256,
		RevocationAuthorityID:              verified.RevocationAuthorityID,
		RevocationTrustProfileSHA256:       verified.RevocationTrustProfileSHA256,
		RevocationSigningKeyID:             verified.RevocationSigningKeyID,
		IssuedAt:                           verified.IssuedAt, EffectiveAt: verified.EffectiveAt,
		ActorID: request.actorID, ReasonDigest: reasonDigest[:], CorrelationID: request.correlationID,
		Acquisition: &persistence.AuditExportRevocationAcquisition{
			Contract: persistence.AuditExportRevocationAcquisitionContract,
			Purpose:  acquired.Purpose, SourceID: acquired.SourceID,
			SourceRegistrySHA256:       acquired.SourceRegistrySHA256,
			SourceGeneration:           sourceGeneration,
			NoticeCredentialSHA256:     acquired.NoticeCredential.CredentialSHA256,
			NoticeCredentialGeneration: credentialGenerations.Notice,
			TrustCredentialSHA256:      acquired.TrustCredential.CredentialSHA256,
			TrustCredentialGeneration:  credentialGenerations.Trust,
		},
	}
}

func persistenceCredentialEvidence(
	evidence auditseal.RevocationSourceCredentialEvidence,
) persistence.AuditExportRevocationCredentialEvidence {
	return persistence.AuditExportRevocationCredentialEvidence{
		Endpoint: evidence.Endpoint, CredentialSHA256: evidence.CredentialSHA256,
		ActivatedAt: evidence.ActivatedAt, ExpiresAt: evidence.ExpiresAt,
	}
}

func parseArguments(arguments []string) (commandRequest, error) {
	var request commandRequest
	flags := flag.NewFlagSet("dataground-audit-export-revocation-import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&request.isolationDomainID, "isolation-domain", "", "exact isolation domain identifier")
	flags.StringVar(&request.purpose, "purpose", "", "recipient-proof or workload-identity")
	flags.StringVar(&request.sourceID, "source", "", "registered external revocation source identifier")
	flags.StringVar(&request.sourceRegistryFile, "source-registry-file", "", "canonical deployment-owned source registry")
	flags.StringVar(&request.sourceRegistrySHA256, "source-registry-sha256", "", "exact source registry digest")
	flags.StringVar(&request.actorID, "actor", "", "authorized operator identifier")
	flags.StringVar(&request.reason, "reason", "", "operator-visible acquisition reason")
	flags.StringVar(&request.correlationID, "correlation-id", "", "stable operation correlation identifier")
	if err := flags.Parse(arguments); err != nil {
		return commandRequest{}, errors.New("audit export revocation import arguments are invalid")
	}
	if flags.NArg() != 0 || request.isolationDomainID == "" || request.sourceID == "" ||
		request.sourceRegistryFile == "" || request.sourceRegistrySHA256 == "" ||
		request.actorID == "" || request.reason == "" || request.correlationID == "" {
		return commandRequest{}, errors.New("audit export revocation import arguments are incomplete")
	}
	if request.purpose != auditseal.RevocationNoticePurposeRecipientProof &&
		request.purpose != auditseal.RevocationNoticePurposeWorkloadIdentity {
		return commandRequest{}, errors.New("audit export revocation import purpose is invalid")
	}
	if !validReason(request.reason) {
		return commandRequest{}, errors.New("audit export revocation import reason is invalid")
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
