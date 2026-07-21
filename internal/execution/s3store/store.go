// Package s3store adapts the bounded enforcement-object ports to the S3 REST
// protocol. Its operator-owned RoundTripper is the future authentication seam,
// keeping credentials and workload-identity policy outside product state.
package s3store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/asabla/dataground/internal/execution"
)

type AddressingStyle string

const (
	PathStyle          AddressingStyle = "path"
	VirtualHostedStyle AddressingStyle = "virtual-hosted"
)

var (
	bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	keyPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,1023}$`)

	errUnavailable = errors.New("s3 enforcement-object storage is unavailable")
)

// Config describes one dedicated platform-object bucket. Endpoint must be an
// origin without user information, query, fragment or path. HTTP is accepted
// only for an explicitly enabled loopback development endpoint.
type Config struct {
	Endpoint             string
	Bucket               string
	AddressingStyle      AddressingStyle
	AllowHTTPForLoopback bool
	HTTPClient           *http.Client
}

// Store implements the immutable enforcement-object read and write ports. It
// owns no credentials, bucket lifecycle, delete operation or list operation.
type Store struct {
	endpoint *url.URL
	bucket   string
	style    AddressingStyle
	client   *http.Client
}

// New validates and owns a copy of the supplied client configuration.
func New(config Config) (*Store, error) {
	endpoint, err := normalizeEndpoint(config.Endpoint, config.AllowHTTPForLoopback)
	if err != nil {
		return nil, err
	}
	if !validBucket(config.Bucket) {
		return nil, errors.New("invalid S3 enforcement-object bucket")
	}
	if config.AddressingStyle != PathStyle && config.AddressingStyle != VirtualHostedStyle {
		return nil, errors.New("invalid S3 addressing style")
	}
	if config.AddressingStyle == VirtualHostedStyle && net.ParseIP(endpoint.Hostname()) != nil {
		return nil, errors.New("virtual-hosted S3 addressing requires a DNS endpoint")
	}
	if config.HTTPClient == nil || config.HTTPClient.Transport == nil {
		return nil, errors.New("S3 enforcement-object transport is required")
	}

	copy := *config.HTTPClient
	client := &copy
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &Store{endpoint: endpoint, bucket: config.Bucket, style: config.AddressingStyle, client: client}, nil
}

func (store *Store) OpenEnforcementObject(ctx context.Context, key string) (io.ReadCloser, error) {
	objectURL, err := store.objectURL(key)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, objectURL, nil)
	if err != nil {
		return nil, errUnavailable
	}
	request.Header.Set("Accept-Encoding", "identity")

	response, err := store.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errUnavailable
	}
	if response.StatusCode == http.StatusOK {
		if response.Header.Get("Content-Encoding") != "" ||
			response.ContentLength > execution.MaximumEnforcementPolicyBytes {
			closeResponse(response)
			return nil, errUnavailable
		}
		return response.Body, nil
	}
	closeResponse(response)
	if response.StatusCode == http.StatusNotFound {
		return nil, execution.ErrEnforcementObjectMissing
	}
	return nil, errUnavailable
}

func (store *Store) PutEnforcementObjectIfAbsent(
	ctx context.Context,
	key string,
	content io.Reader,
	size int64,
	digest string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	objectURL, err := store.objectURL(key)
	if err != nil {
		return err
	}
	if content == nil || size <= 0 || size > execution.MaximumEnforcementPolicyBytes ||
		!digestPattern.MatchString(digest) {
		return execution.ErrEnforcementObjectConflict
	}
	owned, err := io.ReadAll(io.LimitReader(content, size+1))
	if err != nil || int64(len(owned)) != size || execution.VerifyEnforcementPolicy(owned, digest) != nil {
		return execution.ErrEnforcementObjectConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPut, objectURL, bytes.NewReader(owned))
	if err != nil {
		return errUnavailable
	}
	request.ContentLength = size
	request.Header.Set("Content-Type", execution.EnforcementBundleMediaType)
	request.Header.Set("If-None-Match", "*")
	checksum := sha256.Sum256(owned)
	request.Header.Set("x-amz-checksum-sha256", base64.StdEncoding.EncodeToString(checksum[:]))

	response, err := store.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errUnavailable
	}
	closeResponse(response)
	if response.StatusCode == http.StatusOK {
		return nil
	}
	if response.StatusCode == http.StatusPreconditionFailed {
		return execution.ErrEnforcementObjectConflict
	}
	return errUnavailable
}

func (store *Store) objectURL(key string) (string, error) {
	if !validKey(key) {
		return "", errors.New("invalid S3 enforcement-object key")
	}
	object := *store.endpoint
	if store.style == VirtualHostedStyle {
		host := store.bucket + "." + object.Hostname()
		if port := object.Port(); port != "" {
			host = net.JoinHostPort(host, port)
		}
		object.Host = host
		object.Path = "/" + key
	} else {
		object.Path = "/" + store.bucket + "/" + key
	}
	return object.String(), nil
}

func normalizeEndpoint(raw string, allowHTTPForLoopback bool) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil || !endpoint.IsAbs() || endpoint.Hostname() == "" || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" ||
		endpoint.Path != "" && endpoint.Path != "/" {
		return nil, errors.New("invalid S3 enforcement-object endpoint")
	}
	if endpoint.Scheme != "https" {
		if endpoint.Scheme != "http" || !allowHTTPForLoopback || !isLoopbackHost(endpoint.Hostname()) {
			return nil, errors.New("S3 enforcement-object endpoint requires HTTPS")
		}
	}
	if endpoint.Port() != "" {
		port, err := strconv.ParseUint(endpoint.Port(), 10, 16)
		if err != nil || port == 0 {
			return nil, errors.New("invalid S3 enforcement-object endpoint port")
		}
	}
	endpoint.Path = ""
	endpoint.RawPath = ""
	return endpoint, nil
}

func validBucket(bucket string) bool {
	return bucketPattern.MatchString(bucket) && !strings.Contains(bucket, "..") && net.ParseIP(bucket) == nil
}

func validKey(key string) bool {
	if !keyPattern.MatchString(key) || strings.Contains(key, "//") || strings.Contains(key, `\`) {
		return false
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return strings.HasPrefix(key, "enforcement-bundles/v1/")
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func closeResponse(response *http.Response) {
	_ = response.Body.Close()
}

var _ execution.EnforcementObjectReader = (*Store)(nil)
var _ execution.EnforcementObjectWriter = (*Store)(nil)
