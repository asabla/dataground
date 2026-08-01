package authn

import (
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	maximumDPoPProofBytes          = 8 << 10
	maximumDPoPHeaderBytes         = 2 << 10
	maximumDPoPProofIDBytes        = 128
	minimumDPoPProofIDBytes        = 16
	maximumDPoPMethodBytes         = 16
	maximumDPoPURIBytes            = 2048
	maximumDPoPClockSkew           = time.Minute
	maximumDPoPProofAge            = 5 * time.Minute
	minimumDPoPProofAge            = 10 * time.Second
	maximumDPoPReservationLifetime = maximumDPoPProofAge + 2*maximumDPoPClockSkew
)

var (
	ErrDPoPProofReplayed = errors.New("DPoP proof was already used")
	dpopMethodPattern    = regexp.MustCompile(`^[A-Z]{3,16}$`)
)

type DPoPRequest struct {
	IsolationDomainID string
	Method            string
	ExternalURI       string
	Proof             []byte
}

func (request DPoPRequest) Valid() bool {
	return isolationDomainPattern.MatchString(request.IsolationDomainID) &&
		len(request.Method) <= maximumDPoPMethodBytes &&
		dpopMethodPattern.MatchString(request.Method) &&
		validDPoPExternalURI(request.ExternalURI) &&
		len(request.Proof) > 0 && len(request.Proof) <= maximumDPoPProofBytes
}

type dpopRequestKey struct{}

func WithDPoPRequest(ctx context.Context, request DPoPRequest) (context.Context, error) {
	if ctx == nil || !request.Valid() {
		return nil, errors.New("DPoP request binding is invalid")
	}
	request.Proof = append([]byte(nil), request.Proof...)
	return context.WithValue(ctx, dpopRequestKey{}, request), nil
}

type DPoPReplayReservation struct {
	IsolationDomainID   string
	KeyThumbprintDigest [sha256.Size]byte
	ProofIDDigest       [sha256.Size]byte
	ExpiresAt           time.Time
}

func (reservation DPoPReplayReservation) Valid() bool {
	if !isolationDomainPattern.MatchString(reservation.IsolationDomainID) ||
		reservation.KeyThumbprintDigest == ([sha256.Size]byte{}) ||
		reservation.ProofIDDigest == ([sha256.Size]byte{}) ||
		reservation.ExpiresAt.IsZero() {
		return false
	}
	now := time.Now()
	return reservation.ExpiresAt.After(now) &&
		!reservation.ExpiresAt.After(now.Add(maximumDPoPReservationLifetime))
}

type DPoPReplayStore interface {
	ReserveDPoPProof(context.Context, DPoPReplayReservation) error
}

type DPoPConfig struct {
	Verifier        OIDCTokenVerifier
	Replays         DPoPReplayStore
	ClockSkew       time.Duration
	MaximumProofAge time.Duration
}

type DPoPTokenVerifier struct {
	delegate        OIDCTokenVerifier
	replays         DPoPReplayStore
	clockSkew       time.Duration
	maximumProofAge time.Duration
}

func NewDPoPTokenVerifier(config DPoPConfig) (*DPoPTokenVerifier, error) {
	if nilDPoPDependency(config.Verifier) || nilDPoPDependency(config.Replays) {
		return nil, errors.New("DPoP dependencies are required")
	}
	if !validDPoPTimeBounds(config.ClockSkew, config.MaximumProofAge) {
		return nil, errors.New("DPoP time bounds are invalid")
	}
	return &DPoPTokenVerifier{
		delegate:        config.Verifier,
		replays:         config.Replays,
		clockSkew:       config.ClockSkew,
		maximumProofAge: config.MaximumProofAge,
	}, nil
}

func validDPoPTimeBounds(clockSkew, maximumProofAge time.Duration) bool {
	return clockSkew >= 0 && clockSkew <= maximumDPoPClockSkew &&
		maximumProofAge >= minimumDPoPProofAge &&
		maximumProofAge <= maximumDPoPProofAge
}

func (verifier *DPoPTokenVerifier) Verify(
	ctx context.Context,
	accessToken []byte,
) (VerifiedOIDCToken, error) {
	if verifier == nil || nilDPoPDependency(verifier.delegate) || nilDPoPDependency(verifier.replays) {
		return VerifiedOIDCToken{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return VerifiedOIDCToken{}, err
	}
	request, ok := ctx.Value(dpopRequestKey{}).(DPoPRequest)
	if !ok || !request.Valid() {
		return VerifiedOIDCToken{}, ErrInvalidCredential
	}
	verified, err := verifier.delegate.Verify(ctx, accessToken)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return VerifiedOIDCToken{}, err
		case errors.Is(err, ErrInvalidCredential):
			return VerifiedOIDCToken{}, ErrInvalidCredential
		default:
			return VerifiedOIDCToken{}, ErrUnavailable
		}
	}
	if err := ctx.Err(); err != nil {
		return VerifiedOIDCToken{}, err
	}
	if !validDPoPThumbprint(verified.ConfirmationThumbprint) {
		return VerifiedOIDCToken{}, ErrInvalidCredential
	}
	reservation, err := verifyDPoPProof(
		request,
		accessToken,
		verified.ConfirmationThumbprint,
		verifier.clockSkew,
		verifier.maximumProofAge,
	)
	if err != nil {
		return VerifiedOIDCToken{}, ErrInvalidCredential
	}
	if err := verifier.replays.ReserveDPoPProof(ctx, reservation); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return VerifiedOIDCToken{}, ctxErr
		}
		if errors.Is(err, ErrDPoPProofReplayed) {
			return VerifiedOIDCToken{}, ErrInvalidCredential
		}
		return VerifiedOIDCToken{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return VerifiedOIDCToken{}, err
	}
	return verified, nil
}

type dpopHeader struct {
	Type      string          `json:"typ"`
	Algorithm string          `json:"alg"`
	JWK       json.RawMessage `json:"jwk"`
}

type dpopClaims struct {
	ProofID     string           `json:"jti"`
	Method      string           `json:"htm"`
	URI         string           `json:"htu"`
	IssuedAt    *jwt.NumericDate `json:"iat"`
	AccessToken string           `json:"ath"`
}

func verifyDPoPProof(
	request DPoPRequest,
	accessToken []byte,
	expectedThumbprint string,
	clockSkew time.Duration,
	maximumProofAge time.Duration,
) (DPoPReplayReservation, error) {
	header, claims, err := inspectDPoPProof(request.Proof)
	if err != nil {
		return DPoPReplayReservation{}, err
	}
	key, thumbprint, err := parseDPoPPublicKey(header.JWK, header.Algorithm)
	if err != nil || thumbprint != expectedThumbprint {
		return DPoPReplayReservation{}, errors.New("DPoP key binding is invalid")
	}
	parsed, err := jwt.ParseSigned(string(request.Proof), []jose.SignatureAlgorithm{
		jose.SignatureAlgorithm(header.Algorithm),
	})
	if err != nil || len(parsed.Headers) != 1 || parsed.Headers[0].Algorithm != header.Algorithm {
		return DPoPReplayReservation{}, errors.New("DPoP signature envelope is invalid")
	}
	var signedClaims dpopClaims
	if err := parsed.Claims(key.Key, &signedClaims); err != nil || !equalDPoPClaims(signedClaims, claims) {
		return DPoPReplayReservation{}, errors.New("DPoP signature is invalid")
	}
	if claims.Method != request.Method || claims.URI != request.ExternalURI ||
		claims.IssuedAt == nil || !validDPoPProofID(claims.ProofID) ||
		!validDPoPThumbprint(claims.AccessToken) {
		return DPoPReplayReservation{}, errors.New("DPoP claims are invalid")
	}
	accessTokenDigest := sha256.Sum256(accessToken)
	if claims.AccessToken != base64.RawURLEncoding.EncodeToString(accessTokenDigest[:]) {
		return DPoPReplayReservation{}, errors.New("DPoP access token binding is invalid")
	}
	now := time.Now()
	issuedAt := claims.IssuedAt.Time()
	if issuedAt.After(now.Add(clockSkew)) || now.Sub(issuedAt) > maximumProofAge+clockSkew {
		return DPoPReplayReservation{}, errors.New("DPoP proof time is invalid")
	}
	keyDigest := sha256.Sum256([]byte(thumbprint))
	proofDigest := sha256.Sum256([]byte(claims.ProofID))
	reservation := DPoPReplayReservation{
		IsolationDomainID:   request.IsolationDomainID,
		KeyThumbprintDigest: keyDigest,
		ProofIDDigest:       proofDigest,
		ExpiresAt:           issuedAt.Add(maximumProofAge + clockSkew),
	}
	if !reservation.Valid() {
		return DPoPReplayReservation{}, errors.New("DPoP replay reservation is invalid")
	}
	return reservation, nil
}

func equalDPoPClaims(left, right dpopClaims) bool {
	if left.ProofID != right.ProofID || left.Method != right.Method || left.URI != right.URI ||
		left.AccessToken != right.AccessToken || (left.IssuedAt == nil) != (right.IssuedAt == nil) {
		return false
	}
	return left.IssuedAt == nil || left.IssuedAt.Time().Equal(right.IssuedAt.Time())
}

func inspectDPoPProof(proof []byte) (dpopHeader, dpopClaims, error) {
	if len(proof) == 0 || len(proof) > maximumDPoPProofBytes {
		return dpopHeader{}, dpopClaims{}, errors.New("DPoP proof size is invalid")
	}
	parts := bytes.Split(proof, []byte{'.'})
	if len(parts) != 3 || len(parts[0]) == 0 || len(parts[1]) == 0 || len(parts[2]) == 0 {
		return dpopHeader{}, dpopClaims{}, errors.New("DPoP compact serialization is invalid")
	}
	headerBytes, err := base64.RawURLEncoding.Strict().DecodeString(string(parts[0]))
	if err != nil || len(headerBytes) == 0 || len(headerBytes) > maximumDPoPHeaderBytes {
		return dpopHeader{}, dpopClaims{}, errors.New("DPoP protected header is invalid")
	}
	payloadBytes, err := base64.RawURLEncoding.Strict().DecodeString(string(parts[1]))
	if err != nil || len(payloadBytes) == 0 {
		return dpopHeader{}, dpopClaims{}, errors.New("DPoP payload is invalid")
	}
	if err := requireUniqueJSONObject(headerBytes); err != nil {
		return dpopHeader{}, dpopClaims{}, err
	}
	if err := requireUniqueJSONObject(payloadBytes); err != nil {
		return dpopHeader{}, dpopClaims{}, err
	}
	var header dpopHeader
	if err := decodeClosedJSONObject(headerBytes, &header); err != nil ||
		header.Type != "dpop+jwt" || !supportedDPoPAlgorithm(header.Algorithm) || len(header.JWK) == 0 {
		return dpopHeader{}, dpopClaims{}, errors.New("DPoP protected header is invalid")
	}
	var claims dpopClaims
	if err := decodeClosedJSONObject(payloadBytes, &claims); err != nil {
		return dpopHeader{}, dpopClaims{}, errors.New("DPoP claims are invalid")
	}
	return header, claims, nil
}

func parseDPoPPublicKey(raw json.RawMessage, algorithm string) (jose.JSONWebKey, string, error) {
	if err := requireUniqueJSONObject(raw); err != nil {
		return jose.JSONWebKey{}, "", errors.New("DPoP JWK is invalid")
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return jose.JSONWebKey{}, "", errors.New("DPoP JWK is invalid")
	}
	allowed := map[string]struct{}{"kty": {}, "crv": {}, "x": {}, "y": {}}
	for member := range members {
		if _, ok := allowed[member]; !ok {
			return jose.JSONWebKey{}, "", errors.New("DPoP JWK member is invalid")
		}
	}
	var key jose.JSONWebKey
	if err := json.Unmarshal(raw, &key); err != nil || !key.Valid() || !key.IsPublic() {
		return jose.JSONWebKey{}, "", errors.New("DPoP JWK is invalid")
	}
	key.Algorithm = algorithm
	if !validDPoPKey(key) {
		return jose.JSONWebKey{}, "", errors.New("DPoP JWK algorithm is invalid")
	}
	thumbprint, err := key.Thumbprint(crypto.SHA256)
	if err != nil {
		return jose.JSONWebKey{}, "", errors.New("DPoP JWK thumbprint is invalid")
	}
	return key, base64.RawURLEncoding.EncodeToString(thumbprint), nil
}

func validDPoPKey(key jose.JSONWebKey) bool {
	switch key.Algorithm {
	case "ES256":
		return validOIDCJWTKey(key)
	case "EdDSA":
		return validOIDCJWTKey(key)
	default:
		return false
	}
}

func supportedDPoPAlgorithm(algorithm string) bool {
	return algorithm == "ES256" || algorithm == "EdDSA"
}

func validDPoPProofID(value string) bool {
	return len(value) >= minimumDPoPProofIDBytes && len(value) <= maximumDPoPProofIDBytes &&
		validOIDCValue(value)
}

func validDPoPThumbprint(value string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == sha256.Size &&
		base64.RawURLEncoding.EncodeToString(decoded) == value
}

func validDPoPExternalURI(value string) bool {
	if len(value) == 0 || len(value) > maximumDPoPURIBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	hostname := parsed.Hostname()
	return parsed.String() == value && hostname != "" &&
		strings.ToLower(parsed.Host) == parsed.Host && !strings.HasSuffix(hostname, ".") &&
		parsed.Port() != "443"
}

func decodeClosedJSONObject(content []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireJSONDecoderEOF(decoder)
}

func (*DPoPTokenVerifier) MarshalJSON() ([]byte, error) {
	return nil, errors.New("DPoP token verifiers cannot be serialized")
}

func nilDPoPDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ OIDCTokenVerifier = (*DPoPTokenVerifier)(nil)
var _ json.Marshaler = (*DPoPTokenVerifier)(nil)
