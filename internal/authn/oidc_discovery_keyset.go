package authn

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

const maximumOIDCDiscoveryDocumentBytes = 64 << 10
const maximumOIDCDiscoveryResponseHeaderBytes = 32 << 10
const maximumOIDCDiscoveryBearerTokenBytes = 8 << 10

var (
	ErrOIDCDiscoveryInvalid     = errors.New("OIDC discovery metadata is invalid")
	ErrOIDCDiscoveryUnavailable = errors.New("OIDC discovery is unavailable")
)

// OIDCDiscoveryKeysetImportConfig pins the metadata and key endpoints used to
// import one provider-owned public signing-key set. Transport owns TLS trust.
type OIDCDiscoveryKeysetImportConfig struct {
	Issuer               string
	DiscoveryURL         string
	JWKSURL              string
	Algorithms           []string
	DiscoveryBearerToken []byte
	JWKSBearerToken      []byte
	Transport            *http.Transport
}

// OIDCDiscoveryKeysetImporter retrieves provider metadata and public signing
// keys once without following redirects or accepting endpoint changes
// discovered at runtime. The first import attempt consumes its copied endpoint
// credentials even when the supplied context is invalid or cancelled.
type OIDCDiscoveryKeysetImporter struct {
	issuer               string
	discoveryURL         string
	jwksURL              string
	algorithms           map[string]struct{}
	client               *http.Client
	mu                   sync.Mutex
	used                 bool
	discoveryBearerToken []byte
	jwksBearerToken      []byte
}

type oidcDiscoveryDocument struct {
	Issuer  string `json:"issuer"`
	JWKSURL string `json:"jwks_uri"`
}

func NewOIDCDiscoveryKeysetImporter(
	config OIDCDiscoveryKeysetImportConfig,
) (*OIDCDiscoveryKeysetImporter, error) {
	if !validOIDCIssuer(config.Issuer) ||
		!validPinnedOIDCHTTPEndpoint(config.DiscoveryURL) ||
		!validPinnedOIDCHTTPEndpoint(config.JWKSURL) ||
		config.DiscoveryURL == config.JWKSURL ||
		!validOIDCDiscoveryBearerToken(config.DiscoveryBearerToken) ||
		!validOIDCDiscoveryBearerToken(config.JWKSBearerToken) {
		return nil, ErrOIDCDiscoveryInvalid
	}
	_, algorithms, err := parseOIDCJWTAlgorithms(config.Algorithms)
	if err != nil {
		return nil, ErrOIDCDiscoveryInvalid
	}
	transport := config.Transport
	if transport == nil {
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, ErrOIDCDiscoveryUnavailable
		}
		transport = defaultTransport
	}
	transport = transport.Clone()
	transport.DisableCompression = true
	transport.MaxResponseHeaderBytes = maximumOIDCDiscoveryResponseHeaderBytes
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		if transport.TLSClientConfig.MinVersion == 0 ||
			transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
			transport.TLSClientConfig.MinVersion = tls.VersionTLS12
		}
	}
	return &OIDCDiscoveryKeysetImporter{
		issuer:       config.Issuer,
		discoveryURL: config.DiscoveryURL,
		jwksURL:      config.JWKSURL,
		algorithms:   algorithms,
		discoveryBearerToken: append(
			[]byte(nil), config.DiscoveryBearerToken...,
		),
		jwksBearerToken: append([]byte(nil), config.JWKSBearerToken...),
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (importer *OIDCDiscoveryKeysetImporter) Import(ctx context.Context) ([]byte, error) {
	if importer == nil || importer.client == nil {
		return nil, ErrOIDCDiscoveryUnavailable
	}
	discoveryBearerToken, jwksBearerToken, ok := importer.takeBearerTokens()
	if !ok {
		return nil, ErrOIDCDiscoveryUnavailable
	}
	defer clear(discoveryBearerToken)
	defer clear(jwksBearerToken)
	if ctx == nil {
		return nil, ErrOIDCDiscoveryUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	metadata, err := importer.fetchJSON(
		ctx,
		importer.discoveryURL,
		discoveryBearerToken,
		maximumOIDCDiscoveryDocumentBytes,
		"application/json",
	)
	if err != nil {
		return nil, err
	}
	defer clear(metadata)
	if err := requireUniqueJSONObject(metadata); err != nil {
		return nil, ErrOIDCDiscoveryInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(metadata))
	var discovery oidcDiscoveryDocument
	if err := decoder.Decode(&discovery); err != nil || requireJSONDecoderEOF(decoder) != nil ||
		discovery.Issuer != importer.issuer || discovery.JWKSURL != importer.jwksURL {
		return nil, ErrOIDCDiscoveryInvalid
	}

	jwks, err := importer.fetchJSON(
		ctx,
		importer.jwksURL,
		jwksBearerToken,
		maximumOIDCJWKSBytes,
		"application/json",
		"application/jwk-set+json",
	)
	if err != nil {
		return nil, err
	}
	defer clear(jwks)
	canonical, err := canonicalOIDCJWKS(jwks, importer.algorithms)
	if err != nil {
		return nil, ErrOIDCDiscoveryInvalid
	}
	if err := ctx.Err(); err != nil {
		clear(canonical)
		return nil, err
	}
	return canonical, nil
}

func (importer *OIDCDiscoveryKeysetImporter) fetchJSON(
	ctx context.Context,
	endpoint string,
	bearerToken []byte,
	maximumBytes int,
	mediaTypes ...string,
) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ErrOIDCDiscoveryInvalid
	}
	request.Header.Set("Accept", strings.Join(mediaTypes, ", "))
	if len(bearerToken) != 0 {
		request.Header.Set("Authorization", "Bearer "+string(bearerToken))
	}
	response, err := importer.client.Do(request)
	request.Header.Del("Authorization")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, ErrOIDCDiscoveryUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Request == nil ||
		response.Request.URL.String() != endpoint {
		return nil, ErrOIDCDiscoveryUnavailable
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !containsOIDCMediaType(mediaTypes, mediaType) {
		return nil, ErrOIDCDiscoveryInvalid
	}
	if response.ContentLength == 0 || response.ContentLength > int64(maximumBytes) {
		return nil, ErrOIDCDiscoveryInvalid
	}
	return readBoundedOIDCHTTPBody(ctx, response.Body, maximumBytes)
}

func (importer *OIDCDiscoveryKeysetImporter) takeBearerTokens() ([]byte, []byte, bool) {
	importer.mu.Lock()
	defer importer.mu.Unlock()
	if importer.used {
		return nil, nil, false
	}
	importer.used = true
	discovery := append([]byte(nil), importer.discoveryBearerToken...)
	jwks := append([]byte(nil), importer.jwksBearerToken...)
	clear(importer.discoveryBearerToken)
	clear(importer.jwksBearerToken)
	importer.discoveryBearerToken = nil
	importer.jwksBearerToken = nil
	return discovery, jwks, true
}

func validOIDCDiscoveryBearerToken(token []byte) bool {
	if len(token) > maximumOIDCDiscoveryBearerTokenBytes {
		return false
	}
	padding := false
	for _, value := range token {
		if value == '=' {
			padding = true
			continue
		}
		if padding || !((value >= 'a' && value <= 'z') ||
			(value >= 'A' && value <= 'Z') ||
			(value >= '0' && value <= '9') ||
			strings.ContainsRune("-._~+/", rune(value))) {
			return false
		}
	}
	return true
}

func readBoundedOIDCHTTPBody(ctx context.Context, reader io.Reader, maximumBytes int) ([]byte, error) {
	content := make([]byte, 0, maximumBytes)
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			clear(content)
			return nil, err
		}
		count, err := reader.Read(buffer)
		if count > 0 {
			if len(content)+count > maximumBytes {
				clear(content)
				return nil, ErrOIDCDiscoveryInvalid
			}
			content = append(content, buffer[:count]...)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || count == 0 {
			clear(content)
			return nil, ErrOIDCDiscoveryUnavailable
		}
	}
	if len(content) == 0 {
		return nil, ErrOIDCDiscoveryInvalid
	}
	if err := ctx.Err(); err != nil {
		clear(content)
		return nil, err
	}
	return content, nil
}

func validPinnedOIDCHTTPEndpoint(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.String() == value && parsed.Scheme == "https" &&
		parsed.Host != "" && parsed.Hostname() != "" && parsed.User == nil &&
		parsed.Fragment == "" && parsed.RawQuery == "" && !parsed.OmitHost && parsed.Path != ""
}

func containsOIDCMediaType(allowed []string, actual string) bool {
	for _, candidate := range allowed {
		if actual == candidate {
			return true
		}
	}
	return false
}

func (*OIDCDiscoveryKeysetImporter) MarshalJSON() ([]byte, error) {
	return nil, errors.New("OIDC discovery keyset importers cannot be serialized")
}

var _ json.Marshaler = (*OIDCDiscoveryKeysetImporter)(nil)
