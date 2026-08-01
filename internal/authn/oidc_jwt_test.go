package authn_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const testOIDCJWTKeyID = "provider-signing-key-1"

func TestPinnedOIDCJWTVerifierAcceptsBoundSignedAccessToken(t *testing.T) {
	fixture := newOIDCJWTFixture(t)
	token := fixture.sign(t, validOIDCJWTClaims(time.Now()), jose.RS256, testOIDCJWTKeyID, nil)

	verified, err := fixture.verifier.Verify(context.Background(), []byte(token))
	if err != nil {
		t.Fatalf("verify signed access token: %v", err)
	}
	if verified.Issuer != testOIDCIssuer || verified.Subject != testOIDCSubject ||
		len(verified.Audiences) != 1 || verified.Audiences[0] != testOIDCAudience {
		t.Fatalf("verified token = %#v", verified)
	}
}

func TestPinnedOIDCJWTVerifierRejectsInvalidSignatureHeaderAndClaims(t *testing.T) {
	fixture := newOIDCJWTFixture(t)
	now := time.Now()
	validClaims := validOIDCJWTClaims(now)
	otherPrivateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	wrongSignature := fixture.signWithKey(
		t,
		otherPrivateKey,
		validClaims,
		jose.RS256,
		testOIDCJWTKeyID,
		nil,
	)
	unknownKey := fixture.sign(t, validClaims, jose.RS256, "missing-key", nil)
	wrongAlgorithm := fixture.sign(t, validClaims, jose.PS256, testOIDCJWTKeyID, nil)
	extraHeader := fixture.sign(
		t,
		validClaims,
		jose.RS256,
		testOIDCJWTKeyID,
		func(options *jose.SignerOptions) {
			options.WithContentType("application/json")
		},
	)
	expiredClaims := validOIDCJWTClaims(now.Add(-10 * time.Minute))
	expiredClaims.Expiry = jwt.NewNumericDate(now.Add(-5 * time.Minute))
	futureClaims := validOIDCJWTClaims(now)
	futureClaims.NotBefore = jwt.NewNumericDate(now.Add(5 * time.Minute))
	missingExpiry := validOIDCJWTClaims(now)
	missingExpiry.Expiry = nil
	missingIssuedAt := validOIDCJWTClaims(now)
	missingIssuedAt.IssuedAt = nil
	tooLong := validOIDCJWTClaims(now)
	tooLong.Expiry = jwt.NewNumericDate(now.Add(2 * time.Hour))
	wrongIssuer := validOIDCJWTClaims(now)
	wrongIssuer.Issuer = "https://other.example.invalid"
	wrongAudience := validOIDCJWTClaims(now)
	wrongAudience.Audience = jwt.Audience{"other-api"}
	duplicateAudience := validOIDCJWTClaims(now)
	duplicateAudience.Audience = jwt.Audience{testOIDCAudience, testOIDCAudience}
	controlSubject := validOIDCJWTClaims(now)
	controlSubject.Subject = "subject\t0001"
	duplicatePayload := fixture.signPayload(t, []byte(fmt.Sprintf(
		`{"iss":%q,"sub":%q,"sub":%q,"aud":%q,"exp":%d,"iat":%d}`,
		testOIDCIssuer,
		testOIDCSubject,
		testOIDCSubject,
		testOIDCAudience,
		now.Add(5*time.Minute).Unix(),
		now.Unix(),
	)), jose.RS256, testOIDCJWTKeyID, nil)

	tests := map[string]string{
		"wrong signature":    wrongSignature,
		"unknown key":        unknownKey,
		"wrong algorithm":    wrongAlgorithm,
		"extra header":       extraHeader,
		"expired":            fixture.sign(t, expiredClaims, jose.RS256, testOIDCJWTKeyID, nil),
		"future not before":  fixture.sign(t, futureClaims, jose.RS256, testOIDCJWTKeyID, nil),
		"missing expiry":     fixture.sign(t, missingExpiry, jose.RS256, testOIDCJWTKeyID, nil),
		"missing issued at":  fixture.sign(t, missingIssuedAt, jose.RS256, testOIDCJWTKeyID, nil),
		"excess lifetime":    fixture.sign(t, tooLong, jose.RS256, testOIDCJWTKeyID, nil),
		"wrong issuer":       fixture.sign(t, wrongIssuer, jose.RS256, testOIDCJWTKeyID, nil),
		"wrong audience":     fixture.sign(t, wrongAudience, jose.RS256, testOIDCJWTKeyID, nil),
		"duplicate audience": fixture.sign(t, duplicateAudience, jose.RS256, testOIDCJWTKeyID, nil),
		"control subject":    fixture.sign(t, controlSubject, jose.RS256, testOIDCJWTKeyID, nil),
		"duplicate claim":    duplicatePayload,
		"not compact":        "not-a-compact-signed-jwt-with-at-least-thirty-two-bytes",
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.verifier.Verify(
				context.Background(),
				[]byte(token),
			); !errors.Is(err, authn.ErrInvalidCredential) {
				t.Fatalf("error = %v, want invalid credential", err)
			}
		})
	}
}

func TestPinnedOIDCJWTVerifierRejectsUnsafeProfiles(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	weakKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	validKey := oidcJWTJWK(&privateKey.PublicKey, jose.RS256, testOIDCJWTKeyID)
	privateJWK := oidcJWTJWK(privateKey, jose.RS256, testOIDCJWTKeyID)
	weakJWK := oidcJWTJWK(&weakKey.PublicKey, jose.RS256, testOIDCJWTKeyID)
	wrongAlgorithmJWK := oidcJWTJWK(&privateKey.PublicKey, jose.PS256, testOIDCJWTKeyID)
	duplicateKeys := []jose.JSONWebKey{validKey, validKey}
	validJWKS := marshalOIDCJWKS(t, []jose.JSONWebKey{validKey})
	unknownMember := append([]byte(nil), validJWKS...)
	unknownMember = []byte(string(unknownMember[:len(unknownMember)-3]) + `,"x5u":"https://keys.example.invalid/key"}]}`)

	valid := authn.PinnedOIDCJWTConfig{
		Issuer:          testOIDCIssuer,
		Audience:        testOIDCAudience,
		Algorithms:      []string{"RS256"},
		JWKS:            validJWKS,
		ClockSkew:       30 * time.Second,
		MaximumLifetime: time.Hour,
	}
	tests := map[string]authn.PinnedOIDCJWTConfig{
		"plaintext issuer": func() authn.PinnedOIDCJWTConfig {
			config := valid
			config.Issuer = "http://identity.example.invalid"
			return config
		}(),
		"wrong audience": func() authn.PinnedOIDCJWTConfig {
			config := valid
			config.Audience = "other-api"
			return config
		}(),
		"duplicate algorithm": func() authn.PinnedOIDCJWTConfig {
			config := valid
			config.Algorithms = []string{"RS256", "RS256"}
			return config
		}(),
		"symmetric algorithm": func() authn.PinnedOIDCJWTConfig {
			config := valid
			config.Algorithms = []string{"HS256"}
			return config
		}(),
		"excessive skew": func() authn.PinnedOIDCJWTConfig {
			config := valid
			config.ClockSkew = 10 * time.Minute
			return config
		}(),
		"excessive lifetime": func() authn.PinnedOIDCJWTConfig {
			config := valid
			config.MaximumLifetime = 48 * time.Hour
			return config
		}(),
		"private key": func() authn.PinnedOIDCJWTConfig {
			config := valid
			config.JWKS = marshalOIDCJWKS(t, []jose.JSONWebKey{privateJWK})
			return config
		}(),
		"weak key": func() authn.PinnedOIDCJWTConfig {
			config := valid
			config.JWKS = marshalOIDCJWKS(t, []jose.JSONWebKey{weakJWK})
			return config
		}(),
		"algorithm not allowed": func() authn.PinnedOIDCJWTConfig {
			config := valid
			config.JWKS = marshalOIDCJWKS(t, []jose.JSONWebKey{wrongAlgorithmJWK})
			return config
		}(),
		"duplicate key ID": func() authn.PinnedOIDCJWTConfig {
			config := valid
			config.JWKS = marshalOIDCJWKS(t, duplicateKeys)
			return config
		}(),
		"unsupported key member": func() authn.PinnedOIDCJWTConfig {
			config := valid
			config.JWKS = unknownMember
			return config
		}(),
		"duplicate keys member": func() authn.PinnedOIDCJWTConfig {
			config := valid
			config.JWKS = []byte(`{"keys":[],"keys":[]}`)
			return config
		}(),
	}
	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := authn.NewPinnedOIDCJWTVerifier(config); err == nil {
				t.Fatal("unsafe OIDC JWT profile was accepted")
			}
		})
	}
}

func TestPinnedOIDCJWTVerifierPreservesCancellationAndCannotSerialize(t *testing.T) {
	fixture := newOIDCJWTFixture(t)
	token := fixture.sign(t, validOIDCJWTClaims(time.Now()), jose.RS256, testOIDCJWTKeyID, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.verifier.Verify(ctx, []byte(token)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled verification = %v", err)
	}
	if _, err := json.Marshal(fixture.verifier); err == nil {
		t.Fatal("OIDC JWT verifier serialized")
	}
}

type oidcJWTFixture struct {
	privateKey *rsa.PrivateKey
	verifier   *authn.PinnedOIDCJWTVerifier
}

func newOIDCJWTFixture(t *testing.T) oidcJWTFixture {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := authn.NewPinnedOIDCJWTVerifier(authn.PinnedOIDCJWTConfig{
		Issuer:     testOIDCIssuer,
		Audience:   testOIDCAudience,
		Algorithms: []string{"RS256"},
		JWKS: marshalOIDCJWKS(t, []jose.JSONWebKey{
			oidcJWTJWK(&privateKey.PublicKey, jose.RS256, testOIDCJWTKeyID),
		}),
		ClockSkew:       30 * time.Second,
		MaximumLifetime: time.Hour,
	})
	if err != nil {
		t.Fatalf("create pinned OIDC JWT verifier: %v", err)
	}
	return oidcJWTFixture{privateKey: privateKey, verifier: verifier}
}

func (fixture oidcJWTFixture) sign(
	t *testing.T,
	claims jwt.Claims,
	algorithm jose.SignatureAlgorithm,
	keyID string,
	configure func(*jose.SignerOptions),
) string {
	t.Helper()
	return fixture.signWithKey(t, fixture.privateKey, claims, algorithm, keyID, configure)
}

func (fixture oidcJWTFixture) signWithKey(
	t *testing.T,
	privateKey *rsa.PrivateKey,
	claims jwt.Claims,
	algorithm jose.SignatureAlgorithm,
	keyID string,
	configure func(*jose.SignerOptions),
) string {
	t.Helper()
	signer := newOIDCJWTSigner(t, privateKey, algorithm, keyID, configure)
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("serialize signed JWT: %v", err)
	}
	return token
}

func (fixture oidcJWTFixture) signPayload(
	t *testing.T,
	payload []byte,
	algorithm jose.SignatureAlgorithm,
	keyID string,
	configure func(*jose.SignerOptions),
) string {
	t.Helper()
	signer := newOIDCJWTSigner(t, fixture.privateKey, algorithm, keyID, configure)
	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign JWT payload: %v", err)
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize signed JWT payload: %v", err)
	}
	return token
}

func newOIDCJWTSigner(
	t *testing.T,
	privateKey *rsa.PrivateKey,
	algorithm jose.SignatureAlgorithm,
	keyID string,
	configure func(*jose.SignerOptions),
) jose.Signer {
	t.Helper()
	options := (&jose.SignerOptions{}).WithType("at+jwt")
	if configure != nil {
		configure(options)
	}
	signer, err := jose.NewSigner(jose.SigningKey{
		Algorithm: algorithm,
		Key:       oidcJWTJWK(privateKey, algorithm, keyID),
	}, options)
	if err != nil {
		t.Fatalf("create JWT signer: %v", err)
	}
	return signer
}

func validOIDCJWTClaims(now time.Time) jwt.Claims {
	return jwt.Claims{
		Issuer:   testOIDCIssuer,
		Subject:  testOIDCSubject,
		Audience: jwt.Audience{testOIDCAudience},
		Expiry:   jwt.NewNumericDate(now.Add(5 * time.Minute)),
		IssuedAt: jwt.NewNumericDate(now),
	}
}

func oidcJWTJWK(
	key any,
	algorithm jose.SignatureAlgorithm,
	keyID string,
) jose.JSONWebKey {
	return jose.JSONWebKey{
		Key:       key,
		KeyID:     keyID,
		Algorithm: string(algorithm),
		Use:       "sig",
	}
}

func marshalOIDCJWKS(t *testing.T, keys []jose.JSONWebKey) []byte {
	t.Helper()
	content, err := json.Marshal(jose.JSONWebKeySet{Keys: keys})
	if err != nil {
		t.Fatalf("marshal OIDC JWKS: %v", err)
	}
	return content
}
