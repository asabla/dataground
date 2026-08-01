package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/authn"
)

const (
	maximumDPoPHeaderBytes         = 8 << 10
	maximumExternalOriginBytes     = 512
	maximumExternalRequestURIBytes = 2048
)

type DPoPRequestBinder struct {
	externalOrigin string
}

func NewDPoPRequestBinder(externalOrigin string) (*DPoPRequestBinder, error) {
	if !validExternalOrigin(externalOrigin) {
		return nil, errors.New("DPoP external origin is invalid")
	}
	return &DPoPRequestBinder{externalOrigin: externalOrigin}, nil
}

func (binder *DPoPRequestBinder) bind(request *http.Request, isolationDomainID string) (context.Context, error) {
	if binder == nil || !validExternalOrigin(binder.externalOrigin) || request == nil {
		return nil, errors.New("DPoP request binder is unavailable")
	}
	proof, err := takeDPoPProof(request.Header)
	if err != nil {
		return nil, err
	}
	externalPath, err := canonicalExternalRequestPath(request.URL)
	if err != nil {
		clear(proof)
		return nil, err
	}
	externalURI := binder.externalOrigin + externalPath
	if len(externalURI) > maximumExternalRequestURIBytes {
		clear(proof)
		return nil, errors.New("DPoP external request URI is too large")
	}
	ctx, err := authn.WithDPoPRequest(request.Context(), authn.DPoPRequest{
		IsolationDomainID: isolationDomainID,
		Method:            request.Method,
		ExternalURI:       externalURI,
		Proof:             proof,
	})
	clear(proof)
	if err != nil {
		return nil, errors.New("DPoP request binding is invalid")
	}
	return ctx, nil
}

func takeDPoPProof(header http.Header) ([]byte, error) {
	if header == nil {
		return nil, errors.New("DPoP proof is missing")
	}
	values := header.Values("DPoP")
	header.Del("DPoP")
	if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > maximumDPoPHeaderBytes {
		return nil, errors.New("DPoP proof header is invalid")
	}
	value := values[0]
	if strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return nil, errors.New("DPoP proof header is invalid")
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return nil, errors.New("DPoP proof header is invalid")
		}
	}
	return []byte(value), nil
}

func canonicalExternalRequestPath(requestURL *url.URL) (string, error) {
	if requestURL == nil || requestURL.IsAbs() || requestURL.Opaque != "" ||
		requestURL.Host != "" || requestURL.User != nil || requestURL.Fragment != "" {
		return "", errors.New("DPoP request target is invalid")
	}
	if requestURL.Path == "" || !strings.HasPrefix(requestURL.Path, "/") ||
		path.Clean(requestURL.Path) != requestURL.Path ||
		(requestURL.Path != "/" && strings.HasSuffix(requestURL.Path, "/")) ||
		strings.Contains(requestURL.Path, "//") {
		return "", errors.New("DPoP request path is not canonical")
	}
	canonical := (&url.URL{Path: requestURL.Path}).EscapedPath()
	if requestURL.RawPath != "" {
		decoded, err := url.PathUnescape(requestURL.RawPath)
		if err != nil || decoded != requestURL.Path || requestURL.EscapedPath() != canonical {
			return "", errors.New("DPoP request path encoding is not canonical")
		}
	}
	if len(canonical) == 0 || len(canonical) > maximumExternalRequestURIBytes || !utf8.ValidString(requestURL.Path) {
		return "", errors.New("DPoP request path is invalid")
	}
	for _, character := range requestURL.Path {
		if unicode.IsControl(character) {
			return "", errors.New("DPoP request path is invalid")
		}
	}
	return canonical, nil
}

func validExternalOrigin(value string) bool {
	if value == "" || len(value) > maximumExternalOriginBytes ||
		strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.Opaque != "" || parsed.Path != "" ||
		parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || parsed.String() != value {
		return false
	}
	hostname := parsed.Hostname()
	if hostname == "" || strings.HasSuffix(hostname, ".") ||
		parsed.Host != strings.ToLower(parsed.Host) || parsed.Port() == "443" {
		return false
	}
	return validExternalHostname(hostname)
}

func validExternalHostname(hostname string) bool {
	if address := net.ParseIP(hostname); address != nil {
		return true
	}
	if len(hostname) > 253 || strings.Contains(hostname, "..") {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 ||
			label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}
