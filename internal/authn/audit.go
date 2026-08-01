package authn

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
)

var authenticationCorrelationIDPattern = regexp.MustCompile(`^cor_[0-9a-z]{20,32}$`)

type AuthenticationMethod string

const (
	AuthenticationMethodDevelopmentBearer AuthenticationMethod = "development-bearer"
	AuthenticationMethodOIDC              AuthenticationMethod = "oidc"
)

type AuthenticationOutcome string

const (
	AuthenticationOutcomeAuthenticated AuthenticationOutcome = "authenticated"
	AuthenticationOutcomeRejected      AuthenticationOutcome = "rejected"
	AuthenticationOutcomeScopeDenied   AuthenticationOutcome = "scope-denied"
	AuthenticationOutcomeUnavailable   AuthenticationOutcome = "unavailable"
)

type AuthenticationAttemptScope struct {
	IsolationDomainID string
	CorrelationID     string
}

func (scope AuthenticationAttemptScope) Valid() bool {
	return isolationDomainPattern.MatchString(scope.IsolationDomainID) &&
		authenticationCorrelationIDPattern.MatchString(scope.CorrelationID)
}

type authenticationAttemptScopeKey struct{}

func WithAuthenticationAttemptScope(
	ctx context.Context,
	scope AuthenticationAttemptScope,
) (context.Context, error) {
	if ctx == nil || !scope.Valid() {
		return nil, errors.New("authentication attempt scope is invalid")
	}
	return context.WithValue(ctx, authenticationAttemptScopeKey{}, scope), nil
}

type AuthenticationAttemptRecord struct {
	IsolationDomainID string
	PrincipalID       string
	PrincipalKind     PrincipalKind
	Method            AuthenticationMethod
	Outcome           AuthenticationOutcome
	CorrelationID     string
}

func (record AuthenticationAttemptRecord) Valid() bool {
	if !isolationDomainPattern.MatchString(record.IsolationDomainID) ||
		!authenticationCorrelationIDPattern.MatchString(record.CorrelationID) ||
		!validAuthenticationMethod(record.Method) ||
		!validAuthenticationOutcome(record.Outcome) {
		return false
	}
	hasPrincipal := record.PrincipalID != "" || record.PrincipalKind != ""
	if record.Outcome == AuthenticationOutcomeAuthenticated {
		return principalIDPattern.MatchString(record.PrincipalID) && validPrincipalKind(record.PrincipalKind)
	}
	return !hasPrincipal
}

type AuthenticationAttemptRecorder interface {
	RecordAuthenticationAttempt(context.Context, AuthenticationAttemptRecord) error
}

type describedAuthenticator interface {
	Authenticator
	AuthenticationMethod() AuthenticationMethod
}

type AuditedAuthenticator struct {
	delegate describedAuthenticator
	recorder AuthenticationAttemptRecorder
	method   AuthenticationMethod
}

func NewAuditedAuthenticator(
	authenticator Authenticator,
	recorder AuthenticationAttemptRecorder,
) (*AuditedAuthenticator, error) {
	if nilAuthenticationDependency(authenticator) || nilAuthenticationDependency(recorder) {
		return nil, errors.New("authentication audit dependencies are required")
	}
	delegate, ok := authenticator.(describedAuthenticator)
	if !ok {
		return nil, errors.New("authentication method identity is required")
	}
	method := delegate.AuthenticationMethod()
	if !validAuthenticationMethod(method) {
		return nil, errors.New("authentication method identity is invalid")
	}
	return &AuditedAuthenticator{delegate: delegate, recorder: recorder, method: method}, nil
}

func (authenticator *AuditedAuthenticator) Authenticate(
	ctx context.Context,
	bearerToken []byte,
) (Principal, error) {
	if authenticator == nil ||
		nilAuthenticationDependency(authenticator.delegate) ||
		nilAuthenticationDependency(authenticator.recorder) ||
		!validAuthenticationMethod(authenticator.method) {
		return Principal{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	scope, ok := ctx.Value(authenticationAttemptScopeKey{}).(AuthenticationAttemptScope)
	if !ok || !scope.Valid() {
		return Principal{}, ErrUnavailable
	}

	principal, authenticationErr := authenticator.delegate.Authenticate(ctx, bearerToken)
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	record, resultPrincipal, resultErr := authenticationAttemptResult(
		scope,
		authenticator.method,
		principal,
		authenticationErr,
	)
	if !record.Valid() {
		return Principal{}, ErrUnavailable
	}
	if err := authenticator.recorder.RecordAuthenticationAttempt(ctx, record); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Principal{}, ctxErr
		}
		return Principal{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	return resultPrincipal, resultErr
}

func (authenticator *AuditedAuthenticator) AuthenticationMethod() AuthenticationMethod {
	if authenticator == nil {
		return ""
	}
	return authenticator.method
}

func (*AuditedAuthenticator) MarshalJSON() ([]byte, error) {
	return nil, errors.New("authenticators cannot be serialized")
}

func authenticationAttemptResult(
	scope AuthenticationAttemptScope,
	method AuthenticationMethod,
	principal Principal,
	err error,
) (AuthenticationAttemptRecord, Principal, error) {
	record := AuthenticationAttemptRecord{
		IsolationDomainID: scope.IsolationDomainID,
		Method:            method,
		CorrelationID:     scope.CorrelationID,
	}
	switch {
	case err == nil && !principal.Valid():
		record.Outcome = AuthenticationOutcomeUnavailable
		return record, Principal{}, ErrUnavailable
	case err == nil && !principal.AllowsIsolationDomain(scope.IsolationDomainID):
		record.Outcome = AuthenticationOutcomeScopeDenied
		return record, principal, nil
	case err == nil:
		record.PrincipalID = principal.ID()
		record.PrincipalKind = principal.Kind()
		record.Outcome = AuthenticationOutcomeAuthenticated
		return record, principal, nil
	case errors.Is(err, ErrInvalidCredential):
		record.Outcome = AuthenticationOutcomeRejected
		return record, Principal{}, ErrInvalidCredential
	default:
		record.Outcome = AuthenticationOutcomeUnavailable
		return record, Principal{}, ErrUnavailable
	}
}

func validAuthenticationMethod(method AuthenticationMethod) bool {
	switch method {
	case AuthenticationMethodDevelopmentBearer, AuthenticationMethodOIDC:
		return true
	default:
		return false
	}
}

func validAuthenticationOutcome(outcome AuthenticationOutcome) bool {
	switch outcome {
	case AuthenticationOutcomeAuthenticated,
		AuthenticationOutcomeRejected,
		AuthenticationOutcomeScopeDenied,
		AuthenticationOutcomeUnavailable:
		return true
	default:
		return false
	}
}

func nilAuthenticationDependency(value any) bool {
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

var _ Authenticator = (*AuditedAuthenticator)(nil)
var _ json.Marshaler = (*AuditedAuthenticator)(nil)

