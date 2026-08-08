package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
)

const (
	// MaximumEnforcementPolicyBytes is the shared admission and object-adapter
	// limit for materialized enforcement policies.
	MaximumEnforcementPolicyBytes = 4 << 20
	maximumEnforcementPolicyBytes = MaximumEnforcementPolicyBytes
)

var (
	ErrEnforcementBundleMissing  = errors.New("enforcement bundle not found")
	ErrEnforcementBundleMismatch = errors.New("enforcement bundle does not match execution plan")
	operationPattern             = regexp.MustCompile(`^op_[0-9a-z]{20,32}$`)
)

// EnforcementBundle is immutable policy material retrieved through the
// governed artifact boundary. Content is internal and must never be serialized
// into a public resource, log, event, or audit record.
type EnforcementBundle struct {
	IsolationDomainID string
	ID                string
	RevisionID        string
	Digest            string
	Content           []byte `json:"-"`
}

type EnforcementBundleSource interface {
	GetEnforcementBundle(context.Context, string, string) (EnforcementBundle, error)
}

type AdmissionRequest struct {
	IsolationDomainID string
	RevisionID        string
	OperationID       string
	ActorID           string
	CorrelationID     string
}

// Admission resolves the immutable inputs required before provider placement.
// It does not authorize, publish, or start a runtime; callers must complete
// those platform-owned state-machine steps around this internal boundary.
type Admission struct {
	plans       ExecutionPlanStore
	bundles     EnforcementBundleSource
	provider    admissionProvider
	credentials ProviderCredentialUseAuthorizer
}

type admissionProvider interface {
	SelectGateway(context.Context, PlacementRequest) (Placement, error)
	Create(context.Context, CreateRequest) (Execution, error)
}

func NewAdmission(
	plans ExecutionPlanStore,
	bundles EnforcementBundleSource,
	provider admissionProvider,
) (*Admission, error) {
	if plans == nil || bundles == nil || provider == nil {
		return nil, errors.New("execution admission dependencies are required")
	}
	return &Admission{plans: plans, bundles: bundles, provider: provider}, nil
}

func NewCredentialMediatedAdmission(
	plans ExecutionPlanStore,
	bundles EnforcementBundleSource,
	provider admissionProvider,
	credentials ProviderCredentialUseAuthorizer,
) (*Admission, error) {
	if credentials == nil {
		return nil, errors.New("provider credential authorizer is required")
	}
	admission, err := NewAdmission(plans, bundles, provider)
	if err != nil {
		return nil, err
	}
	admission.credentials = credentials
	return admission, nil
}

func (admission *Admission) Admit(ctx context.Context, request AdmissionRequest) (Execution, error) {
	if !operationPattern.MatchString(request.OperationID) {
		return Execution{}, errors.New("execution admission operation is invalid")
	}
	if admission.credentials != nil && (request.ActorID == "" || request.CorrelationID == "") {
		return Execution{}, ErrProviderCredentialUseDenied
	}
	plan, err := admission.plans.GetExecutionPlan(ctx, request.IsolationDomainID, request.RevisionID)
	if err != nil {
		return Execution{}, err
	}
	plan, err = NormalizeExecutionPlan(plan)
	if err != nil || plan.IsolationDomainID != request.IsolationDomainID || plan.RevisionID != request.RevisionID {
		return Execution{}, ErrExecutionPlanRevisionMismatch
	}
	if err := admission.authorizeProviderProfiles(ctx, request, plan, ProviderCredentialPhaseAdmission); err != nil {
		return Execution{}, err
	}
	bundle, err := admission.bundles.GetEnforcementBundle(ctx, request.IsolationDomainID, plan.EnforcementBundleID)
	if err != nil {
		return Execution{}, err
	}
	if bundle.IsolationDomainID != request.IsolationDomainID || bundle.ID != plan.EnforcementBundleID ||
		bundle.RevisionID != request.RevisionID || bundle.Digest != plan.EnforcementBundleDigest {
		return Execution{}, ErrEnforcementBundleMismatch
	}
	policy := slices.Clone(bundle.Content)
	if err := VerifyEnforcementPolicy(policy, bundle.Digest); err != nil {
		return Execution{}, ErrEnforcementBundleMismatch
	}

	placement, err := admission.provider.SelectGateway(ctx, PlacementRequest{
		IsolationDomainID:    request.IsolationDomainID,
		OperationID:          request.OperationID,
		RequiredCapabilities: slices.Clone(plan.RequiredCapabilities),
	})
	if err != nil {
		return Execution{}, err
	}
	if err := admission.authorizeProviderProfiles(ctx, request, plan, ProviderCredentialPhaseEffect); err != nil {
		return Execution{}, err
	}
	return admission.provider.Create(ctx, CreateRequest{
		Placement: placement, IsolationDomainID: request.IsolationDomainID, OperationID: request.OperationID,
		Image: plan.ImageReference, Policy: policy, PolicyDigest: bundle.Digest,
		ProviderProfiles: slices.Clone(plan.ProviderProfiles),
	})
}

func VerifyEnforcementPolicy(content []byte, digest string) error {
	if len(content) == 0 || len(content) > maximumEnforcementPolicyBytes || !digestPattern.MatchString(digest) {
		return ErrPolicyInvalid
	}
	actual := sha256.Sum256(content)
	if "sha256:"+hex.EncodeToString(actual[:]) != digest {
		return ErrPolicyInvalid
	}
	return nil
}

func (admission *Admission) authorizeProviderProfiles(
	ctx context.Context,
	request AdmissionRequest,
	plan ExecutionPlan,
	phase string,
) error {
	if admission.credentials == nil || len(plan.ProviderProfiles) == 0 {
		return nil
	}
	for _, profile := range plan.ProviderProfiles {
		if err := admission.credentials.AuthorizeProviderCredentialUse(ctx, ProviderCredentialUse{
			IsolationDomainID: request.IsolationDomainID,
			RevisionID:        request.RevisionID,
			OperationID:       request.OperationID,
			ProviderProfile:   profile,
			Purpose:           ProviderCredentialPurposeAgentInference,
			Phase:             phase,
			ActorID:           request.ActorID,
			CorrelationID:     request.CorrelationID,
		}); err != nil {
			return err
		}
	}
	return nil
}
