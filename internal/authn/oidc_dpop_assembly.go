package authn

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ReloadableOIDCDPoPConfig describes the complete authentication chain used
// by a durable DPoP-bound API. Deployment policy and HTTP composition remain
// outside this package.
type ReloadableOIDCDPoPConfig struct {
	Issuer                 string
	Audience               string
	ProviderID             string
	ProviderRegistrySHA256 string
	Algorithms             []string
	KeysetSource           OIDCJWTKeysetSource
	IdentityResolver       OIDCIdentityResolver
	ReplayStore            DPoPReplayStore
	Nonce                  DPoPNoncePolicy
	JWTClockSkew           time.Duration
	MaximumTokenLifetime   time.Duration
	DPoPClockSkew          time.Duration
	MaximumProofAge        time.Duration
}

// ReloadableOIDCDPoPAuthenticator owns the signing-key lifecycle behind one
// OIDC authenticator. Refresh replaces the complete keyset generation without
// changing the identity or replay boundaries.
type ReloadableOIDCDPoPAuthenticator struct {
	authenticator *OIDCAuthenticator
	keysets       *ReloadableOIDCJWTVerifier
}

func NewReloadableOIDCDPoPAuthenticator(
	ctx context.Context,
	config ReloadableOIDCDPoPConfig,
) (*ReloadableOIDCDPoPAuthenticator, error) {
	if ctx == nil || nilOIDCDependency(config.IdentityResolver) ||
		nilDPoPDependency(config.ReplayStore) {
		return nil, errors.New("OIDC DPoP dependencies are required")
	}
	if !validDPoPTimeBounds(config.DPoPClockSkew, config.MaximumProofAge) || !config.Nonce.Valid() {
		return nil, errors.New("OIDC DPoP profile is invalid")
	}
	keysets, err := NewReloadableOIDCJWTVerifier(ctx, ReloadableOIDCJWTConfig{
		Issuer:                 config.Issuer,
		Audience:               config.Audience,
		ProviderID:             config.ProviderID,
		ProviderRegistrySHA256: config.ProviderRegistrySHA256,
		Algorithms:             append([]string(nil), config.Algorithms...),
		ClockSkew:              config.JWTClockSkew,
		MaximumLifetime:        config.MaximumTokenLifetime,
		Source:                 config.KeysetSource,
	})
	if err != nil {
		return nil, err
	}
	dpop, err := NewDPoPTokenVerifier(DPoPConfig{
		Verifier:        keysets,
		Replays:         config.ReplayStore,
		Nonce:           config.Nonce,
		ClockSkew:       config.DPoPClockSkew,
		MaximumProofAge: config.MaximumProofAge,
	})
	if err != nil {
		return nil, err
	}
	authenticator, err := NewOIDCAuthenticator(OIDCConfig{
		Issuer:   config.Issuer,
		Audience: config.Audience,
		Verifier: dpop,
		Resolver: config.IdentityResolver,
	})
	if err != nil {
		return nil, err
	}
	return &ReloadableOIDCDPoPAuthenticator{
		authenticator: authenticator,
		keysets:       keysets,
	}, nil
}

func (authenticator *ReloadableOIDCDPoPAuthenticator) Authenticate(
	ctx context.Context,
	bearerToken []byte,
) (Principal, error) {
	if authenticator == nil || nilOIDCDependency(authenticator.authenticator) ||
		nilOIDCDependency(authenticator.keysets) || ctx == nil {
		return Principal{}, ErrUnavailable
	}
	return authenticator.authenticator.Authenticate(ctx, bearerToken)
}

func (authenticator *ReloadableOIDCDPoPAuthenticator) NewKeysetRefreshSupervisor(
	policy OIDCJWTKeysetRefreshPolicy,
) (*OIDCJWTKeysetRefreshSupervisor, error) {
	if authenticator == nil || nilOIDCDependency(authenticator.keysets) {
		return nil, ErrUnavailable
	}
	return NewOIDCJWTKeysetRefreshSupervisor(authenticator.keysets, policy)
}

func (authenticator *ReloadableOIDCDPoPAuthenticator) RefreshKeyset(ctx context.Context) error {
	if authenticator == nil || nilOIDCDependency(authenticator.keysets) {
		return ErrUnavailable
	}
	return authenticator.keysets.Refresh(ctx)
}

func (*ReloadableOIDCDPoPAuthenticator) AuthenticationMethod() AuthenticationMethod {
	return AuthenticationMethodOIDC
}

func (*ReloadableOIDCDPoPAuthenticator) MarshalJSON() ([]byte, error) {
	return nil, errors.New("authenticators cannot be serialized")
}

var _ Authenticator = (*ReloadableOIDCDPoPAuthenticator)(nil)
var _ json.Marshaler = (*ReloadableOIDCDPoPAuthenticator)(nil)
