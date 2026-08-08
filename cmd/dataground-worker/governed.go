package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/openshell"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/execution/s3store"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	workerModeReference           = "reference"
	workerModeGovernedDevelopment = "governed-development"

	governedOpenShellVersion       = "0.0.86"
	governedGatewayDriver          = "docker"
	governedProviderProfile        = "codex"
	governedGatewayEndpoint        = "http://127.0.0.1:8080"
	governedSandboxImage           = "ghcr.io/nvidia/openshell-community/sandboxes/base@sha256:aeef1c63f00e2913ea002ccb3aaf925f338b5c5d70e63576f0d95c16a138044e"
	governedRuntimeLeaseDuration   = 30 * time.Second
	governedRuntimeRenewInterval   = 10 * time.Second
	maximumConfiguredArtifactBytes = 1 << 30
)

type environmentLookup func(string) (string, bool)

type workerConfig struct {
	mode                 string
	isolationDomainID    string
	gatewayID            string
	gatewayEndpoint      string
	openShellBinary      string
	policyWorkspace      string
	exportWorkspace      string
	s3Endpoint           string
	s3Bucket             string
	s3RequestTimeout     time.Duration
	maximumArtifactBytes int64
	certification        runtimeCertificationConfig
}

type workerResources struct {
	policyWorkspace *openshell.PolicyWorkspace
	exportWorkspace *openshell.ExportWorkspace
	readiness       runtimeCertificationReadiness
}

type governedExecutionPlanStore struct {
	execution.ExecutionPlanStore
	readiness runtimeCertificationReadiness
	target    runtimeCertificationTarget
}

type durableProviderCredentialAuthorizer struct {
	repository *persistence.Repository
}

func (authorizer durableProviderCredentialAuthorizer) AuthorizeProviderCredentialUse(
	ctx context.Context,
	use execution.ProviderCredentialUse,
) error {
	if authorizer.repository == nil {
		return execution.ErrProviderCredentialUseDenied
	}
	err := authorizer.repository.AuthorizeProviderCredentialUse(ctx, persistence.ProviderCredentialUse{
		Contract:          persistence.ProviderCredentialAuthorizationContract,
		IsolationDomainID: use.IsolationDomainID,
		RevisionID:        use.RevisionID,
		OperationID:       use.OperationID,
		ProviderProfile:   use.ProviderProfile,
		Purpose:           use.Purpose,
		Phase:             use.Phase,
		ActorID:           use.ActorID,
		CorrelationID:     use.CorrelationID,
	})
	if errors.Is(err, persistence.ErrProviderCredentialUnauthorized) {
		return errors.Join(execution.ErrProviderCredentialUseDenied, err)
	}
	return err
}

func (store governedExecutionPlanStore) GetExecutionPlan(
	ctx context.Context,
	isolationDomainID string,
	revisionID string,
) (execution.ExecutionPlan, error) {
	if isolationDomainID != store.target.isolationDomainID || revisionID != store.target.revisionID {
		return execution.ExecutionPlan{}, execution.ErrExecutionPlanRevisionMismatch
	}
	if err := store.readiness.Check(ctx); err != nil {
		return execution.ExecutionPlan{}, err
	}
	plan, err := store.ExecutionPlanStore.GetExecutionPlan(ctx, isolationDomainID, revisionID)
	if err != nil {
		return execution.ExecutionPlan{}, err
	}
	if !validGovernedDevelopmentPlan(plan) {
		return execution.ExecutionPlan{}, execution.ErrExecutionPlanRevisionMismatch
	}
	return plan, nil
}

func validGovernedDevelopmentPlan(plan execution.ExecutionPlan) bool {
	return plan.RuntimeProfile == reconcile.CodexAppServerRuntimeProfileV1 &&
		plan.ImageReference == governedSandboxImage &&
		slices.Equal(plan.ProviderProfiles, []string{governedProviderProfile}) &&
		slices.Equal(plan.RequiredCapabilities, []string{reconcile.CodexAppServerRuntimeProfileV1})
}

type scopedReconcileStore interface {
	reconcile.Store
	ClaimNextForServiceRevision(
		context.Context,
		string,
		string,
		string,
		string,
		string,
		time.Duration,
	) (*persistence.OperationClaim, error)
}

type isolationScopedReconcileStore struct {
	scopedReconcileStore
	target runtimeCertificationTarget
}

func (store *isolationScopedReconcileStore) ClaimNext(
	ctx context.Context,
	kind string,
	workerID string,
	leaseDuration time.Duration,
) (*persistence.OperationClaim, error) {
	return store.ClaimNextForServiceRevision(
		ctx,
		kind,
		store.target.isolationDomainID,
		store.target.serviceID,
		store.target.revisionID,
		workerID,
		leaseDuration,
	)
}

func workerReconcileStore(
	repository scopedReconcileStore,
	config workerConfig,
) reconcile.Store {
	if config.mode == workerModeGovernedDevelopment {
		return &isolationScopedReconcileStore{
			scopedReconcileStore: repository,
			target:               config.certification.target,
		}
	}
	return repository
}

func (resources *workerResources) Ready(ctx context.Context) error {
	if resources == nil || resources.readiness == nil {
		return nil
	}
	return resources.readiness.Check(ctx)
}

func (resources *workerResources) Close() error {
	if resources == nil {
		return nil
	}
	var exportErr, policyErr error
	if resources.exportWorkspace != nil {
		exportErr = resources.exportWorkspace.Close()
	}
	if resources.policyWorkspace != nil {
		policyErr = resources.policyWorkspace.Close()
	}
	return errors.Join(exportErr, policyErr)
}

func loadWorkerConfig(lookup environmentLookup) (workerConfig, error) {
	mode, _ := lookup("DATAGROUND_WORKER_MODE")
	if mode == "" {
		mode = workerModeReference
	}
	config := workerConfig{mode: mode}
	if mode == workerModeReference {
		return config, nil
	}
	if mode != workerModeGovernedDevelopment {
		return workerConfig{}, fmt.Errorf("unsupported DATAGROUND_WORKER_MODE %q", mode)
	}

	var err error
	if config.isolationDomainID, err = requiredEnvironment(lookup, "DATAGROUND_DEVELOPMENT_ISOLATION_DOMAIN_ID"); err != nil {
		return workerConfig{}, err
	}
	config.certification, err = loadRuntimeCertificationConfig(lookup)
	if err != nil {
		return workerConfig{}, err
	}
	if config.certification.target.isolationDomainID != config.isolationDomainID {
		return workerConfig{}, ErrRuntimeCertificationScopeMismatch
	}
	if config.gatewayID, err = requiredEnvironment(lookup, "DATAGROUND_OPENSHELL_GATEWAY_ID"); err != nil {
		return workerConfig{}, err
	}
	if config.gatewayEndpoint, err = requiredEnvironment(lookup, "DATAGROUND_OPENSHELL_GATEWAY_ENDPOINT"); err != nil {
		return workerConfig{}, err
	}
	if err := requireLoopbackHTTPOrigin(config.gatewayEndpoint); err != nil {
		return workerConfig{}, fmt.Errorf("DATAGROUND_OPENSHELL_GATEWAY_ENDPOINT: %w", err)
	}
	if config.gatewayEndpoint != governedGatewayEndpoint {
		return workerConfig{}, fmt.Errorf(
			"DATAGROUND_OPENSHELL_GATEWAY_ENDPOINT must match the pinned development endpoint %q",
			governedGatewayEndpoint,
		)
	}
	config.openShellBinary = "openshell"
	if value, found := lookup("DATAGROUND_OPENSHELL_BINARY"); found {
		if value == "" {
			return workerConfig{}, errors.New("DATAGROUND_OPENSHELL_BINARY must not be empty")
		}
		config.openShellBinary = value
	}
	if config.policyWorkspace, err = requiredEnvironment(lookup, "DATAGROUND_OPENSHELL_POLICY_WORKSPACE"); err != nil {
		return workerConfig{}, err
	}
	if config.exportWorkspace, err = requiredEnvironment(lookup, "DATAGROUND_OPENSHELL_EXPORT_WORKSPACE"); err != nil {
		return workerConfig{}, err
	}
	if err := requireDisjointAbsoluteWorkspaces(config.policyWorkspace, config.exportWorkspace); err != nil {
		return workerConfig{}, err
	}
	if config.s3Endpoint, err = requiredEnvironment(lookup, "DATAGROUND_S3_ENDPOINT"); err != nil {
		return workerConfig{}, err
	}
	if err := requireLoopbackHTTPOrigin(config.s3Endpoint); err != nil {
		return workerConfig{}, fmt.Errorf("DATAGROUND_S3_ENDPOINT: %w", err)
	}
	if config.s3Bucket, err = requiredEnvironment(lookup, "DATAGROUND_S3_BUCKET"); err != nil {
		return workerConfig{}, err
	}
	requestTimeout, err := requiredEnvironment(lookup, "DATAGROUND_S3_REQUEST_TIMEOUT")
	if err != nil {
		return workerConfig{}, err
	}
	config.s3RequestTimeout, err = time.ParseDuration(requestTimeout)
	if err != nil || config.s3RequestTimeout < time.Second || config.s3RequestTimeout > 10*time.Minute {
		return workerConfig{}, errors.New("DATAGROUND_S3_REQUEST_TIMEOUT must be between 1s and 10m")
	}
	maximumBytes, err := requiredEnvironment(lookup, "DATAGROUND_INVOCATION_ARTIFACT_MAX_BYTES")
	if err != nil {
		return workerConfig{}, err
	}
	config.maximumArtifactBytes, err = strconv.ParseInt(maximumBytes, 10, 64)
	if err != nil || config.maximumArtifactBytes <= 0 || config.maximumArtifactBytes > maximumConfiguredArtifactBytes {
		return workerConfig{}, fmt.Errorf(
			"DATAGROUND_INVOCATION_ARTIFACT_MAX_BYTES must be between 1 and %d",
			maximumConfiguredArtifactBytes,
		)
	}
	return config, nil
}

func requiredEnvironment(lookup environmentLookup, name string) (string, error) {
	value, found := lookup(name)
	if !found || value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func requireLoopbackHTTPOrigin(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("a plain HTTP loopback origin without path, credentials, query, or fragment is required")
	}
	host := parsed.Hostname()
	address := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (address == nil || !address.IsLoopback()) {
		return errors.New("a loopback host is required")
	}
	return nil
}

func requireDisjointAbsoluteWorkspaces(policyRoot string, exportRoot string) error {
	policyRoot = filepath.Clean(policyRoot)
	exportRoot = filepath.Clean(exportRoot)
	if !filepath.IsAbs(policyRoot) || !filepath.IsAbs(exportRoot) ||
		policyRoot == string(filepath.Separator) || exportRoot == string(filepath.Separator) {
		return errors.New("OpenShell workspaces must be non-root absolute paths")
	}
	if pathContains(policyRoot, exportRoot) || pathContains(exportRoot, policyRoot) {
		return errors.New("OpenShell policy and export workspaces must be disjoint")
	}
	return nil
}

func pathContains(parent string, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func composeWorkerDriver(
	ctx context.Context,
	pool *pgxpool.Pool,
	repository *persistence.Repository,
	config workerConfig,
) (reconcile.EffectDriver, *workerResources, error) {
	if config.mode == workerModeReference {
		return reconcile.NewReferenceDriver(pool), &workerResources{}, nil
	}
	if config.mode != workerModeGovernedDevelopment || pool == nil || repository == nil {
		return nil, nil, errors.New("governed worker configuration and durable dependencies are required")
	}

	checker, err := newRuntimeCertificationChecker(
		config.certification,
		nodeRuntimeCertificationVerifier{},
	)
	if err != nil {
		return nil, nil, err
	}
	if err := checker.Check(ctx); err != nil {
		return nil, nil, err
	}
	var runtimeProfile string
	if err := pool.QueryRow(ctx, `
		SELECT runtime_profile
		FROM service_revisions
		WHERE isolation_domain_id = $1
		  AND service_id = $2
		  AND id = $3
		  AND state = 'published'
	`, config.certification.target.isolationDomainID, config.certification.target.serviceID,
		config.certification.target.revisionID).Scan(&runtimeProfile); err != nil ||
		runtimeProfile != reconcile.CodexAppServerRuntimeProfileV1 {
		return nil, nil, ErrRuntimeCertificationScopeMismatch
	}
	resources := &workerResources{readiness: checker}
	fail := func(cause error) (reconcile.EffectDriver, *workerResources, error) {
		return nil, nil, errors.Join(cause, resources.Close())
	}
	policyWorkspace, err := openshell.OpenPolicyWorkspace(config.policyWorkspace)
	if err != nil {
		return fail(err)
	}
	resources.policyWorkspace = policyWorkspace
	exportWorkspace, err := openshell.OpenExportWorkspace(config.exportWorkspace, config.maximumArtifactBytes)
	if err != nil {
		return fail(err)
	}
	resources.exportWorkspace = exportWorkspace

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	objectStore, err := s3store.New(s3store.Config{
		Endpoint:             config.s3Endpoint,
		Bucket:               config.s3Bucket,
		AddressingStyle:      s3store.PathStyle,
		AllowHTTPForLoopback: true,
		HTTPClient: &http.Client{
			Transport: transport,
			Timeout:   config.s3RequestTimeout,
		},
	})
	if err != nil {
		return fail(err)
	}
	artifactStore, err := s3store.NewArtifactStore(objectStore, config.maximumArtifactBytes)
	if err != nil {
		return fail(err)
	}
	artifactFinalizer, err := artifact.NewFinalizer(
		repository,
		artifactStore,
		artifactStore,
		artifact.FinalizerConfig{MaximumBytes: config.maximumArtifactBytes},
	)
	if err != nil {
		return fail(err)
	}

	executionStore := executionpostgres.New(pool)
	providerProfiles, err := execution.NewProviderProfileRegistry([]string{governedProviderProfile})
	if err != nil {
		return fail(err)
	}
	openShellProvider := openshell.New(openshell.Config{
		Binary:           config.openShellBinary,
		ExpectedVersion:  governedOpenShellVersion,
		PolicyWorkspace:  policyWorkspace,
		ExportWorkspace:  exportWorkspace,
		StateStore:       executionStore,
		ProviderProfiles: providerProfiles,
	}, openshell.ExecRunner{})
	if err := openShellProvider.Check(ctx); err != nil {
		return fail(err)
	}
	provider := &certifiedExecutionProvider{
		ExecutionProvider: openShellProvider,
		readiness:         checker,
		target:            config.certification.target,
	}

	bundles, err := execution.NewObjectEnforcementBundleSource(executionStore, objectStore)
	if err != nil {
		return fail(err)
	}
	admission, err := execution.NewCredentialMediatedAdmission(
		governedExecutionPlanStore{
			ExecutionPlanStore: executionStore,
			readiness:          checker,
			target:             config.certification.target,
		},
		bundles,
		provider,
		durableProviderCredentialAuthorizer{repository: repository},
	)
	if err != nil {
		return fail(err)
	}
	policySource, err := reconcile.NewDurableInvocationAuthorizationPolicySource(repository)
	if err != nil {
		return fail(err)
	}
	baseAuthorizer, err := reconcile.NewAuditedCedarInvocationAuthorizer(policySource, repository)
	if err != nil {
		return fail(err)
	}
	authorizer := &certifiedInvocationAuthorizer{
		delegate: baseAuthorizer, readiness: checker, target: config.certification.target,
	}
	admissionDriver, err := reconcile.NewInvocationAdmissionDriver(
		repository,
		authorizer,
		admission,
		executionStore,
	)
	if err != nil {
		return fail(err)
	}
	runtimeDriver, err := reconcile.NewInvocationRuntimeDriver(
		repository,
		authorizer,
		reconcile.CodexInvocationRuntimeRequestBuilder{},
		executionStore,
		provider,
		reconcile.CodexInvocationRuntimeAdapterFactory{},
		artifactFinalizer,
		reconcile.InvocationRuntimeDriverConfig{
			LeaseDuration: governedRuntimeLeaseDuration,
			RenewInterval: governedRuntimeRenewInterval,
			Readiness:     checker.Check,
		},
	)
	if err != nil {
		return fail(err)
	}
	cancellationDriver, err := reconcile.NewInvocationCancellationDriver(
		repository,
		authorizer,
		executionStore,
		provider,
	)
	if err != nil {
		return fail(err)
	}
	routed, err := reconcile.NewGovernedInvocationDriver(
		reconcile.NewReferenceDriver(pool),
		admissionDriver,
		runtimeDriver,
		cancellationDriver,
	)
	if err != nil {
		return fail(err)
	}
	driver := &certificationBoundDriver{
		delegate: routed, readiness: checker, target: config.certification.target,
	}
	if err := checker.Check(ctx); err != nil {
		return fail(err)
	}
	if _, err := openShellProvider.RegisterGateway(ctx, execution.GatewayRegistration{
		IsolationDomainID: config.isolationDomainID,
		ID:                config.gatewayID,
		Endpoint:          config.gatewayEndpoint,
		Driver:            governedGatewayDriver,
		Capabilities:      []string{reconcile.CodexAppServerRuntimeProfileV1},
	}); err != nil {
		return fail(err)
	}
	return driver, resources, nil
}
