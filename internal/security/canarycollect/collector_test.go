package canarycollect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

const testCanary = "dataground-canary-v1:ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_a-b-cD"

func TestCollectOwnsCanonicalDirectHandoff(t *testing.T) {
	t.Parallel()

	order := make([]string, 0, len(surfaceOrder))
	closes := make(map[string]int, len(surfaceOrder))
	config := validConfig(func(request SourceRequest) (io.ReadCloser, error) {
		order = append(order, request.Surface)
		if request.RunID != "0123456789abcdef0123456789abcdef" ||
			request.ResourceName != resourceName(configResources(), request.Surface) {
			t.Fatalf("acquisition request = %+v", request)
		}
		return &trackedReadCloser{
			Reader: strings.NewReader("safe " + request.Surface),
			close: func() { closes[request.Surface]++ },
		}, nil
	})

	collection, err := Collect(context.Background(), config)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if strings.Join(order, ",") != strings.Join(surfaceOrder, ",") {
		t.Fatalf("Collect() order = %v", order)
	}
	for _, surface := range surfaceOrder {
		if closes[surface] != 1 {
			t.Fatalf("Collect() closes[%q] = %d", surface, closes[surface])
		}
	}

	encoded, err := json.Marshal(collection)
	if err != nil {
		t.Fatalf("marshal collection: %v", err)
	}
	var reports []map[string]any
	if err := json.Unmarshal(encoded, &reports); err != nil {
		t.Fatalf("decode collection: %v", err)
	}
	if len(reports) != len(surfaceOrder) {
		t.Fatalf("collection report count = %d", len(reports))
	}
	if bytes.Contains(encoded, []byte("safe ")) {
		t.Fatalf("collection retained source content: %s", encoded)
	}
	for index, report := range reports {
		if report["surface"] != surfaceOrder[index] || report["status"] != "clear" {
			t.Fatalf("collection report %d = %v", index, report)
		}
	}
}

func TestCollectRejectsCompletePlanDriftBeforeAcquisition(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*Config){
		"missing source": func(config *Config) {
			config.Sources = config.Sources[:len(config.Sources)-1]
		},
		"duplicate source": func(config *Config) {
			config.Sources[1].Surface = config.Sources[0].Surface
		},
		"extra limit": func(config *Config) {
			config.Limits["host-environment"] = 1024
		},
		"invalid resource": func(config *Config) {
			config.Resources.Sandbox = "Invalid Sandbox"
		},
		"invalid run": func(config *Config) {
			config.RunID = ""
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			acquisitions := 0
			config := validConfig(func(SourceRequest) (io.ReadCloser, error) {
				acquisitions++
				return io.NopCloser(strings.NewReader("safe")), nil
			})
			mutate(&config)
			collection, err := Collect(context.Background(), config)
			if !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("Collect() error = %v, want ErrInvalidConfiguration", err)
			}
			if acquisitions != 0 {
				t.Fatalf("Collect() acquired %d sources", acquisitions)
			}
			assertReportCount(t, collection, 0)
		})
	}
}

func TestCollectRejectsCancellationBeforeAcquisition(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	acquisitions := 0
	collection, err := Collect(ctx, validConfig(func(SourceRequest) (io.ReadCloser, error) {
		acquisitions++
		return io.NopCloser(strings.NewReader("safe")), nil
	}))
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrAcquisition) {
		t.Fatalf("Collect() error = %v", err)
	}
	if acquisitions != 0 {
		t.Fatalf("Collect() acquired %d sources", acquisitions)
	}
	assertReportCount(t, collection, 0)
}

func TestCollectSanitizesAcquisitionFailure(t *testing.T) {
	t.Parallel()

	acquired := 0
	config := validConfig(func(request SourceRequest) (io.ReadCloser, error) {
		acquired++
		if request.Surface == "sandbox-environment" {
			return nil, errors.New("sensitive upstream payload")
		}
		return io.NopCloser(strings.NewReader("safe")), nil
	})

	collection, err := Collect(context.Background(), config)
	if !errors.Is(err, ErrAcquisition) {
		t.Fatalf("Collect() error = %v, want ErrAcquisition", err)
	}
	if strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("Collect() leaked acquisition error: %v", err)
	}
	if acquired != 2 {
		t.Fatalf("Collect() acquisitions = %d", acquired)
	}
	assertReportCount(t, collection, 1)
}

func TestCollectRetainsIncompleteBoundReport(t *testing.T) {
	t.Parallel()

	readErr := errors.New("sensitive reader failure")
	config := validConfig(func(request SourceRequest) (io.ReadCloser, error) {
		if request.Surface == "sandbox-process" {
			return &errorReadCloser{
				Reader: &bytesErrorReader{content: []byte("partial source"), err: readErr},
			}, nil
		}
		return io.NopCloser(strings.NewReader("safe")), nil
	})

	collection, err := Collect(context.Background(), config)
	if !errors.Is(err, ErrScan) {
		t.Fatalf("Collect() error = %v, want ErrScan", err)
	}
	if strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("Collect() leaked scan error: %v", err)
	}
	if _, marshalErr := json.Marshal(collection); !errors.Is(marshalErr, ErrCollectionIncomplete) {
		t.Fatalf("marshal partial collection error = %v", marshalErr)
	}
	if len(collection.reports) != 1 {
		t.Fatalf("partial collection report count = %d", len(collection.reports))
	}
	encoded, marshalErr := json.Marshal(collection.reports[0])
	if marshalErr != nil {
		t.Fatalf("marshal partial report: %v", marshalErr)
	}
	var report map[string]any
	if unmarshalErr := json.Unmarshal(encoded, &report); unmarshalErr != nil {
		t.Fatalf("decode partial report: %v", unmarshalErr)
	}
	if report["status"] != "incomplete" {
		t.Fatalf("partial report = %v", report)
	}
	if bytes.Contains(encoded, []byte("partial source")) {
		t.Fatalf("partial report retained source content: %s", encoded)
	}
}

func TestCollectFailsOnCanaryAndCloseUncertainty(t *testing.T) {
	t.Parallel()

	for name, opener := range map[string]func(SourceRequest) (io.ReadCloser, error){
		"canary": func(request SourceRequest) (io.ReadCloser, error) {
			content := "safe"
			if request.Surface == "sandbox-process" {
				content = testCanary
			}
			return io.NopCloser(strings.NewReader(content)), nil
		},
		"close": func(SourceRequest) (io.ReadCloser, error) {
			return &errorReadCloser{
				Reader:   strings.NewReader("safe"),
				closeErr: errors.New("sensitive close failure"),
			}, nil
		},
	} {
		name, opener := name, opener
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			collection, err := Collect(context.Background(), validConfig(opener))
			if name == "canary" && !errors.Is(err, ErrCanaryDetected) {
				t.Fatalf("Collect() error = %v, want ErrCanaryDetected", err)
			}
			if name == "close" && !errors.Is(err, ErrSourceClose) {
				t.Fatalf("Collect() error = %v, want ErrSourceClose", err)
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("Collect() leaked close error: %v", err)
			}
			assertReportCount(t, collection, 1)
		})
	}
}

func validConfig(opener func(SourceRequest) (io.ReadCloser, error)) Config {
	sources := make([]Source, 0, len(surfaceOrder))
	limits := make(map[string]int64, len(surfaceOrder))
	for _, surface := range surfaceOrder {
		surface := surface
		sources = append(sources, Source{
			Surface: surface,
			Acquire: func(_ context.Context, request SourceRequest) (io.ReadCloser, error) {
				return opener(request)
			},
		})
		limits[surface] = 1024
	}
	digest := sha256.Sum256([]byte(testCanary))
	return Config{
		RunID:            "0123456789abcdef0123456789abcdef",
		CanaryCommitment: "sha256:" + hex.EncodeToString(digest[:]),
		Resources: configResources(),
		Limits:  limits,
		Sources: sources,
	}
}

func configResources() ResourceNames {
	return ResourceNames{
		Gateway:  "dataground-gateway",
		Sandbox:  "sandbox-credential-check",
		Provider: "provider-credential-check",
		Runtime:  "runtime-invocation",
	}
}

func assertReportCount(t *testing.T, collection Collection, want int) {
	t.Helper()

	if len(collection.reports) != want {
		t.Fatalf("collection report count = %d, want %d", len(collection.reports), want)
	}
	if !collection.complete {
		if _, err := json.Marshal(collection); !errors.Is(err, ErrCollectionIncomplete) {
			t.Fatalf("marshal incomplete collection error = %v", err)
		}
	}
}

type trackedReadCloser struct {
	io.Reader
	close func()
}

func (source *trackedReadCloser) Close() error {
	source.close()
	return nil
}

type errorReadCloser struct {
	io.Reader
	closeErr error
}

func (source *errorReadCloser) Close() error {
	return source.closeErr
}

type bytesErrorReader struct {
	content []byte
	err     error
	read    bool
}

func (reader *bytesErrorReader) Read(buffer []byte) (int, error) {
	if reader.read {
		return 0, io.EOF
	}
	reader.read = true
	return copy(buffer, reader.content), reader.err
}
