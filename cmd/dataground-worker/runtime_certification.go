package main

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

const (
	governedCertificationProfile = "openshell-codex-development/v1"
	runtimeCertificationVerifier = "scripts/check-openshell-runtime-certification.mjs"
	maximumSafeJSONInteger       = uint64(9007199254740991)
)

var (
	ErrRuntimeCertificationUnavailable   = errors.New("runtime certification is unavailable")
	ErrRuntimeCertificationScopeMismatch = errors.New("runtime certification scope does not match the governed worker")

	isolationDomainIDPattern = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
	serviceIDPattern         = regexp.MustCompile(`^svc_[0-9a-z]{20,32}$`)
	revisionIDPattern        = regexp.MustCompile(`^rev_[0-9a-z]{20,32}$`)
	certificationIDPattern   = regexp.MustCompile(`^rtcert_[0-9a-z]{20,32}$`)
	sha256Pattern            = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern            = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type runtimeCertificationTarget struct {
	isolationDomainID string
	serviceID         string
	revisionID        string
}

func (target runtimeCertificationTarget) valid() bool {
	return isolationDomainIDPattern.MatchString(target.isolationDomainID) &&
		serviceIDPattern.MatchString(target.serviceID) &&
		revisionIDPattern.MatchString(target.revisionID)
}

type runtimeCertificationConfig struct {
	target                   runtimeCertificationTarget
	manifestFile             string
	evidenceFile             string
	acceptanceFile           string
	manifestSHA256           string
	sourceRevision           string
	minimumGeneration        uint64
	rejectedCertificationIDs []string
}

func loadRuntimeCertificationConfig(lookup environmentLookup) (runtimeCertificationConfig, error) {
	var config runtimeCertificationConfig
	var err error
	if config.target.isolationDomainID, err = requiredEnvironment(
		lookup,
		"DATAGROUND_DEVELOPMENT_ISOLATION_DOMAIN_ID",
	); err != nil {
		return runtimeCertificationConfig{}, err
	}
	if config.target.serviceID, err = requiredEnvironment(lookup, "DATAGROUND_CERTIFIED_SERVICE_ID"); err != nil {
		return runtimeCertificationConfig{}, err
	}
	if config.target.revisionID, err = requiredEnvironment(lookup, "DATAGROUND_CERTIFIED_REVISION_ID"); err != nil {
		return runtimeCertificationConfig{}, err
	}
	if !config.target.valid() {
		return runtimeCertificationConfig{}, errors.New("runtime certification target is invalid")
	}
	inputs := []struct {
		name        string
		destination *string
	}{
		{name: "DATAGROUND_RUNTIME_CERTIFICATION_MANIFEST", destination: &config.manifestFile},
		{name: "DATAGROUND_RUNTIME_CONFORMANCE_EVIDENCE", destination: &config.evidenceFile},
		{name: "DATAGROUND_RUNTIME_CONFORMANCE_ACCEPTANCE", destination: &config.acceptanceFile},
		{name: "DATAGROUND_RUNTIME_CERTIFICATION_SHA256", destination: &config.manifestSHA256},
		{name: "DATAGROUND_RUNTIME_CERTIFICATION_SOURCE_REVISION", destination: &config.sourceRevision},
	}
	for _, input := range inputs {
		*input.destination, err = requiredEnvironment(lookup, input.name)
		if err != nil {
			return runtimeCertificationConfig{}, err
		}
	}
	if !validRepositoryFile(config.manifestFile) ||
		!validRepositoryFile(config.evidenceFile) ||
		!validRepositoryFile(config.acceptanceFile) ||
		config.manifestFile == config.evidenceFile ||
		config.manifestFile == config.acceptanceFile ||
		config.evidenceFile == config.acceptanceFile {
		return runtimeCertificationConfig{}, errors.New(
			"runtime certification inputs must be distinct clean repository files",
		)
	}
	if !sha256Pattern.MatchString(config.manifestSHA256) {
		return runtimeCertificationConfig{}, errors.New("runtime certification digest is invalid")
	}
	if !commitPattern.MatchString(config.sourceRevision) {
		return runtimeCertificationConfig{}, errors.New("runtime certification source revision is invalid")
	}
	minimumGeneration, err := requiredEnvironment(
		lookup,
		"DATAGROUND_RUNTIME_CERTIFICATION_MINIMUM_GENERATION",
	)
	if err != nil {
		return runtimeCertificationConfig{}, err
	}
	config.minimumGeneration, err = strconv.ParseUint(minimumGeneration, 10, 64)
	if err != nil || config.minimumGeneration == 0 || config.minimumGeneration > maximumSafeJSONInteger {
		return runtimeCertificationConfig{}, errors.New(
			"runtime certification minimum generation must be a safe positive integer",
		)
	}
	if rejected, found := lookup("DATAGROUND_RUNTIME_CERTIFICATION_REJECTED_IDS"); found {
		if rejected == "" {
			return runtimeCertificationConfig{}, errors.New(
				"DATAGROUND_RUNTIME_CERTIFICATION_REJECTED_IDS must be omitted or non-empty",
			)
		}
		config.rejectedCertificationIDs = strings.Split(rejected, ",")
		for _, certificationID := range config.rejectedCertificationIDs {
			if !certificationIDPattern.MatchString(certificationID) {
				return runtimeCertificationConfig{}, errors.New(
					"runtime certification rejected identifier is invalid",
				)
			}
		}
		slices.Sort(config.rejectedCertificationIDs)
		config.rejectedCertificationIDs = slices.Compact(config.rejectedCertificationIDs)
	}
	return config, nil
}

func validRepositoryFile(value string) bool {
	return value != "" &&
		!filepath.IsAbs(value) &&
		filepath.Clean(value) == value &&
		filepath.ToSlash(value) == value &&
		value != "." &&
		value != ".." &&
		!strings.HasPrefix(value, "../")
}

type runtimeCertificationReadiness interface {
	Check(context.Context) error
}

type runtimeCertificationCommand func(context.Context, string, ...string) error

type nodeRuntimeCertificationVerifier struct {
	run runtimeCertificationCommand
}

func (verifier nodeRuntimeCertificationVerifier) Verify(
	ctx context.Context,
	config runtimeCertificationConfig,
) error {
	run := verifier.run
	if run == nil {
		run = func(ctx context.Context, name string, arguments ...string) error {
			command := exec.CommandContext(ctx, name, arguments...)
			command.Env = []string{}
			command.Stdout = io.Discard
			command.Stderr = io.Discard
			return command.Run()
		}
	}
	arguments := []string{
		runtimeCertificationVerifier,
		config.manifestFile,
		config.evidenceFile,
		config.acceptanceFile,
		"--isolation-domain", config.target.isolationDomainID,
		"--service", config.target.serviceID,
		"--revision", config.target.revisionID,
		"--runtime-profile", governedCertificationProfile,
		"--source-revision", config.sourceRevision,
		"--expected-manifest-sha256", config.manifestSHA256,
		"--minimum-generation", strconv.FormatUint(config.minimumGeneration, 10),
	}
	for _, certificationID := range config.rejectedCertificationIDs {
		arguments = append(arguments, "--reject-certification-id", certificationID)
	}
	return run(ctx, "node", arguments...)
}

type runtimeCertificationChecker struct {
	config   runtimeCertificationConfig
	verifier nodeRuntimeCertificationVerifier
}

func newRuntimeCertificationChecker(
	config runtimeCertificationConfig,
	verifier nodeRuntimeCertificationVerifier,
) (*runtimeCertificationChecker, error) {
	if !config.target.valid() {
		return nil, ErrRuntimeCertificationUnavailable
	}
	return &runtimeCertificationChecker{config: config, verifier: verifier}, nil
}

func (checker *runtimeCertificationChecker) Check(ctx context.Context) error {
	if checker == nil || ctx == nil {
		return ErrRuntimeCertificationUnavailable
	}
	if err := checker.verifier.Verify(ctx, checker.config); err != nil {
		return errors.Join(ErrRuntimeCertificationUnavailable, err)
	}
	return nil
}

type governedInvocationAuthorizer interface {
	reconcile.InvocationAdmissionAuthorizer
	reconcile.InvocationRuntimeAuthorizer
	reconcile.InvocationCancellationAuthorizer
}

type certifiedInvocationAuthorizer struct {
	delegate  governedInvocationAuthorizer
	readiness runtimeCertificationReadiness
	target    runtimeCertificationTarget
}

func (authorizer *certifiedInvocationAuthorizer) check(
	ctx context.Context,
	isolationDomainID string,
	serviceID string,
	revisionID string,
) error {
	if authorizer == nil || authorizer.delegate == nil || authorizer.readiness == nil ||
		isolationDomainID != authorizer.target.isolationDomainID ||
		serviceID != authorizer.target.serviceID ||
		revisionID != authorizer.target.revisionID {
		return errors.Join(reconcile.ErrEffectInvalid, ErrRuntimeCertificationScopeMismatch)
	}
	return authorizer.readiness.Check(ctx)
}

func (authorizer *certifiedInvocationAuthorizer) AuthorizeInvocationAdmission(
	ctx context.Context,
	target persistence.InvocationAdmissionTarget,
) error {
	if err := authorizer.check(ctx, target.IsolationDomainID, target.ServiceID, target.RevisionID); err != nil {
		return err
	}
	return authorizer.delegate.AuthorizeInvocationAdmission(ctx, target)
}

func (authorizer *certifiedInvocationAuthorizer) AuthorizeInvocationRuntime(
	ctx context.Context,
	target persistence.InvocationRuntimeTarget,
	request dgruntime.StartRequest,
) error {
	if err := authorizer.check(ctx, target.IsolationDomainID, target.ServiceID, target.RevisionID); err != nil {
		return err
	}
	return authorizer.delegate.AuthorizeInvocationRuntime(ctx, target, request)
}

func (authorizer *certifiedInvocationAuthorizer) AuthorizeInvocationCancellation(
	ctx context.Context,
	target persistence.InvocationCancellationTarget,
) error {
	if err := authorizer.check(ctx, target.IsolationDomainID, target.ServiceID, target.RevisionID); err != nil {
		return err
	}
	return authorizer.delegate.AuthorizeInvocationCancellation(ctx, target)
}

type certifiedExecutionProvider struct {
	execution.ExecutionProvider
	readiness runtimeCertificationReadiness
	target    runtimeCertificationTarget
}

func (provider *certifiedExecutionProvider) check(ctx context.Context, isolationDomainID string) error {
	if provider == nil || provider.ExecutionProvider == nil || provider.readiness == nil ||
		isolationDomainID != provider.target.isolationDomainID {
		return errors.Join(reconcile.ErrEffectInvalid, ErrRuntimeCertificationScopeMismatch)
	}
	return provider.readiness.Check(ctx)
}

func (provider *certifiedExecutionProvider) SelectGateway(
	ctx context.Context,
	request execution.PlacementRequest,
) (execution.Placement, error) {
	if err := provider.check(ctx, request.IsolationDomainID); err != nil {
		return execution.Placement{}, err
	}
	return provider.ExecutionProvider.SelectGateway(ctx, request)
}

func (provider *certifiedExecutionProvider) Create(
	ctx context.Context,
	request execution.CreateRequest,
) (execution.Execution, error) {
	if err := provider.check(ctx, request.IsolationDomainID); err != nil {
		return execution.Execution{}, err
	}
	return provider.ExecutionProvider.Create(ctx, request)
}

func (provider *certifiedExecutionProvider) StartRuntime(
	ctx context.Context,
	ref execution.ExecutionRef,
) (execution.RuntimeSession, error) {
	if err := provider.check(ctx, ref.IsolationDomainID); err != nil {
		return nil, err
	}
	return provider.ExecutionProvider.StartRuntime(ctx, ref)
}

func (provider *certifiedExecutionProvider) Export(
	ctx context.Context,
	request execution.ExportRequest,
) (execution.ExportResult, error) {
	if err := provider.check(ctx, request.IsolationDomainID); err != nil {
		return execution.ExportResult{}, err
	}
	return provider.ExecutionProvider.Export(ctx, request)
}

func (provider *certifiedExecutionProvider) Terminate(
	ctx context.Context,
	ref execution.ExecutionRef,
) error {
	if err := provider.check(ctx, ref.IsolationDomainID); err != nil {
		return err
	}
	return provider.ExecutionProvider.Terminate(ctx, ref)
}

type certificationBoundDriver struct {
	delegate  *reconcile.RoutedDriver
	readiness runtimeCertificationReadiness
	target    runtimeCertificationTarget
}

func (driver *certificationBoundDriver) check(
	ctx context.Context,
	effect persistence.EffectRecord,
) error {
	if effect.OperationKind != persistence.OperationKindInvocation {
		return nil
	}
	if driver == nil || driver.delegate == nil || driver.readiness == nil ||
		effect.IsolationDomainID != driver.target.isolationDomainID {
		return errors.Join(reconcile.ErrEffectInvalid, ErrRuntimeCertificationScopeMismatch)
	}
	return driver.readiness.Check(ctx)
}

func (driver *certificationBoundDriver) Observe(
	ctx context.Context,
	effect persistence.EffectRecord,
) (map[string]any, bool, error) {
	if err := driver.check(ctx, effect); err != nil {
		return nil, false, err
	}
	return driver.delegate.Observe(ctx, effect)
}

func (driver *certificationBoundDriver) Apply(
	ctx context.Context,
	effect persistence.EffectRecord,
) (map[string]any, error) {
	if err := driver.check(ctx, effect); err != nil {
		return nil, err
	}
	return driver.delegate.Apply(ctx, effect)
}

func (driver *certificationBoundDriver) ObserveClaimed(
	ctx context.Context,
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
) (map[string]any, bool, error) {
	if err := driver.check(ctx, effect); err != nil {
		return nil, false, err
	}
	return driver.delegate.ObserveClaimed(ctx, claim, effect)
}

func (driver *certificationBoundDriver) ApplyClaimed(
	ctx context.Context,
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
) (map[string]any, error) {
	if err := driver.check(ctx, effect); err != nil {
		return nil, err
	}
	return driver.delegate.ApplyClaimed(ctx, claim, effect)
}

var (
	_ reconcile.InvocationAdmissionAuthorizer    = (*certifiedInvocationAuthorizer)(nil)
	_ reconcile.InvocationRuntimeAuthorizer      = (*certifiedInvocationAuthorizer)(nil)
	_ reconcile.InvocationCancellationAuthorizer = (*certifiedInvocationAuthorizer)(nil)
	_ execution.ExecutionProvider                = (*certifiedExecutionProvider)(nil)
	_ reconcile.EffectDriver                     = (*certificationBoundDriver)(nil)
	_ reconcile.ClaimedEffectDriver              = (*certificationBoundDriver)(nil)
)
