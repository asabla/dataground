package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgreSQLAuthenticationRateLimiterValidatesComposition(t *testing.T) {
	t.Parallel()

	policy := persistence.AuthenticationRateLimitPolicy{
		Window: time.Minute, GlobalBurst: 100, IsolationDomainBurst: 20, CredentialBurst: 5,
	}
	if _, err := NewPostgreSQLAuthenticationRateLimiter(nil, 1, policy); err == nil {
		t.Fatal("nil repository was accepted")
	}
	repository := persistence.NewRepository(&pgxpool.Pool{})
	if _, err := NewPostgreSQLAuthenticationRateLimiter(
		repository,
		1,
		persistence.AuthenticationRateLimitPolicy{},
	); err == nil {
		t.Fatal("invalid policy was accepted")
	}
	limiter, err := NewPostgreSQLAuthenticationRateLimiter(repository, 1, policy)
	if err != nil {
		t.Fatalf("create limiter: %v", err)
	}
	if _, err := json.Marshal(limiter); err == nil {
		t.Fatal("rate limiter serialized its repository state")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := authenticationRateLimitRequest("iso_0123456789abcdefghij", []byte("credential"))
	if _, err := limiter.AllowAuthentication(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admission error = %v", err)
	}
}
