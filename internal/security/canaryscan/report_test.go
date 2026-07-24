package canaryscan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"
)

const reportTestRunID = "0123456789abcdef0123456789abcdef"

func TestScanReportOwnsCompleteEvidenceShape(t *testing.T) {
	t.Parallel()

	input := []byte("safe material")
	report, err := ScanReport(context.Background(), bytes.NewReader(input), reportConfig(
		"sandbox-environment",
		"sandbox-credential-check",
		int64(len(input)),
	))
	if err != nil {
		t.Fatalf("ScanReport() error = %v", err)
	}
	if report.SchemaVersion != SchemaVersion ||
		report.Surface != "sandbox-environment" ||
		report.RunID != reportTestRunID ||
		report.Resource != (ResourceBinding{Kind: "sandbox", Name: "sandbox-credential-check"}) ||
		report.CanaryCommitment != commitment(testCanary) ||
		report.InputCommitment != bindInput(
			reportConfig("sandbox-environment", "sandbox-credential-check", int64(len(input))),
			"sandbox",
			sha256.Sum256(input),
		) ||
		report.Status != "clear" ||
		!report.Complete ||
		report.Matches != 0 ||
		report.InputLimitBytes != int64(len(input)) ||
		report.InspectedBytes != int64(len(input)) ||
		report.Candidates != 0 ||
		report.StartedAt != "2026-07-24T12:00:00Z" ||
		report.FinishedAt != "2026-07-24T12:00:01Z" {
		t.Fatalf("ScanReport() report = %+v", report)
	}
}

func TestScanReportOwnsMatchedAndIncompleteStatus(t *testing.T) {
	t.Parallel()

	matched, err := ScanReport(
		context.Background(),
		strings.NewReader(testCanary),
		reportConfig("gateway-logs", "dataground-gateway", 1024),
	)
	if err != nil {
		t.Fatalf("ScanReport() matched error = %v", err)
	}
	if matched.Status != "matched" || !matched.Complete || matched.Matches != 1 {
		t.Fatalf("ScanReport() matched report = %+v", matched)
	}

	truncated, err := ScanReport(
		context.Background(),
		strings.NewReader("four"),
		reportConfig("runtime-errors", "runtime-invocation", 3),
	)
	if !errors.Is(err, ErrInputLimit) {
		t.Fatalf("ScanReport() truncated error = %v, want ErrInputLimit", err)
	}
	if truncated.Status != "incomplete" ||
		truncated.Complete ||
		truncated.Resource.Kind != "runtime" ||
		truncated.InspectedBytes != 4 {
		t.Fatalf("ScanReport() truncated report = %+v", truncated)
	}
}

func TestScanReportRejectsClockRegression(t *testing.T) {
	t.Parallel()

	config := reportConfig("sandbox-process", "sandbox-credential-check", 1024)
	config.Now = reportClock(
		time.Date(2026, 7, 24, 12, 0, 1, 0, time.UTC),
		time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
	)
	report, err := ScanReport(context.Background(), strings.NewReader("safe"), config)
	if err == nil {
		t.Fatal("ScanReport() accepted clock regression")
	}
	if report.Status != "incomplete" || report.Complete {
		t.Fatalf("ScanReport() report = %+v", report)
	}
}

func TestScanReportDerivesEveryResourceKind(t *testing.T) {
	t.Parallel()

	for surface, kind := range map[string]string{
		"sandbox-process":     "sandbox",
		"sandbox-environment": "sandbox",
		"sandbox-filesystem":  "sandbox",
		"provider-arguments":  "provider",
		"gateway-logs":        "gateway",
		"sandbox-logs":        "sandbox",
		"runtime-errors":      "runtime",
	} {
		surface, kind := surface, kind
		t.Run(surface, func(t *testing.T) {
			t.Parallel()

			report, err := ScanReport(
				context.Background(),
				strings.NewReader(""),
				reportConfig(surface, "checked-resource", 1024),
			)
			if err != nil {
				t.Fatalf("ScanReport() error = %v", err)
			}
			if report.Resource != (ResourceBinding{Kind: kind, Name: "checked-resource"}) {
				t.Fatalf("ScanReport() resource = %+v", report.Resource)
			}
		})
	}
}

func TestScanReportRejectsInvalidConfigurationBeforeReading(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*ReportConfig){
		"unknown surface": func(config *ReportConfig) {
			config.Surface = "host-environment"
		},
		"missing run id": func(config *ReportConfig) {
			config.RunID = ""
		},
		"uppercase run id": func(config *ReportConfig) {
			config.RunID = strings.ToUpper(config.RunID)
		},
		"non-portable resource": func(config *ReportConfig) {
			config.ResourceName = "Checked Resource"
		},
		"malformed commitment": func(config *ReportConfig) {
			config.CanaryCommitment = "sha256:00"
		},
		"zero limit": func(config *ReportConfig) {
			config.MaxBytes = 0
		},
		"oversized limit": func(config *ReportConfig) {
			config.MaxBytes = MaxInputBytes + 1
		},
		"missing clock": func(config *ReportConfig) {
			config.Now = nil
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			input := &countingReader{}
			config := reportConfig("sandbox-process", "checked-resource", 1024)
			mutate(&config)
			report, err := ScanReport(context.Background(), input, config)
			if !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("ScanReport() error = %v, want ErrInvalidConfiguration", err)
			}
			if report != (Report{}) || input.reads != 0 {
				t.Fatalf("ScanReport() read invalid input or emitted report: %+v, reads=%d", report, input.reads)
			}
		})
	}
}

func TestBindInputSeparatesEveryContext(t *testing.T) {
	t.Parallel()

	config := reportConfig("sandbox-process", "sandbox-credential-check", 1024)
	inputSHA256 := sha256.Sum256([]byte("safe material"))
	base := bindInput(config, "sandbox", inputSHA256)
	otherInputSHA256 := sha256.Sum256([]byte("different material"))
	for name, commitment := range map[string]string{
		"run": func() string {
			changed := config
			changed.RunID = "fedcba9876543210fedcba9876543210"
			return bindInput(changed, "sandbox", inputSHA256)
		}(),
		"surface": func() string {
			changed := config
			changed.Surface = "sandbox-environment"
			return bindInput(changed, "sandbox", inputSHA256)
		}(),
		"resource kind": bindInput(config, "runtime", inputSHA256),
		"resource name": func() string {
			changed := config
			changed.ResourceName = "other-sandbox"
			return bindInput(changed, "sandbox", inputSHA256)
		}(),
		"input": bindInput(config, "sandbox", otherInputSHA256),
	} {
		if commitment == base {
			t.Fatalf("%s did not separate input commitment contexts", name)
		}
	}
}

func reportConfig(surface, resourceName string, maxBytes int64) ReportConfig {
	return ReportConfig{
		Surface:          surface,
		RunID:            reportTestRunID,
		ResourceName:     resourceName,
		CanaryCommitment: commitment(testCanary),
		MaxBytes:         maxBytes,
		Now: reportClock(
			time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
			time.Date(2026, 7, 24, 12, 0, 1, 0, time.UTC),
		),
	}
}

func reportClock(values ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		value := values[index]
		if index < len(values)-1 {
			index++
		}
		return value
	}
}

type countingReader struct {
	reads int
}

func (reader *countingReader) Read([]byte) (int, error) {
	reader.reads++
	return 0, nil
}
