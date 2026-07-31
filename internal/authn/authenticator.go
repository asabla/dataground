package authn

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

const APIAudience = "dataground-api"

const (
	minimumBearerTokenBytes = 32
	maximumBearerTokenBytes = 8 << 10
)

var (
	ErrInvalidCredential = errors.New("credential is invalid")
	ErrUnavailable       = errors.New("authentication is unavailable")

	principalIDPattern     = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,127}$`)
	isolationDomainPattern = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
)

type PrincipalKind string

const (
	PrincipalHuman              PrincipalKind = "human"
	PrincipalService            PrincipalKind = "service"
	PrincipalPlatformService    PrincipalKind = "platform-service"
	PrincipalSandboxWorkload    PrincipalKind = "sandbox-workload"
	PrincipalDistributedCompute PrincipalKind = "distributed-compute-workload"
)

type PrincipalInput struct {
	ID               string
	Kind             PrincipalKind
	Issuer           string
	Subject          string
	Audience         string
	IsolationDomains []string
}

type Principal struct {
	id               string
	kind             PrincipalKind
	issuer           string
	subject          string
	audience         string
	isolationDomains map[string]struct{}
}

func NewPrincipal(input PrincipalInput) (Principal, error) {
	if !principalIDPattern.MatchString(input.ID) {
		return Principal{}, errors.New("principal ID is invalid")
	}
	if !validPrincipalKind(input.Kind) {
		return Principal{}, errors.New("principal kind is invalid")
	}
	if input.Issuer == "" || len(input.Issuer) > 512 {
		return Principal{}, errors.New("principal issuer is invalid")
	}
	if input.Subject == "" || len(input.Subject) > 512 {
		return Principal{}, errors.New("principal subject is invalid")
	}
	if input.Audience != APIAudience {
		return Principal{}, errors.New("principal audience is invalid")
	}
	if len(input.IsolationDomains) == 0 || len(input.IsolationDomains) > 64 {
		return Principal{}, errors.New("principal isolation domains are invalid")
	}
	domains := make(map[string]struct{}, len(input.IsolationDomains))
	for _, domainID := range input.IsolationDomains {
		if !isolationDomainPattern.MatchString(domainID) {
			return Principal{}, errors.New("principal isolation domain is invalid")
		}
		if _, exists := domains[domainID]; exists {
			return Principal{}, errors.New("principal isolation domains contain a duplicate")
		}
		domains[domainID] = struct{}{}
	}
	return Principal{
		id:               input.ID,
		kind:             input.Kind,
		issuer:           input.Issuer,
		subject:          input.Subject,
		audience:         input.Audience,
		isolationDomains: domains,
	}, nil
}

func (principal Principal) ID() string {
	return principal.id
}

func (principal Principal) Kind() PrincipalKind {
	return principal.kind
}

func (principal Principal) AllowsIsolationDomain(domainID string) bool {
	_, allowed := principal.isolationDomains[domainID]
	return allowed
}

func (principal Principal) Valid() bool {
	if !principalIDPattern.MatchString(principal.id) || !validPrincipalKind(principal.kind) {
		return false
	}
	if principal.audience != APIAudience || principal.issuer == "" || len(principal.issuer) > 512 {
		return false
	}
	if principal.subject == "" || len(principal.subject) > 512 {
		return false
	}
	if len(principal.isolationDomains) == 0 || len(principal.isolationDomains) > 64 {
		return false
	}
	for domainID := range principal.isolationDomains {
		if !isolationDomainPattern.MatchString(domainID) {
			return false
		}
	}
	return true
}

func validPrincipalKind(kind PrincipalKind) bool {
	switch kind {
	case PrincipalHuman, PrincipalService, PrincipalPlatformService, PrincipalSandboxWorkload, PrincipalDistributedCompute:
		return true
	default:
		return false
	}
}

func (Principal) MarshalJSON() ([]byte, error) {
	return nil, errors.New("authenticated principals cannot be serialized")
}

type Authenticator interface {
	Authenticate(context.Context, []byte) (Principal, error)
}

type DevelopmentConfig struct {
	BearerToken       []byte
	PrincipalID       string
	IsolationDomainID string
}

type DevelopmentAuthenticator struct {
	bearerDigest [sha256.Size]byte
	principal    Principal
}

func NewDevelopmentAuthenticator(config DevelopmentConfig) (*DevelopmentAuthenticator, error) {
	defer clear(config.BearerToken)
	if len(config.BearerToken) < minimumBearerTokenBytes || len(config.BearerToken) > maximumBearerTokenBytes {
		return nil, errors.New("development bearer token length is invalid")
	}
	principal, err := NewPrincipal(PrincipalInput{
		ID:               config.PrincipalID,
		Kind:             PrincipalHuman,
		Issuer:           "dataground-development",
		Subject:          config.PrincipalID,
		Audience:         APIAudience,
		IsolationDomains: []string{config.IsolationDomainID},
	})
	if err != nil {
		return nil, fmt.Errorf("development principal: %w", err)
	}
	return &DevelopmentAuthenticator{
		bearerDigest: sha256.Sum256(config.BearerToken),
		principal:    principal,
	}, nil
}

func (authenticator *DevelopmentAuthenticator) Authenticate(ctx context.Context, bearerToken []byte) (Principal, error) {
	if authenticator == nil {
		return Principal{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	if len(bearerToken) < minimumBearerTokenBytes || len(bearerToken) > maximumBearerTokenBytes {
		return Principal{}, ErrInvalidCredential
	}
	candidate := sha256.Sum256(bearerToken)
	if subtle.ConstantTimeCompare(candidate[:], authenticator.bearerDigest[:]) != 1 {
		return Principal{}, ErrInvalidCredential
	}
	if !authenticator.principal.Valid() {
		return Principal{}, ErrUnavailable
	}
	return authenticator.principal.clone(), nil
}

func (*DevelopmentAuthenticator) MarshalJSON() ([]byte, error) {
	return nil, errors.New("authenticators cannot be serialized")
}

func (principal Principal) clone() Principal {
	domains := make(map[string]struct{}, len(principal.isolationDomains))
	for domainID := range principal.isolationDomains {
		domains[domainID] = struct{}{}
	}
	principal.isolationDomains = domains
	return principal
}

var _ json.Marshaler = Principal{}
var _ json.Marshaler = (*DevelopmentAuthenticator)(nil)
