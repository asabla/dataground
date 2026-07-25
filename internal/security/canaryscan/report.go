package canaryscan

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"
)

const (
	SchemaVersion               = "dataground.dev.openshell-canary-scan/v1"
	InputCommitmentDomain       = "dataground.openshell-canary-input/v1"
	MaxInputBytes         int64 = 256 << 20
)

var (
	ErrInvalidConfiguration = errors.New("invalid canary scan configuration")
	runIDPattern            = regexp.MustCompile(`^[a-f0-9]{32}$`)
	resourceNamePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	surfaceResourceKinds    = map[string]string{
		"sandbox-process":     "sandbox",
		"sandbox-environment": "sandbox",
		"sandbox-filesystem":  "sandbox",
		"provider-arguments":  "provider",
		"gateway-logs":        "gateway",
		"sandbox-logs":        "sandbox",
		"runtime-errors":      "runtime",
	}
)

type resourceBinding struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Report is an opaque content-free result. Its JSON form is owned by this
// package so callers cannot substitute scan metrics or evidence bindings.
type Report struct {
	schemaVersion    string
	surface          string
	runID            string
	resource         resourceBinding
	canaryCommitment string
	inputCommitment  string
	status           string
	matches          int64
	complete         bool
	inputLimitBytes  int64
	inspectedBytes   int64
	candidates       int64
	startedAt        string
	finishedAt       string
}

type reportJSON struct {
	SchemaVersion    string          `json:"schemaVersion"`
	Surface          string          `json:"surface"`
	RunID            string          `json:"runID"`
	Resource         resourceBinding `json:"resource"`
	CanaryCommitment string          `json:"canaryCommitment"`
	InputCommitment  string          `json:"inputCommitment"`
	Status           string          `json:"status"`
	Matches          int64           `json:"matches"`
	Complete         bool            `json:"complete"`
	InputLimitBytes  int64           `json:"inputLimitBytes"`
	InspectedBytes   int64           `json:"inspectedBytes"`
	Candidates       int64           `json:"candidates"`
	StartedAt        string          `json:"startedAt"`
	FinishedAt       string          `json:"finishedAt"`
}

type ReportConfig struct {
	Surface          string
	RunID            string
	ResourceName     string
	CanaryCommitment string
	MaxBytes         int64
}

// ValidateReportConfig checks report identity and bounds without reading a source.
func ValidateReportConfig(config ReportConfig) error {
	_, err := validateReportConfig(config)
	return err
}

// ScanReport scans one acquired byte stream and owns every field of the
// content-free report. Callers cannot substitute metrics, resource kinds,
// timestamps, status, or input commitments.
func ScanReport(ctx context.Context, input io.Reader, config ReportConfig) (Report, error) {
	return scanReport(ctx, input, config, time.Now)
}

func scanReport(
	ctx context.Context,
	input io.Reader,
	config ReportConfig,
	now func() time.Time,
) (Report, error) {
	resourceKind, err := validateReportConfig(config)
	if err != nil {
		return Report{}, err
	}

	startedAt := now().UTC()
	result, scanErr := scan(ctx, input, config.MaxBytes, config.CanaryCommitment)
	finishedAt := now().UTC()
	if finishedAt.Before(startedAt) {
		scanErr = errors.Join(scanErr, errors.New("canary scan observation clock moved backwards"))
	}

	report := Report{
		schemaVersion:    SchemaVersion,
		surface:          config.Surface,
		runID:            config.RunID,
		resource:         resourceBinding{Kind: resourceKind, Name: config.ResourceName},
		canaryCommitment: config.CanaryCommitment,
		inputCommitment:  bindInput(config, resourceKind, result.inspectedSHA256),
		status:           "clear",
		matches:          result.Matches,
		complete:         scanErr == nil,
		inputLimitBytes:  config.MaxBytes,
		inspectedBytes:   result.InspectedBytes,
		candidates:       result.Candidates,
		startedAt:        startedAt.Format(time.RFC3339Nano),
		finishedAt:       finishedAt.Format(time.RFC3339Nano),
	}
	if result.Matches > 0 {
		report.status = "matched"
	}
	if scanErr != nil {
		report.status = "incomplete"
	}
	return report, scanErr
}

// HasMatches reports whether the scanner observed the committed canary.
func (report Report) HasMatches() bool {
	return report.matches > 0
}

func (report Report) MarshalJSON() ([]byte, error) {
	return json.Marshal(reportJSON{
		SchemaVersion:    report.schemaVersion,
		Surface:          report.surface,
		RunID:            report.runID,
		Resource:         report.resource,
		CanaryCommitment: report.canaryCommitment,
		InputCommitment:  report.inputCommitment,
		Status:           report.status,
		Matches:          report.matches,
		Complete:         report.complete,
		InputLimitBytes:  report.inputLimitBytes,
		InspectedBytes:   report.inspectedBytes,
		Candidates:       report.candidates,
		StartedAt:        report.startedAt,
		FinishedAt:       report.finishedAt,
	})
}

func validateReportConfig(config ReportConfig) (string, error) {
	resourceKind, ok := surfaceResourceKinds[config.Surface]
	if !ok ||
		!runIDPattern.MatchString(config.RunID) ||
		!resourceNamePattern.MatchString(config.ResourceName) ||
		config.MaxBytes <= 0 ||
		config.MaxBytes > MaxInputBytes {
		return "", ErrInvalidConfiguration
	}
	if _, err := parseCommitment(config.CanaryCommitment); err != nil {
		return "", errors.Join(ErrInvalidConfiguration, err)
	}
	return resourceKind, nil
}

func bindInput(config ReportConfig, resourceKind string, inspectedSHA256 [sha256.Size]byte) string {
	digest := sha256.New()
	var length [4]byte
	for _, value := range []string{
		InputCommitmentDomain,
		config.RunID,
		config.Surface,
		resourceKind,
		config.ResourceName,
	} {
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(value))
	}
	_, _ = digest.Write(inspectedSHA256[:])
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}
