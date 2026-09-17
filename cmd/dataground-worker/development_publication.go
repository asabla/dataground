package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/asabla/dataground/internal/execution"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/execution/s3store"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
)

var errDevelopmentPublication = errors.New("governed development publication unavailable; retry the exact command to resolve its outcome")

type developmentPublicationConfiguration struct {
	command     string
	operationID string
	deadline    time.Time
	acceptance  localRuntimeAcceptanceConfig
	input       persistence.DevelopmentPublicationInput
	endpoint    string
	bucket      string
}

func loadDevelopmentPublication(args []string, lookup environmentLookup) (developmentPublicationConfiguration, error) {
	var config developmentPublicationConfiguration
	if len(args) == 0 || (args[0] != "publish-development" && args[0] != "queue-development-publication" && args[0] != "reconcile-development-publication" && args[0] != "reconcile-authorized-publication" && args[0] != "prepare-publication-configuration") {
		return config, errDevelopmentPublication
	}
	config.command = args[0]
	flags := flag.NewFlagSet(config.command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var version, deadline string
	type publicationFlag struct {
		name  string
		value *string
	}
	items := []publicationFlag{{"expected-version", &version}, {"plan-digest", &config.input.PlanDigest}, {"policy-digest", &config.input.PolicyDigest}}
	if config.isPublicationConsumer() {
		items = append(items, publicationFlag{"operation-id", &config.operationID})
	} else if config.command != "prepare-publication-configuration" {
		items = append(items, publicationFlag{"actor", &config.input.ActorID}, publicationFlag{"correlation-id", &config.input.CorrelationID})
	}
	if config.command == "queue-development-publication" {
		items = append(items, publicationFlag{"deadline", &deadline})
	}
	for _, item := range items {
		seen := false
		flags.Func(item.name, "exact reviewed publication value", func(value string) error {
			if seen || value == "" {
				return errDevelopmentPublication
			}
			seen = true
			*item.value = value
			return nil
		})
	}
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return config, errDevelopmentPublication
	}
	if flags.NFlag() != len(items) && !(config.authorizedPublication() && config.operationID == "" && flags.NFlag() == len(items)-1) {
		return config, errDevelopmentPublication
	}
	var err error
	if config.command == "queue-development-publication" {
		config.deadline, err = time.Parse(time.RFC3339Nano, deadline)
		if err != nil || config.deadline.Format(time.RFC3339Nano) != deadline {
			return config, errDevelopmentPublication
		}
	}
	if config.isPublicationConsumer() && !(config.authorizedPublication() && config.operationID == "") && !publicationOperationIDPattern.MatchString(config.operationID) {
		return config, errDevelopmentPublication
	}
	config.input.ExpectedVersion, err = strconv.Atoi(version)
	if err != nil || strconv.Itoa(config.input.ExpectedVersion) != version {
		return config, errDevelopmentPublication
	}
	if profile, _ := lookup("DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE"); profile != strictLocalRuntimeProfile {
		return config, errDevelopmentPublication
	}
	acceptance, err := loadLocalRuntimeAcceptanceConfig(lookup)
	if err != nil || acceptance.strict == nil {
		return config, errDevelopmentPublication
	}
	config.acceptance = *acceptance
	config.endpoint, err = requiredEnvironment(lookup, "DATAGROUND_S3_ENDPOINT")
	if err != nil || requireLoopbackHTTPOrigin(config.endpoint) != nil {
		return config, errDevelopmentPublication
	}
	config.bucket, err = requiredEnvironment(lookup, "DATAGROUND_S3_BUCKET")
	if err != nil {
		return config, errDevelopmentPublication
	}
	config.input.Contract = persistence.DevelopmentPublicationContract
	config.input.Target = persistence.InvocationDispatchTarget{IsolationDomainID: acceptance.target.isolationDomainID, ServiceID: acceptance.target.serviceID, RevisionID: acceptance.target.revisionID, RuntimeProfile: persistence.GovernedInvocationRuntimeProfile}
	// Freeze every verifier and routing input in command identity. Paths are kept
	// only in this hash; receipts and audit never disclose deployment routing.
	strict := acceptance.strict
	pins, err := json.Marshal([]any{"dataground.development-publication-inputs/v1", acceptance.target.isolationDomainID, acceptance.target.serviceID, acceptance.target.revisionID,
		acceptance.envelopeFile, acceptance.trustFile, acceptance.evidenceDirectory, acceptance.envelopeSHA256, acceptance.trustSHA256, acceptance.sourceRevision, acceptance.minimumGeneration, acceptance.rejectedIDs, acceptance.image, acceptance.model, acceptance.nodeBinary, acceptance.githubBinary,
		strict.supervisorImage, strict.topology.SupervisorLocalImageID, strict.topology.GatewayConfigSHA256, strict.topology.RunID, strict.topology.ContainerID, strict.topology.StartedAt, strict.topology.WorkspaceRoot, strict.topology.DockerBinary, config.endpoint, config.bucket})
	if err != nil {
		return config, errDevelopmentPublication
	}
	digest := sha256.Sum256(pins)
	config.input.VerificationDigest = "sha256:" + hex.EncodeToString(digest[:])
	if !config.input.ValidReviewedInputs() || (!config.isPublicationConsumer() && config.command != "prepare-publication-configuration" && !config.input.Valid()) {
		return config, errDevelopmentPublication
	}
	return config, nil
}

type publicationAcceptanceVerifier interface {
	check(context.Context) (localRuntimeAcceptanceProof, error)
}

func verifyDevelopmentPublication(ctx context.Context, config developmentPublicationConfiguration, plans execution.ExecutionPlanStore, bundles execution.EnforcementBundleSource, policies reconcile.InvocationAuthorizationPolicySource, acceptance publicationAcceptanceVerifier) (persistence.DevelopmentPublicationEvidence, error) {
	target := config.input.Target
	plan, err := plans.GetExecutionPlan(ctx, target.IsolationDomainID, target.RevisionID)
	if err != nil || plan.IsolationDomainID != target.IsolationDomainID || plan.RevisionID != target.RevisionID || !validGovernedDevelopmentPlan(plan, config.acceptance.image, strictLocalRuntimeProfile) {
		return persistence.DevelopmentPublicationEvidence{}, errDevelopmentPublication
	}
	digest, err := execution.DigestExecutionPlan(plan)
	if err != nil || digest != config.input.PlanDigest {
		return persistence.DevelopmentPublicationEvidence{}, errDevelopmentPublication
	}
	bundle, err := bundles.GetEnforcementBundle(ctx, target.IsolationDomainID, plan.EnforcementBundleID)
	defer clear(bundle.Content)
	if err != nil || bundle.IsolationDomainID != target.IsolationDomainID || bundle.RevisionID != target.RevisionID || bundle.ID != plan.EnforcementBundleID || bundle.Digest != plan.EnforcementBundleDigest || execution.VerifyEnforcementPolicy(bundle.Content, plan.EnforcementBundleDigest) != nil {
		return persistence.DevelopmentPublicationEvidence{}, errDevelopmentPublication
	}
	policy, err := policies.ResolveInvocationAuthorizationPolicy(ctx, reconcile.InvocationAuthorizationPolicyScope{IsolationDomainID: target.IsolationDomainID, ServiceID: target.ServiceID, RevisionID: target.RevisionID})
	if err != nil || policy.IsolationDomainID != target.IsolationDomainID || policy.ServiceID != target.ServiceID || policy.RevisionID != target.RevisionID || policy.Contract != config.publicationPolicyContract() || "sha256:"+hex.EncodeToString(policy.Digest[:]) != config.input.PolicyDigest {
		return persistence.DevelopmentPublicationEvidence{}, errDevelopmentPublication
	}
	proof, err := acceptance.check(ctx)
	if err != nil || ctx.Err() != nil {
		return persistence.DevelopmentPublicationEvidence{}, errDevelopmentPublication
	}
	return persistence.DevelopmentPublicationEvidence{AcceptanceID: proof.acceptanceID, Generation: proof.generation, Profile: strictLocalRuntimeProfile, ImageReference: config.acceptance.image, ExpiresAt: proof.expiresAt}, nil
}

func runDevelopmentPublication(ctx context.Context, args []string, output io.Writer) error {
	config, err := loadDevelopmentPublication(args, os.LookupEnv)
	if err != nil {
		return errDevelopmentPublication
	}
	if config.command == "prepare-publication-configuration" {
		return writePublicPublicationConfiguration(output, config.input)
	}
	if !config.isPublicationConsumer() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if databaseURL == "" {
		return errDevelopmentPublication
	}
	db, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		return errDevelopmentPublication
	}
	err = persistence.RequireCurrentSchema(ctx, db)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		return errDevelopmentPublication
	}
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		return errDevelopmentPublication
	}
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	if config.command == "queue-development-publication" {
		idem := persistence.Idempotency{IsolationDomainID: config.input.Target.IsolationDomainID, Method: "POST", Path: "/internal/queued-development-publication/" + config.input.Target.RevisionID, Key: config.input.CorrelationID, RequestDigest: sha256.Sum256([]byte("dataground.queued-development-publication/v1"))}
		result, err := repository.QueueDevelopmentPublication(ctx, idem, config.input, config.deadline)
		if err != nil {
			return errDevelopmentPublication
		}
		return writeDevelopmentPublicationReceipt(output, result.Body)
	}
	if config.isPublicationConsumer() && config.operationID != "" {
		require := repository.RequireDevelopmentPublication
		if config.authorizedPublication() {
			require = repository.RequireAuthorizedDevelopmentPublication
		}
		if err := require(ctx, config.operationID, config.input); err != nil {
			return errDevelopmentPublication
		}
	}
	store := executionpostgres.New(pool)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	objects, err := s3store.New(s3store.Config{Endpoint: config.endpoint, Bucket: config.bucket, AddressingStyle: s3store.PathStyle, AllowHTTPForLoopback: true, HTTPClient: &http.Client{Transport: transport, Timeout: 15 * time.Second}})
	if err != nil {
		return errDevelopmentPublication
	}
	bundles, err := execution.NewObjectEnforcementBundleSource(store, objects)
	if err != nil {
		return errDevelopmentPublication
	}
	policies, err := reconcile.NewDurableInvocationAuthorizationPolicySource(repository)
	if err != nil {
		return errDevelopmentPublication
	}
	checker := &localRuntimeAcceptanceChecker{config: config.acceptance}
	defer checker.Close()
	verify := func(ctx context.Context) (persistence.DevelopmentPublicationEvidence, error) {
		return verifyDevelopmentPublication(ctx, config, store, bundles, policies, checker)
	}
	if config.isPublicationConsumer() {
		workerID := os.Getenv("DATAGROUND_WORKER_ID")
		if config.authorizedPublication() {
			authorizer, err := reconcile.NewPublicationAuthorizer(policies, repository)
			if err != nil {
				return errDevelopmentPublication
			}
			return consumeAuthorizedDevelopmentPublication(ctx, repository, config, workerID, verify, authorizer.AuthorizePublication, output)
		}
		return consumeDevelopmentPublication(ctx, repository, config, workerID, verify, output)
	}
	result, err := repository.PublishDevelopmentRevision(ctx, config.input, verify)
	if err != nil {
		return errDevelopmentPublication
	}
	return writeDevelopmentPublicationReceipt(output, result.Body)
}

func writeDevelopmentPublicationReceipt(output io.Writer, body []byte) error {
	receipt := append(body, '\n')
	if written, err := output.Write(receipt); err != nil || written != len(receipt) {
		return errors.New("publication receipt could not be written; retry the exact command")
	}
	return nil
}

func (config developmentPublicationConfiguration) authorizedPublication() bool {
	return config.command == "reconcile-authorized-publication"
}
func (config developmentPublicationConfiguration) isPublicationConsumer() bool {
	return config.command == "reconcile-development-publication" || config.authorizedPublication()
}
func (config developmentPublicationConfiguration) publicationPolicyContract() string {
	if config.authorizedPublication() {
		return reconcile.InvocationAuthorizationPolicyPublicationContract
	}
	return reconcile.InvocationAuthorizationPolicyApprovalContract
}

// writePublicPublicationConfiguration exports only reviewed identity and digests.
// Runtime paths, credentials, provider routes and native endpoints remain internal.
func writePublicPublicationConfiguration(output io.Writer, input persistence.DevelopmentPublicationInput) error {
	if output == nil || !input.ValidReviewedInputs() {
		return errDevelopmentPublication
	}
	configuration := struct {
		Contract           string `json:"contract"`
		IsolationDomainID  string `json:"isolationDomainId"`
		ServiceID          string `json:"serviceId"`
		RevisionID         string `json:"revisionId"`
		RuntimeProfile     string `json:"runtimeProfile"`
		ExpectedVersion    int    `json:"expectedVersion"`
		PlanDigest         string `json:"planDigest"`
		PolicyDigest       string `json:"policyDigest"`
		VerificationDigest string `json:"verificationDigest"`
	}{"dataground.api-governed-publication/v1", input.Target.IsolationDomainID, input.Target.ServiceID, input.Target.RevisionID, input.Target.RuntimeProfile, input.ExpectedVersion, input.PlanDigest, input.PolicyDigest, input.VerificationDigest}
	if err := json.NewEncoder(output).Encode(configuration); err != nil {
		return errDevelopmentPublication
	}
	return nil
}
