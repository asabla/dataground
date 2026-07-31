package authz

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"

	"github.com/asabla/dataground/internal/authn"
)

var (
	correlationIDPattern = regexp.MustCompile(`^cor_[0-9a-z]{20,32}$`)
	policyDigestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Outcome string

const (
	OutcomeAllowed     Outcome = "allowed"
	OutcomeDenied      Outcome = "denied"
	OutcomeUnavailable Outcome = "unavailable"
)

type PolicyDescriptor struct {
	PolicySetID string
	Digest      string
}

type DecisionRecord struct {
	PrincipalID       string
	PrincipalKind     authn.PrincipalKind
	IsolationDomainID string
	Action            Action
	ResourceType      ResourceType
	ResourceID        string
	Outcome           Outcome
	PolicySetID       string
	PolicyDigest      string
	CorrelationID     string
}

func (record DecisionRecord) Valid() bool {
	return resourceIDPattern.MatchString(record.PrincipalID) &&
		validPrincipalKind(record.PrincipalKind) &&
		domainIDPattern.MatchString(record.IsolationDomainID) &&
		validActionResource(record.Action, record.ResourceType) &&
		resourceIDPattern.MatchString(record.ResourceID) &&
		validOutcome(record.Outcome) &&
		policySetIDPattern.MatchString(record.PolicySetID) &&
		policyDigestPattern.MatchString(record.PolicyDigest) &&
		correlationIDPattern.MatchString(record.CorrelationID)
}

type DecisionRecorder interface {
	RecordAuthorizationDecision(context.Context, DecisionRecord) error
}

type describedAuthorizer interface {
	Authorizer
	AuthorizationPolicy() PolicyDescriptor
}

type AuditedAuthorizer struct {
	delegate describedAuthorizer
	recorder DecisionRecorder
	policy   PolicyDescriptor
}

func NewAuditedAuthorizer(authorizer Authorizer, recorder DecisionRecorder) (*AuditedAuthorizer, error) {
	if nilInterface(authorizer) || nilInterface(recorder) {
		return nil, errors.New("authorization audit dependencies are required")
	}
	delegate, ok := authorizer.(describedAuthorizer)
	if !ok {
		return nil, errors.New("authorizer policy identity is required")
	}
	policy := delegate.AuthorizationPolicy()
	if !validPolicyDescriptor(policy) {
		return nil, errors.New("authorizer policy identity is invalid")
	}
	return &AuditedAuthorizer{delegate: delegate, recorder: recorder, policy: policy}, nil
}

func (authorizer *AuditedAuthorizer) Authorize(ctx context.Context, request Request) error {
	if authorizer == nil || authorizer.delegate == nil || authorizer.recorder == nil {
		return ErrUnavailable
	}
	err := authorizer.delegate.Authorize(ctx, request)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	outcome, complete := authorizationOutcome(err)
	if !complete {
		return err
	}
	record := DecisionRecord{
		PrincipalID:       request.Principal.ID(),
		PrincipalKind:     request.Principal.Kind(),
		IsolationDomainID: request.IsolationDomainID,
		Action:            request.Action,
		ResourceType:      request.ResourceType,
		ResourceID:        request.ResourceID,
		Outcome:           outcome,
		PolicySetID:       authorizer.policy.PolicySetID,
		PolicyDigest:      authorizer.policy.Digest,
		CorrelationID:     request.CorrelationID,
	}
	if !record.Valid() {
		return ErrUnavailable
	}
	if recordErr := authorizer.recorder.RecordAuthorizationDecision(ctx, record); recordErr != nil {
		return ErrUnavailable
	}
	return err
}

func (*AuditedAuthorizer) MarshalJSON() ([]byte, error) {
	return nil, errors.New("authorizers cannot be serialized")
}

func authorizationOutcome(err error) (Outcome, bool) {
	switch {
	case err == nil:
		return OutcomeAllowed, true
	case errors.Is(err, ErrDenied):
		return OutcomeDenied, true
	case errors.Is(err, ErrUnavailable):
		return OutcomeUnavailable, true
	default:
		return "", false
	}
}

func validOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeAllowed, OutcomeDenied, OutcomeUnavailable:
		return true
	default:
		return false
	}
}

func validPrincipalKind(kind authn.PrincipalKind) bool {
	switch kind {
	case authn.PrincipalHuman,
		authn.PrincipalService,
		authn.PrincipalPlatformService,
		authn.PrincipalSandboxWorkload,
		authn.PrincipalDistributedCompute:
		return true
	default:
		return false
	}
}

func validPolicyDescriptor(policy PolicyDescriptor) bool {
	return policySetIDPattern.MatchString(policy.PolicySetID) &&
		policyDigestPattern.MatchString(policy.Digest)
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ Authorizer = (*AuditedAuthorizer)(nil)
var _ json.Marshaler = (*AuditedAuthorizer)(nil)
