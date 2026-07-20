package rosetta

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

var (
	ErrUnavailable      = errors.New("Rosetta is unavailable")
	ErrRejected         = errors.New("Rosetta rejected materialization")
	ErrUnauthorized     = errors.New("Rosetta transport identity was rejected")
	ErrProtocol         = errors.New("Rosetta protocol response is invalid")
	ErrIncompatible     = errors.New("Rosetta version or target contract is incompatible")
	ErrRequestTooLarge  = errors.New("Rosetta materialization request is too large")
	ErrResponseTooLarge = errors.New("Rosetta response is too large")
)

type Config struct {
	Endpoint                string
	ExpectedCompilerVersion string
	ExpectedTargetContract  string
	AllowInsecureLoopback   bool
}

// Client is a strict adapter for Rosetta's v1 HTTP contract. Authentication,
// mTLS, and workload identity belong in the supplied HTTP transport; the
// client never accepts a bearer token or provider credential.
type Client struct {
	endpoint               *url.URL
	httpClient             *http.Client
	expectedCompiler       string
	expectedTargetContract string
}

func New(config Config, transportClient *http.Client) (*Client, error) {
	if transportClient == nil {
		return nil, errors.New("Rosetta HTTP client is required")
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("Rosetta endpoint is invalid")
	}
	if endpoint.Path != "" && endpoint.Path != "/" {
		return nil, errors.New("Rosetta endpoint must not include a path")
	}
	if endpoint.Scheme != "https" && !(config.AllowInsecureLoopback && endpoint.Scheme == "http" && isLoopbackHost(endpoint.Hostname())) {
		return nil, errors.New("Rosetta endpoint must use HTTPS")
	}
	if config.ExpectedCompilerVersion == "" || config.ExpectedTargetContract == "" {
		return nil, errors.New("Rosetta compiler and target contract versions are required")
	}
	clientCopy := *transportClient
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/")
	return &Client{
		endpoint: endpoint, httpClient: &clientCopy,
		expectedCompiler: config.ExpectedCompilerVersion, expectedTargetContract: config.ExpectedTargetContract,
	}, nil
}

func (client *Client) VerifyCompatibility(ctx context.Context) (Compatibility, error) {
	var response capabilitiesResponse
	if err := client.doJSON(ctx, http.MethodGet, "/v1/capabilities", nil, &response); err != nil {
		return Compatibility{}, err
	}
	if response.Version != client.expectedCompiler {
		return Compatibility{}, ErrIncompatible
	}
	if countValue(response.Targets, TargetOpenShell) != 1 {
		return Compatibility{}, ErrIncompatible
	}
	for _, required := range []string{"authorize", "compile", "schema-validation", "deterministic-artifacts"} {
		if !slices.Contains(response.Capabilities, required) {
			return Compatibility{}, ErrIncompatible
		}
	}
	var compatible *targetContractInfo
	for _, contract := range response.TargetContracts {
		if contract.Target != TargetOpenShell {
			continue
		}
		if contract.Version != client.expectedTargetContract || contract.Maturity != "supported" {
			return Compatibility{}, ErrIncompatible
		}
		if compatible != nil {
			return Compatibility{}, ErrIncompatible
		}
		candidate := contract
		compatible = &candidate
	}
	if compatible == nil {
		return Compatibility{}, ErrIncompatible
	}
	return Compatibility{
		CompilerVersion: response.Version,
		TargetContract:  compatible.Version,
		TargetMaturity:  compatible.Maturity,
	}, nil
}

func countValue(values []string, expected string) int {
	count := 0
	for _, value := range values {
		if value == expected {
			count++
		}
	}
	return count
}

func (client *Client) Materialize(ctx context.Context, request MaterializeRequest) (Materialization, error) {
	if err := validateMaterializeRequest(request); err != nil {
		return Materialization{}, err
	}
	wireRequest := compileRequest{
		Source: request.CedarSource, Target: TargetOpenShell, Mode: ModeStrict,
		Catalog: request.Catalog, Options: targetOptions{OpenShell: request.OpenShell}, Context: request.Context,
	}
	var response compileResponse
	if err := client.doJSON(ctx, http.MethodPost, "/v1/compile", wireRequest, &response); err != nil {
		return Materialization{}, err
	}
	return validateCompileResponse(wireRequest, response, client.expectedCompiler, client.expectedTargetContract)
}

func (client *Client) doJSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return ErrProtocol
		}
		if len(encoded) > maximumRequestBytes {
			return ErrRequestTooLarge
		}
		body = bytes.NewReader(encoded)
	}
	requestURL := *client.endpoint
	requestURL.Path = path
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return ErrProtocol
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		switch response.StatusCode {
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			return ErrRejected
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrUnauthorized
		case http.StatusRequestTimeout, http.StatusTooManyRequests,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return ErrUnavailable
		default:
			if response.StatusCode >= 500 {
				return ErrUnavailable
			}
			return ErrProtocol
		}
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return ErrProtocol
	}
	limited := io.LimitReader(response.Body, maximumResponseBytes+1)
	encoded, err := io.ReadAll(limited)
	if err != nil {
		return ErrUnavailable
	}
	if len(encoded) > maximumResponseBytes {
		return ErrResponseTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return ErrProtocol
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrProtocol
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func safeFieldError(field string) error {
	return fmt.Errorf("%w: invalid %s", ErrRejected, field)
}
