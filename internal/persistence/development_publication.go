package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/lifecycle/publication"
	"github.com/jackc/pgx/v5"
)

const DevelopmentPublicationContract = "dataground.development-publication/v1"
const StrictDevelopmentPublicationProfile = "openshell-codex-strict-candidate-development/v1"
const strictDevelopmentPublicationPolicy = "sha256:a1d56c0470c3264c4c37183352d783ebb67911d92ef2eb6ec5f7c76c61f69f39"

var (
	ErrDevelopmentPublicationUnavailable = errors.New("governed development publication is unavailable")
	publicationDigestPattern             = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	publicationAcceptancePattern         = regexp.MustCompile(`^rtlocal_[0-9a-z]{20,32}$`)
)

// DevelopmentPublicationInput binds one operator's reviewed intent. The
// verification digest covers the immutable acceptance and deployment pins;
// it does not itself establish that those inputs have been verified.
type DevelopmentPublicationInput struct {
	Contract           string
	Target             InvocationDispatchTarget
	ExpectedVersion    int
	PlanDigest         string
	PolicyDigest       string
	VerificationDigest string
	ActorID            string
	CorrelationID      string
}

// DevelopmentPublicationEvidence is supplied only by the command-owned runtime
// verifier after checking acceptance, deployment, plan and actual policy bytes.
// No public request can supply this evidence or select a verifier.
type DevelopmentPublicationEvidence struct {
	AcceptanceID   string
	Generation     uint64
	Profile        string
	ImageReference string
	ExpiresAt      time.Time
}

type DevelopmentPublicationVerifier func(context.Context) (DevelopmentPublicationEvidence, error)

func (input DevelopmentPublicationInput) Valid() bool {
	return input.Contract == DevelopmentPublicationContract && input.Target.Valid() && input.ExpectedVersion > 0 &&
		publicationDigestPattern.MatchString(input.PlanDigest) && publicationDigestPattern.MatchString(input.PolicyDigest) && publicationDigestPattern.MatchString(input.VerificationDigest) &&
		validProviderCredentialText(input.ActorID, 256) && providerCredentialCorrelationPattern.MatchString(input.CorrelationID)
}

// PublishDevelopmentRevision performs a database-only publication after bounded
// read-only verification. Publication, evidence, outbox and replay commit once;
// there is no external mutation or simulated provider receipt to reconcile.
func (repository *Repository) PublishDevelopmentRevision(ctx context.Context, input DevelopmentPublicationInput, verify DevelopmentPublicationVerifier) (CommandResult, error) {
	if repository == nil || !repository.Configured() || ctx == nil || !input.Valid() || verify == nil {
		return CommandResult{}, ErrDevelopmentPublicationUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	encoded, err := json.Marshal(input)
	if err != nil {
		return CommandResult{}, ErrDevelopmentPublicationUnavailable
	}
	target := input.Target
	idem := Idempotency{IsolationDomainID: target.IsolationDomainID, Method: "POST", Path: "/internal/development-publication/" + target.RevisionID, Key: input.CorrelationID, RequestDigest: sha256.Sum256(encoded)}
	result, err := repository.execute(ctx, idem, func(tx pgx.Tx, _ time.Time) (int, any, error) {
		verified, err := verifyDevelopmentPublication(ctx, tx, input, verify)
		if err != nil {
			return 0, nil, err
		}
		now, evidence := verified.now, verified.evidence
		operationID := identity.Derived("op", target.IsolationDomainID+":"+target.RevisionID+":development-publication:v1")
		receipt := map[string]any{"contract": DevelopmentPublicationContract, "isolationDomainId": target.IsolationDomainID, "serviceId": target.ServiceID, "revisionId": target.RevisionID, "operationId": operationID, "state": "published", "certificationEligible": false}
		terminal, err := json.Marshal(receipt)
		if err != nil {
			return 0, nil, err
		}
		// Version 2 is the atomic operator publication path. It has no queued work;
		// reference publication keeps version 1 and its existing reconciler.
		if _, err := tx.Exec(ctx, `INSERT INTO service_publication_operations (
   isolation_domain_id,id,revision_id,command,desired_state,observed_state,state_machine_version,generation,attempt,lease_token,due_at,deadline_at,terminal_result,correlation_id,actor_id,last_transition_at,created_at,updated_at
  ) VALUES ($1,$2,$3,'publish','published','published',$9,1,1,0,$4,$5,$6,$7,$8,$4,$4,$4)`, target.IsolationDomainID, operationID, target.RevisionID, now, evidence.ExpiresAt, terminal, input.CorrelationID, input.ActorID, publication.AtomicDevelopmentVersion); err != nil {
			return 0, nil, err
		}
		if err := recordDevelopmentPublication(ctx, tx, input, operationID, verified); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, receipt, nil
	})
	if err != nil {
		return CommandResult{}, ErrDevelopmentPublicationUnavailable
	}
	return result, nil
}

// Both publication paths hold the same policy, revision and provider-grant
// locks while verifying the exact reviewed inputs and current database time.
type verifiedDevelopmentPublication struct {
	now             time.Time
	evidence        DevelopmentPublicationEvidence
	grantGeneration int64
}

func verifyDevelopmentPublication(ctx context.Context, tx pgx.Tx, input DevelopmentPublicationInput, verify DevelopmentPublicationVerifier) (verifiedDevelopmentPublication, error) {
	target := input.Target
	// Use the policy administrator's lock order before taking the revision lock.
	if err := lockInvocationAuthorizationPolicyScope(ctx, tx, target.IsolationDomainID, target.ServiceID, target.RevisionID); err != nil {
		return verifiedDevelopmentPublication{}, err
	}
	revision, err := getRevisionForUpdate(ctx, tx, target.IsolationDomainID, target.RevisionID)
	if err != nil {
		return verifiedDevelopmentPublication{}, err
	}
	if revision.ServiceID != target.ServiceID || revision.State != "draft" || revision.Metadata.Version != input.ExpectedVersion || revision.RuntimeProfile != target.RuntimeProfile || !slices.Equal(revision.RequiredCapabilities, []string{GovernedInvocationRuntimeProfile}) {
		return verifiedDevelopmentPublication{}, ErrDevelopmentPublicationUnavailable
	}
	if domain.ValidateRevisionSchemas(revision.InputSchema, revision.OutputSchema) != nil {
		return verifiedDevelopmentPublication{}, ErrDevelopmentPublicationUnavailable
	}
	policy, err := getActiveInvocationAuthorizationPolicy(ctx, tx, target.IsolationDomainID, target.ServiceID, target.RevisionID)
	if err != nil || policy.Contract != "dataground.invocation-authorization-policy/v3" || "sha256:"+hex.EncodeToString(policy.PolicyDigest) != input.PolicyDigest {
		return verifiedDevelopmentPublication{}, ErrDevelopmentPublicationUnavailable
	}
	var planDigest, image, enforcement string
	err = tx.QueryRow(ctx, `
   SELECT plan.plan_digest, plan.image_reference, plan.enforcement_bundle_digest
   FROM service_revision_execution_plans AS plan
   JOIN service_revision_enforcement_bundles AS bundle
     ON bundle.isolation_domain_id=plan.isolation_domain_id AND bundle.revision_id=plan.revision_id
    AND bundle.id=plan.enforcement_bundle_id AND bundle.artifact_digest=plan.enforcement_bundle_digest
   WHERE plan.isolation_domain_id=$1 AND plan.revision_id=$2
     AND plan.runtime_profile=$3 AND plan.required_capabilities=ARRAY[$3]::text[]
     AND plan.provider_profiles=ARRAY['codex']::text[]
  `, target.IsolationDomainID, target.RevisionID, target.RuntimeProfile).Scan(&planDigest, &image, &enforcement)
	if err != nil || planDigest != input.PlanDigest || enforcement != strictDevelopmentPublicationPolicy {
		return verifiedDevelopmentPublication{}, ErrDevelopmentPublicationUnavailable
	}
	if err := lockProviderCredentialGrant(ctx, tx, target.IsolationDomainID, target.RevisionID, "codex", ProviderCredentialPurposeAgentInference); err != nil {
		return verifiedDevelopmentPublication{}, err
	}
	var grantGeneration int64
	var grantOperation string
	var activatedAt, grantExpiresAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT generation,operation,activated_at,expires_at FROM provider_credential_grant_events
   WHERE isolation_domain_id=$1 AND revision_id=$2 AND provider_profile='codex' AND purpose='agent-inference'
   ORDER BY generation DESC LIMIT 1`, target.IsolationDomainID, target.RevisionID).Scan(&grantGeneration, &grantOperation, &activatedAt, &grantExpiresAt); err != nil || grantOperation != "activate" || activatedAt == nil || grantExpiresAt == nil {
		return verifiedDevelopmentPublication{}, ErrDevelopmentPublicationUnavailable
	}
	evidence, err := verify(ctx)
	if err != nil || !publicationAcceptancePattern.MatchString(evidence.AcceptanceID) || evidence.Generation == 0 || evidence.Generation > 9007199254740991 || evidence.Profile != StrictDevelopmentPublicationProfile || evidence.ImageReference != image {
		return verifiedDevelopmentPublication{}, ErrDevelopmentPublicationUnavailable
	}
	// Read database time after every lock wait and external read. Neither an old
	// acceptance nor a grant that expired during verification can publish.
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return verifiedDevelopmentPublication{}, err
	}
	if now.Before(*activatedAt) || !now.Before(*grantExpiresAt) || !now.Before(evidence.ExpiresAt) || ctx.Err() != nil {
		return verifiedDevelopmentPublication{}, ErrDevelopmentPublicationUnavailable
	}
	return verifiedDevelopmentPublication{now: now, evidence: evidence, grantGeneration: grantGeneration}, nil
}

func recordDevelopmentPublication(ctx context.Context, tx pgx.Tx, input DevelopmentPublicationInput, operationID string, verified verifiedDevelopmentPublication) error {
	target := input.Target
	now, evidence, grantGeneration := verified.now, verified.evidence, verified.grantGeneration
	if _, err := tx.Exec(ctx, `UPDATE service_revisions SET state='published',published_at=$3,generation=generation+1,version=version+1,updated_at=$3 WHERE isolation_domain_id=$1 AND id=$2`, target.IsolationDomainID, target.RevisionID, now); err != nil {
		return err
	}
	metadata, err := json.Marshal(map[string]any{"planDigest": input.PlanDigest, "policyDigest": input.PolicyDigest, "verificationDigest": input.VerificationDigest, "acceptanceId": evidence.AcceptanceID, "acceptanceGeneration": evidence.Generation, "acceptanceExpiresAt": evidence.ExpiresAt.UTC().Format(time.RFC3339Nano), "providerGrantGeneration": grantGeneration})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_records (id,isolation_domain_id,actor_id,action,resource_type,resource_id,outcome,correlation_id,operation_id,safe_metadata,occurred_at)
   VALUES ($1,$2,$3,'development-publication.accept','service-revision',$4,'accepted',$5,$6,$7,$8)`, identity.New("aud"), target.IsolationDomainID, input.ActorID, target.RevisionID, input.CorrelationID, operationID, metadata, now); err != nil {
		return err
	}
	if err := writeOutboxAndAudit(ctx, tx, target.IsolationDomainID, OperationKindPublication, operationID, "service-publication.published", input.ActorID, input.CorrelationID, "succeeded", operationID, now); err != nil {
		return err
	}
	return nil
}
