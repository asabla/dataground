package reconcile

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/authz"
)

type publicationDecisionRecorder struct {
	records []authz.PublicationDecisionRecord
	failure bool
}

func (recorder *publicationDecisionRecorder) RecordPublicationAuthorizationDecision(_ context.Context, record authz.PublicationDecisionRecord) error {
	recorder.records = append(recorder.records, record)
	if recorder.failure {
		return errors.New("private audit detail")
	}
	return nil
}

type publicationPolicySource struct {
	policy  InvocationAuthorizationPolicy
	failure bool
}

func (source publicationPolicySource) ResolveInvocationAuthorizationPolicy(context.Context, InvocationAuthorizationPolicyScope) (InvocationAuthorizationPolicy, error) {
	if source.failure {
		return InvocationAuthorizationPolicy{}, errors.New("private source detail")
	}
	return source.policy, nil
}

func publicationRequestFixture() authz.PublicationRequest {
	return authz.PublicationRequest{ActorID: "actor_1", IsolationDomainID: "iso_00000000000000000001", ServiceID: "svc_00000000000000000001", RevisionID: "rev_00000000000000000001", OperationID: "op_00000000000000000001", CorrelationID: "cor_00000000000000000001", Phase: "entry", ExpectedVersion: 1, PlanDigest: "sha256:" + strings.Repeat("a", 64), VerificationDigest: "sha256:" + strings.Repeat("b", 64)}
}
func publicationPolicyFixture(t *testing.T, policies string) (InvocationAuthorizationPolicy, authz.PublicationRequest) {
	t.Helper()
	request := publicationRequestFixture()
	policy, err := NewInvocationAuthorizationPolicyWithPublicationEntities(InvocationAuthorizationPolicyScope{request.IsolationDomainID, request.ServiceID, request.RevisionID}, "publication-policy", CanonicalPublicationCedarSchema(), []byte(policies), canonicalInvocationEntityFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	request.PolicyDigest = "sha256:" + hex.EncodeToString(policy.Digest[:])
	return policy, request
}
func TestPublicationAuthorizationRequiresExplicitActionPhaseScopeAndReview(t *testing.T) {
	for _, mode := range []string{"entry", "effect", "actor", "scope", "revision", "version", "plan", "verification", "phase", "token", "policy digest", "source scope", "audit", "source", "diagnostic", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			policyText := `permit(principal in DataGround::Role::"invoker", action == DataGround::Action::"publish", resource == DataGround::ServiceRevision::"rev_00000000000000000001") when { context.isolationDomainID == "iso_00000000000000000001" && context.expectedVersion == 1 && context.planDigest == "sha256:` + strings.Repeat("a", 64) + `" && context.verificationDigest == "sha256:` + strings.Repeat("b", 64) + `" && (context.phase == "entry" || context.phase == "effect" && context.fencingToken == 3) };`
			if mode == "diagnostic" {
				policyText = `permit(principal,action,resource) when { context.missing == "private" };`
			}
			policy, request := publicationPolicyFixture(t, policyText)
			source := publicationPolicySource{policy: policy}
			recorder := &publicationDecisionRecorder{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "effect":
				request.Phase = "effect"
				request.FencingToken = 3
			case "actor":
				request.ActorID = "actor_other"
			case "scope":
				request.IsolationDomainID = "iso_00000000000000000002"
			case "revision":
				request.RevisionID = "rev_00000000000000000002"
			case "version":
				request.ExpectedVersion = 2
			case "plan":
				request.PlanDigest = "sha256:" + strings.Repeat("c", 64)
			case "verification":
				request.VerificationDigest = "sha256:" + strings.Repeat("c", 64)
			case "phase":
				request.Phase = "repair"
			case "token":
				request.Phase = "effect"
				request.FencingToken = 2
			case "policy digest":
				request.PolicyDigest = "sha256:" + strings.Repeat("f", 64)
			case "source scope":
				source.policy.ServiceID = "svc_00000000000000000002"
			case "audit":
				recorder.failure = true
			case "source":
				source.failure = true
			case "cancelled":
				cancel()
			}
			authorizer, err := NewPublicationAuthorizer(source, recorder)
			if err != nil {
				t.Fatal(err)
			}
			err = authorizer.AuthorizePublication(ctx, request)
			if mode == "entry" || mode == "effect" {
				if err != nil || len(recorder.records) != 1 || recorder.records[0].Outcome != authz.OutcomeAllowed || recorder.records[0].Request != request {
					t.Fatal("publication authority or provenance lost", err)
				}
			} else if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("invalid publication authorized or dependency detail leaked", err)
			}
			switch mode {
			case "actor", "version", "plan", "verification", "token":
				if !errors.Is(err, ErrPublicationAuthorizationDenied) || len(recorder.records) != 1 || recorder.records[0].Outcome != authz.OutcomeDenied {
					t.Fatal("denial not audited", err)
				}
			case "diagnostic":
				if err != ErrPublicationAuthorizationUnavailable || len(recorder.records) != 1 || recorder.records[0].Outcome != authz.OutcomeUnavailable {
					t.Fatal("evaluation error mislabeled", err)
				}
			case "scope", "revision", "phase", "policy digest", "source scope", "source", "cancelled":
				if len(recorder.records) != 0 {
					t.Fatal("lookup or input failure mislabeled as completed decision")
				}
			}
		})
	}
}
func TestOlderWildcardPoliciesNeverAcquirePublicationAuthority(t *testing.T) {
	request := publicationRequestFixture()
	scope := InvocationAuthorizationPolicyScope{request.IsolationDomainID, request.ServiceID, request.RevisionID}
	constructors := []struct {
		schema []byte
		build  func(InvocationAuthorizationPolicyScope, string, []byte, []byte, []byte) (InvocationAuthorizationPolicy, error)
	}{
		{CanonicalInvocationCedarSchema(), func(scope InvocationAuthorizationPolicyScope, id string, schema, policies, _ []byte) (InvocationAuthorizationPolicy, error) {
			return NewInvocationAuthorizationPolicy(scope, id, schema, policies)
		}},
		{CanonicalInvocationCedarEntitySchema(), NewInvocationAuthorizationPolicyWithEntities},
		{CanonicalInvocationCedarApprovalSchema(), NewInvocationAuthorizationPolicyWithApprovalEntities},
		{CanonicalInvocationCedarQuestionSchema(), NewInvocationAuthorizationPolicyWithQuestionEntities},
	}
	for _, item := range constructors {
		policy, err := item.build(scope, "legacy-policy", item.schema, []byte(`permit(principal,action,resource);`), canonicalInvocationEntityFixture(t))
		if err != nil {
			t.Fatal(err)
		}
		request.PolicyDigest = "sha256:" + hex.EncodeToString(policy.Digest[:])
		recorder := &publicationDecisionRecorder{}
		authorizer, err := NewPublicationAuthorizer(publicationPolicySource{policy: policy}, recorder)
		if err != nil {
			t.Fatal(err)
		}
		if err := authorizer.AuthorizePublication(context.Background(), request); err != ErrPublicationAuthorizationDenied || len(recorder.records) != 1 || recorder.records[0].Outcome != authz.OutcomeDenied {
			t.Fatal("older wildcard acquired publication", policy.Contract, err)
		}
	}
}
func TestPublicationAuthorizerRejectsMissingDependencies(t *testing.T) {
	policy, _ := publicationPolicyFixture(t, `permit(principal,action,resource);`)
	var missing *publicationDecisionRecorder
	if _, err := NewPublicationAuthorizer(publicationPolicySource{policy: policy}, missing); err == nil {
		t.Fatal("typed-nil recorder accepted")
	}
	if _, err := NewPublicationAuthorizer(nil, &publicationDecisionRecorder{}); err == nil {
		t.Fatal("missing source accepted")
	}
}
