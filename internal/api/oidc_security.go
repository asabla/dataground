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
// publication, rate-limit policy selection, or executable activation.
type DurableOIDCDPoPConfig struct {
	Repository                    *persistence.Repository
	Authorizer                    authz.Authorizer
	RateLimiter                   AuthenticationRateLimiter
	ReleaseCertificationReadiness ReleaseCertificationReadiness
	ExternalOrigin                string
	OIDC                          authn.ReloadableOIDCJWTConfig
	KeysetRefresh                 authn.OIDCJWTKeysetRefreshPolicy
	DPoPClockSkew                 time.Duration
	MaximumProofAge               time.Duration
	DPoPNonce                     authn.DPoPNoncePolicy
}

// ReleaseCertificationReadiness keeps serving bound to the validity window of
// the externally signed release evidence incorporated at startup.
type ReleaseCertificationReadiness interface {
	Ready(context.Context) error
}

// DurableOIDCDPoPAssembly keeps the handler and keyset refresh capability in
// one lifetime so a caller cannot accidentally refresh a verifier that is not
// serving requests.
type DurableOIDCDPoPAssembly struct {
	handler       http.Handler
	authenticator *authn.ReloadableOIDCDPoPAuthenticator
	keysets       *authn.OIDCJWTKeysetRefreshSupervisor
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
	if config.ReleaseCertificationReadiness == nil || isNilInterface(config.ReleaseCertificationReadiness) {
		return nil, errors.New("durable OIDC DPoP release certification readiness is required")
	}
	if !config.KeysetRefresh.Valid() {
		return nil, errors.New("durable OIDC DPoP keyset refresh policy is invalid")
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
		Nonce:                config.DPoPNonce,
	})
	if err != nil {
		return nil, err
	}
	keysets, err := authenticator.NewKeysetRefreshSupervisor(config.KeysetRefresh)
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
		handler: oidcSecurityReadyHandler(
			handler,
			keysets,
			config.ReleaseCertificationReadiness,
			config.RateLimiter,
		),
		authenticator: authenticator,
		keysets:       keysets,
	}, nil
}

func (assembly *DurableOIDCDPoPAssembly) Handler() http.Handler {
	if assembly == nil || assembly.handler == nil {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			clearOIDCSecurityHeaders(request)
			writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeError(
				"AUTHENTICATION_UNAVAILABLE",
				"Authentication is temporarily unavailable.",
				true,
			)})
		})
	}
	return assembly.handler
}

func (assembly *DurableOIDCDPoPAssembly) RunOIDCKeysetRefresh(ctx context.Context) error {
	if assembly == nil || assembly.keysets == nil {
		return authn.ErrUnavailable
	}
	return assembly.keysets.Run(ctx)
}

func (assembly *DurableOIDCDPoPAssembly) OIDCKeysetReady(ctx context.Context) error {
	if assembly == nil || assembly.keysets == nil {
		return authn.ErrUnavailable
	}
	return assembly.keysets.Ready(ctx)
}

func (assembly *DurableOIDCDPoPAssembly) OIDCKeysetRefreshStatus() authn.OIDCJWTKeysetRefreshStatus {
	if assembly == nil || assembly.keysets == nil {
		return authn.OIDCJWTKeysetRefreshStatus{}
	}
	return assembly.keysets.Status()
}

func (assembly *DurableOIDCDPoPAssembly) RefreshOIDCKeyset(ctx context.Context) error {
	if assembly == nil || assembly.authenticator == nil {
		return authn.ErrUnavailable
	}
	return assembly.authenticator.RefreshKeyset(ctx)
}

type authenticationRateLimitReadiness interface {
	Ready(context.Context) error
}

func oidcSecurityReadyHandler(
	handler http.Handler,
	keysets *authn.OIDCJWTKeysetRefreshSupervisor,
	releaseCertification ReleaseCertificationReadiness,
	rateLimiter AuthenticationRateLimiter,
) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Path == "/livez" {
			clearOIDCSecurityHeaders(request)
			handler.ServeHTTP(response, request)
			return
		}
		if err := keysets.Ready(request.Context()); err != nil {
			clearOIDCSecurityHeaders(request)
			writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeError(
				"OIDC_KEYSET_UNAVAILABLE",
				"OIDC signing keys are unavailable.",
				true,
			)})
			return
		}
		if err := releaseCertification.Ready(request.Context()); err != nil {
			clearOIDCSecurityHeaders(request)
			writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeError(
				"RELEASE_CERTIFICATION_UNAVAILABLE",
				"Release certification is unavailable.",
				true,
			)})
			return
		}
		if readiness, ok := rateLimiter.(authenticationRateLimitReadiness); ok {
			if err := readiness.Ready(request.Context()); err != nil {
				clearOIDCSecurityHeaders(request)
				writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeError(
					"AUTHENTICATION_ADMISSION_UNAVAILABLE",
					"Authentication admission is temporarily unavailable.",
					true,
				)})
				return
			}
		}
		handler.ServeHTTP(response, request)
	})
}

func clearOIDCSecurityHeaders(request *http.Request) {
	if request == nil {
		return
	}
	request.Header.Del("Authorization")
	request.Header.Del("DPoP")
}

func (*DurableOIDCDPoPAssembly) MarshalJSON() ([]byte, error) {
	return nil, errors.New("durable OIDC DPoP assemblies cannot be serialized")
}

var _ json.Marshaler = (*DurableOIDCDPoPAssembly)(nil)
