package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/persistence"
)

// DurableOIDCDPoPConfig supplies deployment-owned dependencies for the full
// durable OIDC and DPoP security chain. It does not define ingress, keyset
// refresh scheduling, rate-limit policy, or executable activation.
type DurableOIDCDPoPConfig struct {
	Repository     *persistence.Repository
	Authorizer     authz.Authorizer
	RateLimiter    AuthenticationRateLimiter
	ExternalOrigin string
	OIDC            authn.ReloadableOIDCJWTConfig
	DPoPClockSkew   time.Duration
	MaximumProofAge time.Duration
}

// DurableOIDCDPoPAssembly keeps the handler and keyset refresh capability in
// one lifetime so a caller cannot accidentally refresh a verifier that is not
// serving requests.
type DurableOIDCDPoPAssembly struct {
	handler       http.Handler
	authenticator *authn.ReloadableOIDCDPoPAuthenticator
}

func NewDurableOIDCDPoPAssembly(
	ctx context.Context,
	config DurableOIDCDPoPConfig,
) (*DurableOIDCDPoPAssembly, error) {
	if ctx == nil || config.Repository == nil || !config.Repository.Configured() {
		return nil, errors.New("durable OIDC DPoP repository is required")
	}
	if config.Authorizer == nil || isNilInterface(config.Authorizer) {
		return nil, errors.New("durable OIDC DPoP authorizer is required")
	}
	if config.RateLimiter == nil || isNilInterface(config.RateLimiter) {
		return nil, errors.New("durable OIDC DPoP rate limiter is required")
	}
	binder, err := NewDPoPRequestBinder(config.ExternalOrigin)
	if err != nil {
		return nil, err
	}
	auditedAuthorizer, err := authz.NewAuditedAuthorizer(config.Authorizer, config.Repository)
	if err != nil {
		return nil, err
	}
	authenticator, err := authn.NewReloadableOIDCDPoPAuthenticator(ctx, authn.ReloadableOIDCDPoPConfig{
		Issuer:               config.OIDC.Issuer,
		Audience:             config.OIDC.Audience,
		Algorithms:           append([]string(nil), config.OIDC.Algorithms...),
		KeysetSource:         config.OIDC.Source,
		IdentityResolver:     config.Repository,
		ReplayStore:          config.Repository,
		JWTClockSkew:         config.OIDC.ClockSkew,
		MaximumTokenLifetime: config.OIDC.MaximumLifetime,
		DPoPClockSkew:        config.DPoPClockSkew,
		MaximumProofAge:      config.MaximumProofAge,
	})
	if err != nil {
		return nil, err
	}
	auditedAuthenticator, err := authn.NewAuditedAuthenticator(authenticator, config.Repository)
	if err != nil {
		return nil, err
	}
	handler, err := NewDurableRateLimitedDPoPHandler(
		config.Repository,
		auditedAuthenticator,
		auditedAuthorizer,
		binder,
		config.RateLimiter,
	)
	if err != nil {
		return nil, err
	}
	return &DurableOIDCDPoPAssembly{
		handler:       handler,
		authenticator: authenticator,
	}, nil
}

func (assembly *DurableOIDCDPoPAssembly) Handler() http.Handler {
	if assembly == nil || assembly.handler == nil {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			request.Header.Del("Authorization")
			request.Header.Del("DPoP")
			writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeError(
				"AUTHENTICATION_UNAVAILABLE",
				"Authentication is temporarily unavailable.",
				true,
			)})
		})
	}
	return assembly.handler
}

func (assembly *DurableOIDCDPoPAssembly) RefreshOIDCKeyset(ctx context.Context) error {
	if assembly == nil || assembly.authenticator == nil {
		return authn.ErrUnavailable
	}
	return assembly.authenticator.RefreshKeyset(ctx)
}

func (*DurableOIDCDPoPAssembly) MarshalJSON() ([]byte, error) {
	return nil, errors.New("durable OIDC DPoP assemblies cannot be serialized")
}

var _ json.Marshaler = (*DurableOIDCDPoPAssembly)(nil)
