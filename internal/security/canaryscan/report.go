package canaryscan

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"time"
)

const (
	SchemaVersion          = "dataground.dev.openshell-canary-scan/v1"
	InputCommitmentDomain  = "dataground.openshell-canary-input/v1"
	MaxInputBytes    int64 = 256 << 20
)

var (
	ErrInvalidConfiguration = errors.New("invalid canary scan configuration")
	runIDPattern             = regexp.MustCompile(`^[a-f0-9]{32}$`)
	resourceNamePattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	surfaceResourceKinds     = map[string]string{
		"sandbox-process":     "sandbox",
		"sandbox-environment": "sandbox",
		"sandbox-filesystem":  "sandbox",
		"provider-arguments":  "provider",
		"gateway-logs":        "gateway",
		"sandbox-logs":        "sandbox",
		"runtime-errors":      "runtime",
	}
)

type ResourceBinding struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Report is the complete content-free result retained as credential evidence.
type Report struct {
	SchemaVersion   string          `json:"schemaVersion"`
	Surface         string          `json:"surface"`
	RunID           string          `json:"runID"`
	Resource        ResourceBinding `json:"resource"`
	CanaryCommitment string         `json:"canaryCommitment"`
	InputCommitment string          `json:"inputCommitment"`
	Status          string          `json:"status"`
	Matches         int64           `json:"matches"`
	Complete        bool            `json:"complete"`
	InputLimitBytes int64           `json:"inputLimitBytes"`
	InspectedBytes  int64           `json:"inspectedBytes"`
	Candidates      int64           `json:"candidates"`
	StartedAt       string          `json:"startedAt"`
	FinishedAt      string          `json:"finishedAt"`
}

type ReportConfig struct {
	Surface          string
	RunID            string
	ResourceName     string
	CanaryCommitment string
	MaxBytes         int64
	Now              func() time.Time
}

// ScanReport scans one acquired byte stream and owns every field of the
// content-free report. Callers cannot substitute metrics, resource kinds,
// timestamps, status, or input commitments.
func ScanReport(ctx context.Context, input io.Reader, config ReportConfig) (Report, error) {
	resourceKind, err := validateReportConfig(config)
	if err != nil {
		return Report{}, err
	}

	startedAt := config.Now().UTC()
	result, scanErr := scan(ctx, input, config.MaxBytes, config.CanaryCommitment)
	finishedAt := config.Now().UTC()
	if finishedAt.Before(startedAt) {
		scanErr = errors.Join(scanErr, errors.New("canary scan observation clock moved backwards"))
	}

	report := Report{
		SchemaVersion:    SchemaVersion,
		Surface:          config.Surface,
		RunID:            config.RunID,
		Resource:         ResourceBinding{Kind: resourceKind, Name: config.ResourceName},
		CanaryCommitment: config.CanaryCommitment,
		InputCommitment:  bindInput(config, resourceKind, result.inspectedSHA256),
		Status:           "clear",
		Matches:          result.Matches,
		Complete:         scanErr == nil,
		InputLimitBytes:  config.MaxBytes,
		InspectedBytes:   result.InspectedBytes,
		Candidates:       result.Candidates,
		StartedAt:        startedAt.Format(time.RFC3339Nano),
		FinishedAt:       finishedAt.Format(time.RFC3339Nano),
	}
	if result.Matches > 0 {
		report.Status = "matched"
	}
	if scanErr != nil {
		report.Status = "incomplete"
	}
	return report, scanErr
}

func validateReportConfig(config ReportConfig) (string, error) {
	resourceKind, ok := surfaceResourceKinds[config.Surface]
	if !ok ||
		!runIDPattern.MatchString(config.RunID) ||
		!resourceNamePattern.MatchString(config.ResourceName) ||
		config.MaxBytes <= 0 ||
		config.MaxBytes > MaxInputBytes ||
		config.Now == nil {
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
