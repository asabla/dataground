package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/execution/s3store"
)

const artifactContentConfigurationEnvironment = "DATAGROUND_API_ARTIFACT_CONTENT_CONFIG_FILE"

type artifactContentConfiguration struct {
	Contract     string `json:"contract"`
	Endpoint     string `json:"endpoint"`
	Bucket       string `json:"bucket"`
	MaximumBytes int64  `json:"maximumBytes"`
}

// This development-only profile has no storage credentials. The literal
// loopback endpoint and disabled proxy prevent ambient routing authority.
func loadArtifactContentConfiguration(lookup func(string) (string, bool)) ([]api.DurableArtifactContentConfig, error) {
	path, configured := lookup(artifactContentConfigurationEnvironment)
	if !configured {
		return nil, nil
	}
	if path == "" {
		return nil, errors.New("artifact content configuration path is required")
	}
	if _, oidc := lookup("DATAGROUND_API_SECURITY_CONFIG_FILE"); oidc {
		return nil, errors.New("artifact content configuration is not part of the certified OIDC profile")
	}
	if database, ok := lookup("DATAGROUND_DATABASE_URL"); !ok || database == "" {
		return nil, errors.New("artifact content requires durable API mode")
	}
	encoded, err := readStableConfigurationFile(path, 4096)
	if err != nil {
		return nil, errors.New("artifact content configuration file is invalid")
	}
	defer clear(encoded)
	if requireUniqueConfigurationJSON(encoded) != nil {
		return nil, errors.New("artifact content configuration file is invalid")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) != nil || len(fields) != 4 {
		return nil, errors.New("artifact content configuration fields are invalid")
	}
	for _, name := range []string{"contract", "endpoint", "bucket", "maximumBytes"} {
		if value, ok := fields[name]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, errors.New("artifact content configuration fields are invalid")
		}
	}
	var config artifactContentConfiguration
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil {
		return nil, errors.New("artifact content configuration file is invalid")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return nil, errors.New("artifact content configuration file is invalid")
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil {
		return nil, errors.New("artifact content endpoint is invalid")
	}
	ip := net.ParseIP(endpoint.Hostname())
	if config.Contract != "dataground.api-artifact-content/v1" || ip == nil || !ip.IsLoopback() || config.MaximumBytes <= 0 || config.MaximumBytes > api.MaximumArtifactContentBytes {
		return nil, errors.New("artifact content requires a bounded loopback configuration")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	store, err := s3store.New(s3store.Config{Endpoint: config.Endpoint, Bucket: config.Bucket, AddressingStyle: s3store.PathStyle, AllowHTTPForLoopback: true, HTTPClient: &http.Client{Transport: transport, Timeout: 10 * time.Second}})
	if err != nil {
		return nil, errors.New("artifact content storage configuration is invalid")
	}
	objects, err := s3store.NewArtifactStore(store, config.MaximumBytes)
	if err != nil {
		return nil, err
	}
	return []api.DurableArtifactContentConfig{{Objects: objects, MaximumBytes: config.MaximumBytes}}, nil
}
