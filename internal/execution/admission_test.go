package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
)

type admissionBundleSource struct {
	bundle EnforcementBundle
	err    error
	calls  int
}

func (source *admissionBundleSource) GetEnforcementBundle(
	_ context.Context,
	isolationDomainID string,
	bundleID string,
) (EnforcementBundle, error) {
	source.calls++
	if source.err != nil {
		return EnforcementBundle{}, source.err
	}
	bundle := source.bundle
	bundle.Content = slices.Clone(bundle.Content)
	return bundle, nil
}

type admissionProviderStub struct {
	placements []PlacementRequest
	creates    []CreateRequest
	selectErr  error
	createErr  error
}

func (provider *admissionProviderStub) SelectGateway(
	_ context.Context,
	request PlacementRequest,
) (Placement, error) {
	provider.placements = append(provider.placements, request)
	if provider.selectErr != nil {
		return Placement{}, provider.selectErr
	}
	return Placement{
		IsolationDomainID: request.IsolationDomainID,
		ID:                "plc_" + strings.Repeat("c", 20),
		GatewayID:         "gtw_" + strings.Repeat("d", 20),
	}, nil
}

func (provider *admissionProviderStub) Create(_ context.Context, request CreateRequest) (Execution, error) {
	request.Policy = slices.Clone(request.Policy)
	request.ProviderProfiles = slices.Clone(request.ProviderProfiles)
	provider.creates = append(provider.creates, request)
	if provider.createErr != nil {
		return Execution{}, provider.createErr
	}
	return Execution{
		IsolationDomainID: request.IsolationDomainID,
		ID:                "exe_" + strings.Repeat("e", 20),
		GatewayID:         request.Placement.GatewayID,
		State:             "provisioning",
	}, nil
}

func TestAdmissionResolvesVerifiedImmutableInputs(t *testing.T) {
	admission, source, provider, request, plan, policy := preparedAdmission(t)
	first, err := admission.Admit(context.Background(), request)
	if err != nil {
		t.Fatalf("admit execution: %v", err)
	}
	second, err := admission.Admit(context.Background(), request)
	if err != nil {
		t.Fatalf("repeat admission: %v", err)
	}
	if first != second || first.IsolationDomainID != request.IsolationDomainID {
		t.Fatalf("admission result = %#v then %#v", first, second)
	}
	if source.calls != 2 || len(provider.placements) != 2 || len(provider.creates) != 2 {
		t.Fatalf("admission calls = source:%d placement:%d create:%d", source.calls, len(provider.placements), len(provider.creates))
	}
	placement := provider.placements[0]
	if placement.IsolationDomainID != request.IsolationDomainID || placement.OperationID != request.OperationID ||
		!slices.Equal(placement.RequiredCapabilities, plan.RequiredCapabilities) {
		t.Fatalf("placement request = %#v", placement)
	}
	create := provider.creates[0]
	if create.Image != plan.ImageReference || create.PolicyDigest != plan.EnforcementBundleDigest ||
		!slices.Equal(create.Policy, policy) || !slices.Equal(create.ProviderProfiles, plan.ProviderProfiles) {
		t.Fatalf("create request = %#v", create)
	}
	source.bundle.Content[0] = 'x'
	if provider.creates[0].Policy[0] == 'x' {
		t.Fatal("provider request aliases bundle source content")
	}
}

func TestAdmissionFailsBeforePlacementOnUntrustedBundle(t *testing.T) {
	tests := map[string]func(*EnforcementBundle){
		"domain":   func(bundle *EnforcementBundle) { bundle.IsolationDomainID = "iso_" + strings.Repeat("z", 20) },
		"id":       func(bundle *EnforcementBundle) { bundle.ID = "different-bundle" },
		"revision": func(bundle *EnforcementBundle) { bundle.RevisionID = "rev_" + strings.Repeat("z", 20) },
		"digest":   func(bundle *EnforcementBundle) { bundle.Digest = "sha256:" + strings.Repeat("0", 64) },
		"content":  func(bundle *EnforcementBundle) { bundle.Content = []byte("tampered") },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			admission, source, provider, request, _, _ := preparedAdmission(t)
			change(&source.bundle)
			if _, err := admission.Admit(context.Background(), request); !errors.Is(err, ErrEnforcementBundleMismatch) {
				t.Fatalf("admit error = %v, want bundle mismatch", err)
			}
			if len(provider.placements) != 0 || len(provider.creates) != 0 {
				t.Fatal("untrusted bundle reached provider placement")
			}
		})
	}
}

func TestAdmissionFailsClosedOnMissingPlanBundleAndProvider(t *testing.T) {
	admission, source, provider, request, _, _ := preparedAdmission(t)
	source.err = ErrEnforcementBundleMissing
	if _, err := admission.Admit(context.Background(), request); !errors.Is(err, ErrEnforcementBundleMissing) {
		t.Fatalf("missing bundle error = %v", err)
	}
	if len(provider.placements) != 0 {
		t.Fatal("missing bundle reached placement")
	}

	missingRequest := request
	missingRequest.RevisionID = "rev_" + strings.Repeat("z", 20)
	if _, err := admission.Admit(context.Background(), missingRequest); !errors.Is(err, ErrExecutionPlanMissing) {
		t.Fatalf("missing plan error = %v", err)
	}

	admission, source, provider, request, _, _ = preparedAdmission(t)
	provider.selectErr = ErrNoGateway
	if _, err := admission.Admit(context.Background(), request); !errors.Is(err, ErrNoGateway) {
		t.Fatalf("placement error = %v", err)
	}
	if len(provider.creates) != 0 {
		t.Fatal("failed placement reached create")
	}
}

func TestAdmissionRejectsInvalidOperationBeforeResolution(t *testing.T) {
	admission, source, provider, request, _, _ := preparedAdmission(t)
	request.OperationID = "../../operation"
	if _, err := admission.Admit(context.Background(), request); err == nil {
		t.Fatal("invalid operation identity was accepted")
	}
	if source.calls != 0 || len(provider.placements) != 0 || len(provider.creates) != 0 {
		t.Fatal("invalid operation reached execution dependencies")
	}
}

func TestVerifyEnforcementPolicyIsBoundedAndExact(t *testing.T) {
	policy := []byte("version: 1\n")
	digest := sha256.Sum256(policy)
	validDigest := "sha256:" + hex.EncodeToString(digest[:])
	if err := VerifyEnforcementPolicy(policy, validDigest); err != nil {
		t.Fatalf("valid enforcement policy: %v", err)
	}
	for name, candidate := range map[string]struct {
		content []byte
		digest  string
	}{
		"empty":      {content: nil, digest: validDigest},
		"oversized":  {content: make([]byte, maximumEnforcementPolicyBytes+1), digest: validDigest},
		"unprefixed": {content: policy, digest: strings.TrimPrefix(validDigest, "sha256:")},
		"uppercase":  {content: policy, digest: strings.ToUpper(validDigest)},
		"mismatch":   {content: []byte("version: 2\n"), digest: validDigest},
	} {
		t.Run(name, func(t *testing.T) {
			if err := VerifyEnforcementPolicy(candidate.content, candidate.digest); !errors.Is(err, ErrPolicyInvalid) {
				t.Fatalf("verify error = %v, want ErrPolicyInvalid", err)
			}
		})
	}
}

func preparedAdmission(
	t *testing.T,
) (*Admission, *admissionBundleSource, *admissionProviderStub, AdmissionRequest, ExecutionPlan, []byte) {
	t.Helper()
	policy := []byte("version: 1\nfilesystem_policy: {}\n")
	digest := sha256.Sum256(policy)
	plan := testExecutionPlan()
	plan.EnforcementBundleDigest = "sha256:" + hex.EncodeToString(digest[:])
	plans := NewMemoryExecutionPlanStore()
	_, err := plans.BindExecutionPlan(context.Background(), ExecutionPlanBinding{
		Plan: plan, ActorID: "worker:resolver", CorrelationID: "correlation-admission",
	})
	if err != nil {
		t.Fatalf("bind plan: %v", err)
	}
	source := &admissionBundleSource{bundle: EnforcementBundle{
		IsolationDomainID: plan.IsolationDomainID, ID: plan.EnforcementBundleID,
		RevisionID: plan.RevisionID, Digest: plan.EnforcementBundleDigest, Content: slices.Clone(policy),
	}}
	provider := &admissionProviderStub{}
	admission, err := NewAdmission(plans, source, provider)
	if err != nil {
		t.Fatalf("new admission: %v", err)
	}
	return admission, source, provider, AdmissionRequest{
		IsolationDomainID: plan.IsolationDomainID,
		RevisionID:        plan.RevisionID,
		OperationID:       "op_" + strings.Repeat("f", 20),
	}, plan, policy
}
