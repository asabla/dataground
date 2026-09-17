package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	strictLocalRuntimeProfile    = "openshell-codex-strict-candidate-development/v1"
	strictLocalEnforcementDigest = "sha256:a1d56c0470c3264c4c37183352d783ebb67911d92ef2eb6ec5f7c76c61f69f39"
	localRuntimeProfile          = "openshell-codex-candidate-development/v1"
	localRuntimeVerifier         = "scripts/local-runtime-acceptance.mjs"
	localEnforcementDigest       = "sha256:d7f510e5332068cea5106de5351973dc60f15e22e970fa9352a75d3bbd32b95d"
	maximumAcceptanceOutput      = 16 << 10
)

var (
	localAcceptanceIDPattern   = regexp.MustCompile(`^rtlocal_[0-9a-z]{20,32}$`)
	localCandidateImagePattern = regexp.MustCompile(`^ghcr\.io/asabla/dataground-codex-candidate@sha256:[a-f0-9]{64}$`)
	localModelPattern          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type localRuntimeAcceptanceConfig struct {
	strict            *strictLocalRuntimeConfig
	target            runtimeCertificationTarget
	envelopeFile      string
	trustFile         string
	evidenceDirectory string
	envelopeSHA256    string
	trustSHA256       string
	sourceRevision    string
	minimumGeneration uint64
	rejectedIDs       []string
	image             string
	model             string
	nodeBinary        string
	githubBinary      string
}

func loadLocalRuntimeAcceptanceConfig(lookup environmentLookup) (*localRuntimeAcceptanceConfig, error) {
	config := &localRuntimeAcceptanceConfig{}
	for _, input := range []struct {
		name        string
		destination *string
	}{
		{"DATAGROUND_DEVELOPMENT_ISOLATION_DOMAIN_ID", &config.target.isolationDomainID},
		{"DATAGROUND_LOCAL_RUNTIME_SERVICE_ID", &config.target.serviceID},
		{"DATAGROUND_LOCAL_RUNTIME_REVISION_ID", &config.target.revisionID},
		{"DATAGROUND_LOCAL_RUNTIME_ENVELOPE", &config.envelopeFile},
		{"DATAGROUND_LOCAL_RUNTIME_TRUST", &config.trustFile},
		{"DATAGROUND_LOCAL_RUNTIME_EVIDENCE_DIRECTORY", &config.evidenceDirectory},
		{"DATAGROUND_LOCAL_RUNTIME_ENVELOPE_SHA256", &config.envelopeSHA256},
		{"DATAGROUND_LOCAL_RUNTIME_TRUST_SHA256", &config.trustSHA256},
		{"DATAGROUND_LOCAL_RUNTIME_SOURCE_REVISION", &config.sourceRevision},
		{"DATAGROUND_LOCAL_RUNTIME_IMAGE", &config.image},
		{"DATAGROUND_LOCAL_RUNTIME_MODEL", &config.model},
		{"DATAGROUND_LOCAL_RUNTIME_NODE_BINARY", &config.nodeBinary},
		{"DATAGROUND_LOCAL_RUNTIME_GITHUB_BINARY", &config.githubBinary},
	} {
		var err error
		*input.destination, err = requiredEnvironment(lookup, input.name)
		if err != nil {
			return nil, err
		}
	}
	minimum, err := requiredEnvironment(lookup, "DATAGROUND_LOCAL_RUNTIME_MINIMUM_GENERATION")
	if err != nil {
		return nil, err
	}
	config.minimumGeneration, err = strconv.ParseUint(minimum, 10, 64)
	if err != nil {
		return nil, ErrRuntimeCertificationUnavailable
	}
	if rejected, found := lookup("DATAGROUND_LOCAL_RUNTIME_REJECTED_IDS"); found {
		config.rejectedIDs = strings.Split(rejected, ",")
		slices.Sort(config.rejectedIDs)
		config.rejectedIDs = slices.Compact(config.rejectedIDs)
	}
	if profile, _ := lookup("DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE"); profile == strictLocalRuntimeProfile {
		config.strict, err = loadStrictLocalRuntimeConfig(lookup)
		if err != nil {
			return nil, err
		}
	}
	if !config.valid() {
		return nil, ErrRuntimeCertificationUnavailable
	}
	return config, nil
}

func cleanAbsoluteAcceptancePath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && value != string(filepath.Separator) && !strings.ContainsRune(value, 0)
}

func (config localRuntimeAcceptanceConfig) valid() bool {
	if (config.strict != nil && !config.strict.valid()) || !config.target.valid() || !sha256Pattern.MatchString(config.envelopeSHA256) ||
		!sha256Pattern.MatchString(config.trustSHA256) || !commitPattern.MatchString(config.sourceRevision) ||
		config.minimumGeneration == 0 || config.minimumGeneration > maximumSafeJSONInteger ||
		!localCandidateImagePattern.MatchString(config.image) || !localModelPattern.MatchString(config.model) ||
		config.envelopeFile == config.trustFile || filepath.Base(config.githubBinary) != "gh" ||
		strings.ContainsRune(config.githubBinary, os.PathListSeparator) {
		return false
	}
	for _, path := range []string{config.envelopeFile, config.trustFile, config.evidenceDirectory, config.nodeBinary, config.githubBinary} {
		if !cleanAbsoluteAcceptancePath(path) {
			return false
		}
	}
	for _, id := range config.rejectedIDs {
		if !localAcceptanceIDPattern.MatchString(id) {
			return false
		}
	}
	return true
}

func (config workerConfig) runtimeTarget() runtimeCertificationTarget {
	if config.localAcceptance != nil {
		return config.localAcceptance.target
	}
	return config.certification.target
}

type localAcceptanceCommand func(context.Context, string, []string, []string) ([]byte, error)

type localRuntimeAcceptanceChecker struct {
	config           localRuntimeAcceptanceConfig
	run              localAcceptanceCommand
	mu               sync.Mutex
	closed           bool
	deploymentFailed bool
	observation      runtimeDeploymentObservation
	observe          runtimeDeploymentObserverFactory
}

type acceptanceOutput struct{ buffer bytes.Buffer }

func (output *acceptanceOutput) Write(value []byte) (int, error) {
	if output.buffer.Len()+len(value) > maximumAcceptanceOutput {
		return 0, ErrRuntimeCertificationUnavailable
	}
	return output.buffer.Write(value)
}

func executeLocalAcceptance(ctx context.Context, binary string, arguments []string, environment []string) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Env = environment
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	var output acceptanceOutput
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return nil, ErrRuntimeCertificationUnavailable
	}
	return output.buffer.Bytes(), nil
}

func (checker *localRuntimeAcceptanceChecker) Check(ctx context.Context) error {
	if checker == nil || ctx == nil || !checker.config.valid() {
		return ErrRuntimeCertificationUnavailable
	}
	checker.mu.Lock()
	closed := checker.closed || checker.deploymentFailed
	checker.mu.Unlock()
	if closed {
		return ErrRuntimeCertificationUnavailable
	}
	config := checker.config
	arguments := []string{localRuntimeVerifier, "verify", config.envelopeFile, config.trustFile, config.evidenceDirectory, config.trustSHA256, config.sourceRevision, config.envelopeSHA256, config.target.isolationDomainID, config.target.serviceID, config.target.revisionID, strconv.FormatUint(config.minimumGeneration, 10)}
	if len(config.rejectedIDs) > 0 {
		arguments = append(arguments, strings.Join(config.rejectedIDs, ","))
	}
	// Only the deployment-selected gh directory is added to the child's search
	// path. Model, registry, database and ambient account credentials stay out.
	environment := []string{"PATH=" + filepath.Dir(config.githubBinary) + string(os.PathListSeparator) + "/usr/bin:/bin"}
	run := checker.run
	if run == nil {
		run = executeLocalAcceptance
	}
	ctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	output, err := run(ctx, config.nodeBinary, arguments, environment)
	if err != nil || len(output) == 0 || len(output) > maximumAcceptanceOutput || ctx.Err() != nil {
		return ErrRuntimeCertificationUnavailable
	}
	var receipt struct {
		AcceptanceID string `json:"acceptanceId"`
		Generation   uint64 `json:"generation"`
		Scope        struct {
			IsolationDomainID string `json:"isolationDomainId"`
			ServiceID         string `json:"serviceId"`
			RevisionID        string `json:"revisionId"`
		} `json:"scope"`
		Profile                string          `json:"profile"`
		Image                  string          `json:"image"`
		Model                  string          `json:"model"`
		ExpiresAt              string          `json:"expiresAt"`
		CertificationEligible  *bool           `json:"certificationEligible"`
		DeploymentScope        string          `json:"deploymentScope"`
		SupervisorImage        json.RawMessage `json:"supervisorImage"`
		SupervisorLocalImageID json.RawMessage `json:"supervisorLocalImageId"`
		GatewayConfigSHA256    json.RawMessage `json:"gatewayConfigSHA256"`
		EnforcementDigest      json.RawMessage `json:"enforcementDigest"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return ErrRuntimeCertificationUnavailable
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return ErrRuntimeCertificationUnavailable
	}
	expires, err := time.Parse(time.RFC3339Nano, receipt.ExpiresAt)
	if err != nil || !time.Now().Before(expires) ||
		!localAcceptanceIDPattern.MatchString(receipt.AcceptanceID) ||
		receipt.Generation < config.minimumGeneration || receipt.Generation > maximumSafeJSONInteger ||
		slices.Contains(config.rejectedIDs, receipt.AcceptanceID) ||
		receipt.Scope.IsolationDomainID != config.target.isolationDomainID ||
		receipt.Scope.ServiceID != config.target.serviceID || receipt.Scope.RevisionID != config.target.revisionID ||
		receipt.Profile != config.acceptanceProfile() || receipt.Image != config.image || receipt.Model != config.model ||
		receipt.CertificationEligible == nil || *receipt.CertificationEligible || receipt.DeploymentScope != "loopback-development-only" {
		return ErrRuntimeCertificationUnavailable
	}
	strictFields := []json.RawMessage{receipt.SupervisorImage, receipt.SupervisorLocalImageID, receipt.GatewayConfigSHA256, receipt.EnforcementDigest}
	if config.strict == nil {
		for _, field := range strictFields {
			if len(field) != 0 {
				return ErrRuntimeCertificationUnavailable
			}
		}
	} else {
		expected := []string{config.strict.supervisorImage, config.strict.topology.SupervisorLocalImageID, config.strict.topology.GatewayConfigSHA256, strictLocalEnforcementDigest}
		for index, field := range strictFields {
			var value string
			if json.Unmarshal(field, &value) != nil || value != expected[index] {
				return ErrRuntimeCertificationUnavailable
			}
		}
		if checker.checkDeployment(ctx) != nil {
			return ErrRuntimeCertificationUnavailable
		}
	}
	checker.mu.Lock()
	closed = checker.closed || checker.deploymentFailed
	checker.mu.Unlock()
	if closed || ctx.Err() != nil || !time.Now().Before(expires) {
		return ErrRuntimeCertificationUnavailable
	}
	return nil
}
