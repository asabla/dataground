package authn

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	maximumOIDCJWKSBytes     = 256 << 10
	maximumOIDCJWKSKeys      = 64
	maximumOIDCClockSkew     = 5 * time.Minute
	maximumOIDCTokenLifetime = 24 * time.Hour
	minimumOIDCTokenLifetime = time.Minute
	maximumOIDCHeaderBytes   = 2 << 10
)

type PinnedOIDCJWTConfig struct {
	Issuer          string
	Audience        string
	Algorithms      []string
	JWKS            []byte
	ClockSkew       time.Duration
	MaximumLifetime time.Duration
}

type PinnedOIDCJWTVerifier struct {
	issuer          string
	audience        string
	algorithms      []jose.SignatureAlgorithm
	keys            map[string]jose.JSONWebKey
	clockSkew       time.Duration
	maximumLifetime time.Duration
}

func NewPinnedOIDCJWTVerifier(config PinnedOIDCJWTConfig) (*PinnedOIDCJWTVerifier, error) {
	if !validOIDCIssuer(config.Issuer) {
		return nil, errors.New("OIDC JWT issuer is invalid")
	}
	if config.Audience != APIAudience {
		return nil, errors.New("OIDC JWT audience is invalid")
	}
	if config.ClockSkew < 0 || config.ClockSkew > maximumOIDCClockSkew {
		return nil, errors.New("OIDC JWT clock skew is invalid")
	}
	if config.MaximumLifetime < minimumOIDCTokenLifetime ||
		config.MaximumLifetime > maximumOIDCTokenLifetime {
		return nil, errors.New("OIDC JWT maximum lifetime is invalid")
	}
	algorithms, algorithmSet, err := parseOIDCJWTAlgorithms(config.Algorithms)
	if err != nil {
		return nil, err
	}
	keys, err := parseOIDCJWKS(config.JWKS, algorithmSet)
	if err != nil {
		return nil, err
	}
	return &PinnedOIDCJWTVerifier{
		issuer:          config.Issuer,
		audience:        config.Audience,
		algorithms:      append([]jose.SignatureAlgorithm(nil), algorithms...),
		keys:            keys,
		clockSkew:       config.ClockSkew,
		maximumLifetime: config.MaximumLifetime,
	}, nil
}

func (verifier *PinnedOIDCJWTVerifier) Verify(
	ctx context.Context,
	bearerToken []byte,
) (VerifiedOIDCToken, error) {
	if verifier == nil || len(verifier.keys) == 0 || len(verifier.algorithms) == 0 {
		return VerifiedOIDCToken{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return VerifiedOIDCToken{}, err
	}
	if len(bearerToken) < minimumBearerTokenBytes || len(bearerToken) > maximumBearerTokenBytes {
		return VerifiedOIDCToken{}, ErrInvalidCredential
	}
	header, err := inspectCompactOIDCJWT(bearerToken)
	if err != nil {
		return VerifiedOIDCToken{}, ErrInvalidCredential
	}
	key, exists := verifier.keys[header.KeyID]
	if !exists || key.Algorithm != header.Algorithm {
		return VerifiedOIDCToken{}, ErrInvalidCredential
	}
	parsed, err := jwt.ParseSigned(string(bearerToken), verifier.algorithms)
	if err != nil || len(parsed.Headers) != 1 || parsed.Headers[0].KeyID != header.KeyID ||
		parsed.Headers[0].Algorithm != header.Algorithm {
		return VerifiedOIDCToken{}, ErrInvalidCredential
	}
	var claims jwt.Claims
	if err := parsed.Claims(key.Key, &claims); err != nil {
		return VerifiedOIDCToken{}, ErrInvalidCredential
	}
	if err := ctx.Err(); err != nil {
		return VerifiedOIDCToken{}, err
	}
	now := time.Now()
	if claims.Expiry == nil || claims.IssuedAt == nil ||
		!validPinnedOIDCJWTClaims(claims, verifier.issuer, verifier.audience) ||
		claims.ValidateWithLeeway(jwt.Expected{
			Issuer:      verifier.issuer,
			AnyAudience: jwt.Audience{verifier.audience},
			Time:        now,
		}, verifier.clockSkew) != nil {
		return VerifiedOIDCToken{}, ErrInvalidCredential
	}
	issuedAt := claims.IssuedAt.Time()
	expiresAt := claims.Expiry.Time()
	if !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > verifier.maximumLifetime ||
		(claims.NotBefore != nil && !expiresAt.After(claims.NotBefore.Time())) {
		return VerifiedOIDCToken{}, ErrInvalidCredential
	}
	return VerifiedOIDCToken{
		Issuer:    claims.Issuer,
		Subject:   claims.Subject,
		Audiences: append([]string(nil), claims.Audience...),
	}, nil
}

func validPinnedOIDCJWTClaims(claims jwt.Claims, issuer string, audience string) bool {
	if claims.Issuer != issuer || !validOIDCValue(claims.Subject) ||
		len(claims.Audience) == 0 || len(claims.Audience) > maximumOIDCAudiences {
		return false
	}
	seen := make(map[string]struct{}, len(claims.Audience))
	foundAudience := false
	for _, value := range claims.Audience {
		if !validOIDCValue(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
		if value == audience {
			foundAudience = true
		}
	}
	return foundAudience
}

type compactOIDCJWTHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Type      string `json:"typ,omitempty"`
}

func inspectCompactOIDCJWT(token []byte) (compactOIDCJWTHeader, error) {
	parts := bytes.Split(token, []byte{'.'})
	if len(parts) != 3 || len(parts[0]) == 0 || len(parts[1]) == 0 || len(parts[2]) == 0 {
		return compactOIDCJWTHeader{}, errors.New("OIDC JWT compact serialization is invalid")
	}
	headerBytes, err := base64.RawURLEncoding.Strict().DecodeString(string(parts[0]))
	if err != nil || len(headerBytes) == 0 || len(headerBytes) > maximumOIDCHeaderBytes {
		return compactOIDCJWTHeader{}, errors.New("OIDC JWT protected header is invalid")
	}
	payloadBytes, err := base64.RawURLEncoding.Strict().DecodeString(string(parts[1]))
	if err != nil || len(payloadBytes) == 0 {
		return compactOIDCJWTHeader{}, errors.New("OIDC JWT payload is invalid")
	}
	if err := requireUniqueJSONObject(headerBytes); err != nil {
		return compactOIDCJWTHeader{}, err
	}
	if err := requireUniqueJSONObject(payloadBytes); err != nil {
		return compactOIDCJWTHeader{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(headerBytes))
	decoder.DisallowUnknownFields()
	var header compactOIDCJWTHeader
	if err := decoder.Decode(&header); err != nil {
		return compactOIDCJWTHeader{}, fmt.Errorf("decode OIDC JWT header: %w", err)
	}
	if err := requireJSONDecoderEOF(decoder); err != nil {
		return compactOIDCJWTHeader{}, err
	}
	if !validOIDCValue(header.KeyID) || !validOIDCValue(header.Algorithm) ||
		(header.Type != "" && header.Type != "at+jwt" && header.Type != "JWT") {
		return compactOIDCJWTHeader{}, errors.New("OIDC JWT header values are invalid")
	}
	return header, nil
}

func parseOIDCJWTAlgorithms(values []string) (
	[]jose.SignatureAlgorithm,
	map[string]struct{},
	error,
) {
	if len(values) == 0 || len(values) > 8 {
		return nil, nil, errors.New("OIDC JWT algorithms are invalid")
	}
	algorithms := make([]jose.SignatureAlgorithm, 0, len(values))
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !supportedOIDCJWTAlgorithm(value) {
			return nil, nil, errors.New("OIDC JWT algorithm is unsupported")
		}
		if _, duplicate := set[value]; duplicate {
			return nil, nil, errors.New("OIDC JWT algorithms contain a duplicate")
		}
		set[value] = struct{}{}
		algorithms = append(algorithms, jose.SignatureAlgorithm(value))
	}
	return algorithms, set, nil
}

func supportedOIDCJWTAlgorithm(value string) bool {
	switch value {
	case "RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA":
		return true
	default:
		return false
	}
}

type rawOIDCJWKS struct {
	Keys []json.RawMessage `json:"keys"`
}

func parseOIDCJWKS(content []byte, algorithms map[string]struct{}) (map[string]jose.JSONWebKey, error) {
	if len(content) == 0 || len(content) > maximumOIDCJWKSBytes {
		return nil, errors.New("OIDC JWKS size is invalid")
	}
	if err := requireUniqueJSONObject(content); err != nil {
		return nil, errors.New("OIDC JWKS JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var document rawOIDCJWKS
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("OIDC JWKS document is invalid")
	}
	if err := requireJSONDecoderEOF(decoder); err != nil {
		return nil, errors.New("OIDC JWKS document is invalid")
	}
	if len(document.Keys) == 0 || len(document.Keys) > maximumOIDCJWKSKeys {
		return nil, errors.New("OIDC JWKS key count is invalid")
	}
	keys := make(map[string]jose.JSONWebKey, len(document.Keys))
	for _, rawKey := range document.Keys {
		if err := requireAllowedOIDCJWKMembers(rawKey); err != nil {
			return nil, err
		}
		var key jose.JSONWebKey
		if err := json.Unmarshal(rawKey, &key); err != nil || !key.Valid() {
			return nil, errors.New("OIDC JWK is invalid")
		}
		if !validOIDCValue(key.KeyID) || key.Use != "sig" {
			return nil, errors.New("OIDC JWK metadata is invalid")
		}
		if _, allowed := algorithms[key.Algorithm]; !allowed || !validOIDCJWTKey(key) {
			return nil, errors.New("OIDC JWK algorithm is invalid")
		}
		if _, duplicate := keys[key.KeyID]; duplicate {
			return nil, errors.New("OIDC JWKS contains a duplicate key ID")
		}
		keys[key.KeyID] = key
	}
	return keys, nil
}

func requireAllowedOIDCJWKMembers(rawKey []byte) error {
	if err := requireUniqueJSONObject(rawKey); err != nil {
		return errors.New("OIDC JWK JSON is invalid")
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(rawKey, &members); err != nil {
		return errors.New("OIDC JWK JSON is invalid")
	}
	for member := range members {
		switch member {
		case "kty", "use", "kid", "alg", "n", "e", "crv", "x", "y":
		default:
			return errors.New("OIDC JWK contains an unsupported member")
		}
	}
	return nil
}

func validOIDCJWTKey(key jose.JSONWebKey) bool {
	switch publicKey := key.Key.(type) {
	case *rsa.PublicKey:
		return (strings.HasPrefix(key.Algorithm, "RS") || strings.HasPrefix(key.Algorithm, "PS")) &&
			publicKey.N.BitLen() >= 2048 && publicKey.N.BitLen() <= 8192 &&
			publicKey.E >= 65537 && publicKey.E%2 == 1
	case *ecdsa.PublicKey:
		return oidcECDSACurveForAlgorithm(key.Algorithm) == publicKey.Curve
	case ed25519.PublicKey:
		return key.Algorithm == "EdDSA" && len(publicKey) == ed25519.PublicKeySize
	default:
		return false
	}
}

func oidcECDSACurveForAlgorithm(algorithm string) elliptic.Curve {
	switch algorithm {
	case "ES256":
		return elliptic.P256()
	case "ES384":
		return elliptic.P384()
	case "ES512":
		return elliptic.P521()
	default:
		return nil
	}
}

func requireUniqueJSONObject(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return errors.New("JSON value must be an object")
	}
	if err := requireUniqueJSONContainer(decoder, '}'); err != nil {
		return err
	}
	return requireJSONDecoderEOF(decoder)
}

func requireUniqueJSONContainer(decoder *json.Decoder, closing json.Delim) error {
	seen := map[string]struct{}{}
	for decoder.More() {
		if closing == '}' {
			memberToken, err := decoder.Token()
			if err != nil {
				return err
			}
			member, ok := memberToken.(string)
			if !ok {
				return errors.New("JSON object member is invalid")
			}
			if _, duplicate := seen[member]; duplicate {
				return errors.New("JSON object contains a duplicate member")
			}
			seen[member] = struct{}{}
		}
		value, err := decoder.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := value.(json.Delim); ok {
			switch delimiter {
			case '{':
				if err := requireUniqueJSONContainer(decoder, '}'); err != nil {
					return err
				}
			case '[':
				if err := requireUniqueJSONContainer(decoder, ']'); err != nil {
					return err
				}
			default:
				return errors.New("JSON delimiter is invalid")
			}
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := end.(json.Delim); !ok || delimiter != closing {
		return errors.New("JSON container is not closed")
	}
	return nil
}

func requireJSONDecoderEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("JSON input contains trailing data")
}

func (*PinnedOIDCJWTVerifier) MarshalJSON() ([]byte, error) {
	return nil, errors.New("OIDC JWT verifiers cannot be serialized")
}

var _ OIDCTokenVerifier = (*PinnedOIDCJWTVerifier)(nil)
var _ json.Marshaler = (*PinnedOIDCJWTVerifier)(nil)
