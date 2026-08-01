package authn_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	testDPoPMethod = "POST"
	testDPoPURI    = "https://api.example.invalid/v1/isolation-domains/iso_00000000000000000001/agent-services"
)

func TestDPoPTokenVerifierBindsAndReservesProof(t *testing.T) {
	t.Parallel()
	fixture := newDPoPFixture(t)
	replays := &recordingDPoPReplayStore{}
	verifier := newDPoPVerifier(t, fixture.delegate(), replays)
	ctx := fixture.context(t, testDPoPMethod, testDPoPURI, fixture.proof(t, testDPoPMethod, testDPoPURI, time.Now(), "proof-id-00000000000001", fixture.accessToken))

	verified, err := verifier.Verify(ctx, fixture.accessToken)
	if err != nil {
		t.Fatalf("verify DPoP-bound token: %v", err)
	}
	if verified.ConfirmationThumbprint != fixture.thumbprint || len(replays.reservations) != 1 {
		t.Fatalf("verified = %#v, reservations = %#v", verified, replays.reservations)
	}
	reservation := replays.reservations[0]
	if !reservation.Valid() || reservation.IsolationDomainID != testDomain ||
		reservation.KeyThumbprintDigest == ([sha256.Size]byte{}) ||
		reservation.ProofIDDigest == ([sha256.Size]byte{}) {
		t.Fatalf("reservation = %#v", reservation)
	}
}

func TestDPoPTokenVerifierRejectsUnboundMalformedAndReplayedProofs(t *testing.T) {
	t.Parallel()
	fixture := newDPoPFixture(t)
	now := time.Now()
	validProof := fixture.proof(t, testDPoPMethod, testDPoPURI, now, "proof-id-00000000000002", fixture.accessToken)
	otherToken := []byte("another-signed-access-token-with-at-least-thirty-two-bytes")
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherThumbprint := dpopThumbprint(t, &otherKey.PublicKey)

	tests := map[string]struct {
		delegate authn.VerifiedOIDCToken
		proof    []byte
		method   string
		uri      string
		replay   error
	}{
		"missing confirmation": {delegate: fixture.verified(""), proof: validProof, method: testDPoPMethod, uri: testDPoPURI},
		"wrong confirmation":   {delegate: fixture.verified(otherThumbprint), proof: validProof, method: testDPoPMethod, uri: testDPoPURI},
		"wrong method":         {delegate: fixture.verified(fixture.thumbprint), proof: validProof, method: "GET", uri: testDPoPURI},
		"wrong URI":            {delegate: fixture.verified(fixture.thumbprint), proof: validProof, method: testDPoPMethod, uri: "https://api.example.invalid/other"},
		"wrong access token":   {delegate: fixture.verified(fixture.thumbprint), proof: fixture.proof(t, testDPoPMethod, testDPoPURI, now, "proof-id-00000000000003", otherToken), method: testDPoPMethod, uri: testDPoPURI},
		"expired proof":        {delegate: fixture.verified(fixture.thumbprint), proof: fixture.proof(t, testDPoPMethod, testDPoPURI, now.Add(-10*time.Minute), "proof-id-00000000000004", fixture.accessToken), method: testDPoPMethod, uri: testDPoPURI},
		"future proof":         {delegate: fixture.verified(fixture.thumbprint), proof: fixture.proof(t, testDPoPMethod, testDPoPURI, now.Add(5*time.Minute), "proof-id-00000000000006", fixture.accessToken), method: testDPoPMethod, uri: testDPoPURI},
		"corrupted signature":  {delegate: fixture.verified(fixture.thumbprint), proof: corruptDPoPProof(validProof), method: testDPoPMethod, uri: testDPoPURI},
		"replayed proof":       {delegate: fixture.verified(fixture.thumbprint), proof: validProof, method: testDPoPMethod, uri: testDPoPURI, replay: authn.ErrDPoPProofReplayed},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := &recordingDPoPReplayStore{err: test.replay}
			verifier := newDPoPVerifier(t, &staticOIDCVerifier{token: test.delegate}, store)
			ctx := fixture.context(t, test.method, test.uri, test.proof)
			if _, err := verifier.Verify(ctx, fixture.accessToken); !errors.Is(err, authn.ErrInvalidCredential) {
				t.Fatalf("error = %v, want invalid credential", err)
			}
		})
	}
}

func TestDPoPRequestRejectsUntrustedExternalURIForms(t *testing.T) {
	t.Parallel()
	fixture := newDPoPFixture(t)
	proof := fixture.proof(t, testDPoPMethod, testDPoPURI, time.Now(), "proof-id-00000000000007", fixture.accessToken)
	for name, uri := range map[string]string{
		"plaintext":    "http://api.example.invalid/resource",
		"userinfo":     "https://user@api.example.invalid/resource",
		"uppercase":    "https://API.example.invalid/resource",
		"default port": "https://api.example.invalid:443/resource",
		"trailing dot": "https://api.example.invalid./resource",
		"query":        "https://api.example.invalid/resource?scope=other",
		"fragment":     "https://api.example.invalid/resource#other",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := authn.WithDPoPRequest(context.Background(), authn.DPoPRequest{
				IsolationDomainID: testDomain, Method: testDPoPMethod, ExternalURI: uri, Proof: proof,
			}); err == nil {
				t.Fatal("untrusted external URI was accepted")
			}
		})
	}
}

func TestDPoPTokenVerifierFailsClosedOnMissingScopeAndStoreFailure(t *testing.T) {
	t.Parallel()
	fixture := newDPoPFixture(t)
	proof := fixture.proof(t, testDPoPMethod, testDPoPURI, time.Now(), "proof-id-00000000000005", fixture.accessToken)

	verifier := newDPoPVerifier(t, fixture.delegate(), &recordingDPoPReplayStore{})
	if _, err := verifier.Verify(context.Background(), fixture.accessToken); !errors.Is(err, authn.ErrInvalidCredential) {
		t.Fatalf("missing-scope error = %v", err)
	}
	verifier = newDPoPVerifier(t, fixture.delegate(), &recordingDPoPReplayStore{err: errors.New("private database detail")})
	if _, err := verifier.Verify(
		fixture.context(t, testDPoPMethod, testDPoPURI, proof),
		fixture.accessToken,
	); !errors.Is(err, authn.ErrUnavailable) {
		t.Fatalf("store error = %v, want unavailable", err)
	}
}

func TestDPoPTokenVerifierRejectsIncompleteAssemblyAndSerialization(t *testing.T) {
	t.Parallel()
	var delegate *staticOIDCVerifier
	if _, err := authn.NewDPoPTokenVerifier(authn.DPoPConfig{
		Verifier: delegate, Replays: &recordingDPoPReplayStore{},
		MaximumProofAge: time.Minute,
	}); err == nil {
		t.Fatal("typed-nil verifier was accepted")
	}
	var replays *recordingDPoPReplayStore
	if _, err := authn.NewDPoPTokenVerifier(authn.DPoPConfig{
		Verifier: &staticOIDCVerifier{}, Replays: replays,
		MaximumProofAge: time.Minute,
	}); err == nil {
		t.Fatal("typed-nil replay store was accepted")
	}
	verifier := newDPoPVerifier(t, &staticOIDCVerifier{}, &recordingDPoPReplayStore{})
	if _, err := json.Marshal(verifier); err == nil {
		t.Fatal("DPoP verifier serialized")
	}
}

type dpopFixture struct {
	privateKey  *ecdsa.PrivateKey
	thumbprint  string
	accessToken []byte
}

func newDPoPFixture(t *testing.T) dpopFixture {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return dpopFixture{
		privateKey:  privateKey,
		thumbprint:  dpopThumbprint(t, &privateKey.PublicKey),
		accessToken: []byte("signed-oidc-access-token-with-at-least-thirty-two-bytes"),
	}
}

func (fixture dpopFixture) verified(thumbprint string) authn.VerifiedOIDCToken {
	return authn.VerifiedOIDCToken{
		Issuer: testOIDCIssuer, Subject: testOIDCSubject,
		Audiences: []string{testOIDCAudience}, ConfirmationThumbprint: thumbprint,
	}
}

func (fixture dpopFixture) delegate() authn.OIDCTokenVerifier {
	return &staticOIDCVerifier{token: fixture.verified(fixture.thumbprint)}
}

func (fixture dpopFixture) context(t *testing.T, method, uri string, proof []byte) context.Context {
	t.Helper()
	ctx, err := authn.WithDPoPRequest(context.Background(), authn.DPoPRequest{
		IsolationDomainID: testDomain, Method: method, ExternalURI: uri, Proof: proof,
	})
	if err != nil {
		t.Fatalf("create DPoP request context: %v", err)
	}
	return ctx
}

func (fixture dpopFixture) proof(
	t *testing.T,
	method string,
	uri string,
	issuedAt time.Time,
	proofID string,
	accessToken []byte,
) []byte {
	t.Helper()
	publicJWK := jose.JSONWebKey{Key: &fixture.privateKey.PublicKey}
	options := (&jose.SignerOptions{}).WithType("dpop+jwt").WithHeader("jwk", publicJWK)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: fixture.privateKey}, options)
	if err != nil {
		t.Fatalf("create DPoP signer: %v", err)
	}
	digest := sha256.Sum256(accessToken)
	token, err := jwt.Signed(signer).Claims(map[string]any{
		"jti": proofID,
		"htm": method,
		"htu": uri,
		"iat": issuedAt.Unix(),
		"ath": base64.RawURLEncoding.EncodeToString(digest[:]),
	}).Serialize()
	if err != nil {
		t.Fatalf("serialize DPoP proof: %v", err)
	}
	return []byte(token)
}

func dpopThumbprint(t *testing.T, key *ecdsa.PublicKey) string {
	t.Helper()
	thumbprint, err := (jose.JSONWebKey{Key: key}).Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatalf("compute DPoP thumbprint: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(thumbprint)
}

func corruptDPoPProof(proof []byte) []byte {
	corrupted := append([]byte(nil), proof...)
	if corrupted[len(corrupted)-1] == 'A' {
		corrupted[len(corrupted)-1] = 'B'
	} else {
		corrupted[len(corrupted)-1] = 'A'
	}
	return corrupted
}

func newDPoPVerifier(
	t *testing.T,
	delegate authn.OIDCTokenVerifier,
	replays authn.DPoPReplayStore,
) *authn.DPoPTokenVerifier {
	t.Helper()
	verifier, err := authn.NewDPoPTokenVerifier(authn.DPoPConfig{
		Verifier: delegate, Replays: replays,
		ClockSkew: 30 * time.Second, MaximumProofAge: time.Minute,
	})
	if err != nil {
		t.Fatalf("create DPoP verifier: %v", err)
	}
	return verifier
}

type staticOIDCVerifier struct {
	token authn.VerifiedOIDCToken
	err   error
}

func (verifier *staticOIDCVerifier) Verify(
	_ context.Context,
	_ []byte,
) (authn.VerifiedOIDCToken, error) {
	return verifier.token, verifier.err
}

type recordingDPoPReplayStore struct {
	reservations []authn.DPoPReplayReservation
	err          error
}

func (store *recordingDPoPReplayStore) ReserveDPoPProof(
	_ context.Context,
	reservation authn.DPoPReplayReservation,
) error {
	store.reservations = append(store.reservations, reservation)
	return store.err
}

var _ authn.OIDCTokenVerifier = (*staticOIDCVerifier)(nil)
var _ authn.DPoPReplayStore = (*recordingDPoPReplayStore)(nil)
