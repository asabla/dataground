package authn

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

const maximumOIDCJWTKeysetSnapshotLifetime = 24 * time.Hour

var (
	ErrOIDCJWTKeysetInvalid  = errors.New("OIDC JWT keyset snapshot is invalid")
	ErrOIDCJWTKeysetRollback = errors.New("OIDC JWT keyset snapshot rollback is invalid")
	ErrOIDCJWTKeysetConflict = errors.New("OIDC JWT keyset snapshot sequence conflicts")
)

// OIDCJWTKeysetSnapshot is one deployment-published signing-key generation.
// Load transfers ownership of JWKS to the verifier so its transient copy can
// be cleared after the immutable verifier has been assembled.
type OIDCJWTKeysetSnapshot struct {
	Sequence  uint64
	JWKS      []byte
	ExpiresAt time.Time
}

type OIDCJWTKeysetSource interface {
	Load(context.Context) (OIDCJWTKeysetSnapshot, error)
}

type ReloadableOIDCJWTConfig struct {
	Issuer          string
	Audience        string
	Algorithms      []string
	ClockSkew       time.Duration
	MaximumLifetime time.Duration
	Source          OIDCJWTKeysetSource
}

// ReloadableOIDCJWTVerifier atomically replaces a complete pinned verifier.
// It never merges keysets, falls back to an older generation, or fetches keys
// from token-controlled metadata.
type ReloadableOIDCJWTVerifier struct {
	mu sync.RWMutex

	issuer          string
	audience        string
	algorithms      []string
	clockSkew       time.Duration
	maximumLifetime time.Duration
	source          OIDCJWTKeysetSource
	now             func() time.Time

	sequence  uint64
	digest    [sha256.Size]byte
	expiresAt time.Time
	verifier  OIDCTokenVerifier
}

func NewReloadableOIDCJWTVerifier(
	ctx context.Context,
	config ReloadableOIDCJWTConfig,
) (*ReloadableOIDCJWTVerifier, error) {
	if ctx == nil || nilOIDCDependency(config.Source) {
		return nil, errors.New("OIDC JWT keyset source is required")
	}
	verifier := &ReloadableOIDCJWTVerifier{
		issuer:          config.Issuer,
		audience:        config.Audience,
		algorithms:      append([]string(nil), config.Algorithms...),
		clockSkew:       config.ClockSkew,
		maximumLifetime: config.MaximumLifetime,
		source:          config.Source,
		now:             time.Now,
	}
	if err := verifier.Refresh(ctx); err != nil {
		return nil, err
	}
	return verifier, nil
}

func (verifier *ReloadableOIDCJWTVerifier) Refresh(ctx context.Context) error {
	if verifier == nil || ctx == nil || nilOIDCDependency(verifier.source) || verifier.now == nil {
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot, err := verifier.source.Load(ctx)
	defer clear(snapshot.JWKS)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now := verifier.now()
	if snapshot.Sequence == 0 || !snapshot.ExpiresAt.After(now) ||
		snapshot.ExpiresAt.After(now.Add(maximumOIDCJWTKeysetSnapshotLifetime)) {
		return ErrOIDCJWTKeysetInvalid
	}
	candidate, err := NewPinnedOIDCJWTVerifier(PinnedOIDCJWTConfig{
		Issuer:          verifier.issuer,
		Audience:        verifier.audience,
		Algorithms:      append([]string(nil), verifier.algorithms...),
		JWKS:            snapshot.JWKS,
		ClockSkew:       verifier.clockSkew,
		MaximumLifetime: verifier.maximumLifetime,
	})
	if err != nil {
		return ErrOIDCJWTKeysetInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	digest := sha256.Sum256(snapshot.JWKS)
	expiresAt := snapshot.ExpiresAt.UTC()

	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if snapshot.Sequence < verifier.sequence {
		return ErrOIDCJWTKeysetRollback
	}
	if snapshot.Sequence == verifier.sequence {
		if digest != verifier.digest || !expiresAt.Equal(verifier.expiresAt) {
			return ErrOIDCJWTKeysetConflict
		}
		return nil
	}
	verifier.sequence = snapshot.Sequence
	verifier.digest = digest
	verifier.expiresAt = expiresAt
	verifier.verifier = candidate
	return nil
}

func (verifier *ReloadableOIDCJWTVerifier) Verify(
	ctx context.Context,
	bearerToken []byte,
) (VerifiedOIDCToken, error) {
	if verifier == nil || ctx == nil || verifier.now == nil {
		return VerifiedOIDCToken{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return VerifiedOIDCToken{}, err
	}
	verifier.mu.RLock()
	defer verifier.mu.RUnlock()
	delegate := verifier.verifier
	expiresAt := verifier.expiresAt
	if delegate == nil || expiresAt.IsZero() || !verifier.now().Before(expiresAt) {
		return VerifiedOIDCToken{}, ErrUnavailable
	}
	return delegate.Verify(ctx, bearerToken)
}

func (*ReloadableOIDCJWTVerifier) MarshalJSON() ([]byte, error) {
	return nil, errors.New("OIDC JWT keyset verifiers cannot be serialized")
}

var _ OIDCTokenVerifier = (*ReloadableOIDCJWTVerifier)(nil)
var _ json.Marshaler = (*ReloadableOIDCJWTVerifier)(nil)
