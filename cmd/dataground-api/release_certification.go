package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"sync/atomic"
	"time"

	"github.com/asabla/dataground/internal/releasecert"
)

type oidcReleaseCertification struct {
	expiresAt time.Time
	now       func() time.Time
	expired   atomic.Bool
}

func loadOIDCReleaseCertification(
	configurationPath string,
	configurationBytes []byte,
	certificationPath string,
	trustProfilePath string,
	sourceRevision string,
	goVersion string,
	now time.Time,
) (*oidcReleaseCertification, error) {
	if len(configurationBytes) == 0 || now.IsZero() {
		return nil, errors.New("OIDC release certification request is invalid")
	}
	verification, err := releasecert.VerifyOIDCLoopbackFile(
		certificationPath,
		trustProfilePath,
		sourceRevision,
		goVersion,
		now,
	)
	if err != nil {
		return nil, errors.New("OIDC release certification is invalid")
	}
	envelope := verification.Envelope
	var configurationArtifact releasecert.Artifact
	for _, artifact := range envelope.Statement.Artifacts {
		if artifact.Kind == "oidc-security-configuration" {
			configurationArtifact = artifact
			break
		}
	}
	expectedDigest, decodeErr := hex.DecodeString(configurationArtifact.SHA256)
	digest := sha256.Sum256(configurationBytes)
	if configurationArtifact.File != configurationPath || decodeErr != nil ||
		len(expectedDigest) != sha256.Size ||
		subtle.ConstantTimeCompare(digest[:], expectedDigest) != 1 {
		return nil, errors.New("OIDC release certification does not bind this configuration")
	}
	releaseExpiresAt, releaseErr := time.Parse(time.RFC3339Nano, envelope.Statement.ExpiresAt)
	providerExpiresAt, providerErr := time.Parse(
		time.RFC3339Nano,
		verification.ProviderDPoPIssuance.Statement.ExpiresAt,
	)
	if releaseErr != nil || providerErr != nil ||
		!releaseExpiresAt.After(now) || !providerExpiresAt.After(now) {
		return nil, errors.New("OIDC release certification validity is invalid")
	}
	expiresAt := releaseExpiresAt
	if providerExpiresAt.Before(expiresAt) {
		expiresAt = providerExpiresAt
	}
	return &oidcReleaseCertification{expiresAt: expiresAt, now: time.Now}, nil
}

func (certification *oidcReleaseCertification) Ready(ctx context.Context) error {
	if certification == nil || ctx == nil || certification.now == nil || certification.expiresAt.IsZero() {
		return errors.New("OIDC release certification is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if certification.expired.Load() {
		return errors.New("OIDC release certification has expired")
	}
	if !certification.now().UTC().Before(certification.expiresAt) {
		certification.expired.Store(true)
		return errors.New("OIDC release certification has expired")
	}
	return nil
}
