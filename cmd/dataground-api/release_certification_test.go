package main

import (
	"context"
	"testing"
	"time"
)

func TestOIDCReleaseCertificationExpiryIsIrreversible(t *testing.T) {
	t.Parallel()
	observed := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	certification := &oidcReleaseCertification{
		expiresAt: observed.Add(time.Minute),
		now:       func() time.Time { return observed },
	}
	if err := certification.Ready(context.Background()); err != nil {
		t.Fatalf("unexpired certification readiness: %v", err)
	}
	observed = observed.Add(2 * time.Minute)
	if err := certification.Ready(context.Background()); err == nil {
		t.Fatal("expired certification remained ready")
	}
	observed = observed.Add(-2 * time.Minute)
	if err := certification.Ready(context.Background()); err == nil {
		t.Fatal("clock rollback restored expired certification readiness")
	}
}
