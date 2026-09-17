package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/runtimeevidence"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
)

func publicationArguments() []string {
	return []string{"publish-development", "--expected-version", "1", "--plan-digest", "sha256:" + strings.Repeat("a", 64), "--policy-digest", "sha256:" + strings.Repeat("b", 64), "--actor", "operator", "--correlation-id", "cor_0123456789abcdefghij"}
}

func TestDevelopmentPublicationRequiresExactExplicitConfiguration(t *testing.T) {
	values := validStrictAcceptanceEnvironment()
	config, err := loadDevelopmentPublication(publicationArguments(), mapEnvironment(values))
	if err != nil || !config.input.Valid() {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"unknown"}, append(publicationArguments(), "--actor", "another"), append(publicationArguments(), "extra"), publicationArguments()[:9]} {
		if _, err := loadDevelopmentPublication(args, mapEnvironment(values)); err == nil {
			t.Fatal("ambiguous command accepted")
		}
	}
	for _, value := range []string{"0", "-1", "01", "1.0"} {
		args := publicationArguments()
		args[2] = value
		if _, err := loadDevelopmentPublication(args, mapEnvironment(values)); err == nil {
			t.Fatal("invalid version accepted")
		}
	}
	for _, profile := range []string{localRuntimeProfile, governedCertificationProfile, ""} {
		other := validStrictAcceptanceEnvironment()
		other["DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE"] = profile
		if _, err := loadDevelopmentPublication(publicationArguments(), mapEnvironment(other)); err == nil {
			t.Fatal("non-strict publication accepted")
		}
	}
	for name, value := range map[string]string{"DATAGROUND_LOCAL_RUNTIME_MODEL": "different-model", "DATAGROUND_S3_BUCKET": "different-bucket", "DATAGROUND_LOCAL_RUNTIME_GATEWAY_CONTAINER_ID": strings.Repeat("4", 64)} {
		other := validStrictAcceptanceEnvironment()
		other[name] = value
		changed, err := loadDevelopmentPublication(publicationArguments(), mapEnvironment(other))
		if err != nil || changed.input.VerificationDigest == config.input.VerificationDigest {
			t.Fatal("publication intent omitted changed pin", name, err)
		}
	}
}

type publicationPlans struct {
	execution.ExecutionPlanStore
	plan execution.ExecutionPlan
}

func (source publicationPlans) GetExecutionPlan(context.Context, string, string) (execution.ExecutionPlan, error) {
	return source.plan, nil
}

type publicationBundles struct{ bundle execution.EnforcementBundle }

func (source publicationBundles) GetEnforcementBundle(context.Context, string, string) (execution.EnforcementBundle, error) {
	return source.bundle, nil
}

type publicationPolicies struct {
	policy reconcile.InvocationAuthorizationPolicy
}

func (source publicationPolicies) ResolveInvocationAuthorizationPolicy(context.Context, reconcile.InvocationAuthorizationPolicyScope) (reconcile.InvocationAuthorizationPolicy, error) {
	return source.policy, nil
}

func TestDevelopmentPublicationVerifiesActualMaterialAndAcceptance(t *testing.T) {
	for _, mode := range []string{"valid", "authorized", "authorized older policy", "operator publication policy", "plan scope", "image", "plan digest", "bundle scope", "bundle identity", "bytes", "policy scope", "policy digest", "expired acceptance", "deployment", "verifier", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			config, err := loadDevelopmentPublication(publicationArguments(), mapEnvironment(validStrictAcceptanceEnvironment()))
			if err != nil {
				t.Fatal(err)
			}
			target := config.input.Target
			plan := execution.ExecutionPlan{SchemaVersion: execution.ExecutionPlanSchemaV1, IsolationDomainID: target.IsolationDomainID, RevisionID: target.RevisionID, RuntimeProfile: target.RuntimeProfile, EnvironmentRevisionID: "environment", ImageReference: config.acceptance.image, EnvironmentManifestDigest: "sha256:" + strings.Repeat("a", 64), EnforcementBundleID: "strict-policy", EnforcementBundleDigest: strictLocalEnforcementDigest, RuntimeMatrixID: "matrix", RuntimeMatrixDigest: "sha256:" + strings.Repeat("b", 64), ProviderProfiles: []string{"codex"}, RequiredCapabilities: []string{target.RuntimeProfile}}
			config.input.PlanDigest, err = execution.DigestExecutionPlan(plan)
			if err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile("../../deploy/openshell/codex-compatibility/rosetta-runtime-policy.yaml")
			if err != nil {
				t.Fatal(err)
			}
			bundle := execution.EnforcementBundle{IsolationDomainID: target.IsolationDomainID, RevisionID: target.RevisionID, ID: plan.EnforcementBundleID, Digest: plan.EnforcementBundleDigest, Content: content}
			policy := reconcile.InvocationAuthorizationPolicy{Contract: reconcile.InvocationAuthorizationPolicyApprovalContract, IsolationDomainID: target.IsolationDomainID, ServiceID: target.ServiceID, RevisionID: target.RevisionID, Digest: sha256.Sum256([]byte("fixture-policy"))}
			config.input.PolicyDigest = "sha256:" + hex.EncodeToString(policy.Digest[:])
			receipt := strictAcceptanceReceipt(config.acceptance)
			deployment := &fakeRuntimeDeployment{}
			checker := &localRuntimeAcceptanceChecker{config: config.acceptance, run: func(context.Context, string, []string, []string) ([]byte, error) {
				if mode == "verifier" {
					return nil, errors.New("private verifier payload")
				}
				return json.Marshal(receipt)
			}, observe: func(runtimeevidence.ObservedDockerTopologyConfig) (runtimeDeploymentObservation, error) {
				return deployment, nil
			}}
			defer checker.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "authorized":
				config.command = "reconcile-authorized-publication"
				policy.Contract = reconcile.InvocationAuthorizationPolicyPublicationContract
			case "authorized older policy":
				config.command = "reconcile-authorized-publication"
			case "operator publication policy":
				policy.Contract = reconcile.InvocationAuthorizationPolicyPublicationContract
			case "plan scope":
				plan.RevisionID = "rev_00000000000000000001"
			case "image":
				plan.ImageReference = "different"
			case "plan digest":
				config.input.PlanDigest = "sha256:" + strings.Repeat("f", 64)
			case "bundle scope":
				bundle.IsolationDomainID = "iso_99999999999999999999"
			case "bundle identity":
				bundle.ID = "other"
			case "bytes":
				bundle.Content = []byte("unexpected bytes")
			case "policy scope":
				policy.ServiceID = "svc_00000000000000000001"
			case "policy digest":
				policy.Digest[0] ^= 255
			case "expired acceptance":
				receipt["expiresAt"] = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
			case "deployment":
				deployment.check = func(context.Context) error { return errors.New("private deployment detail") }
			case "cancelled":
				cancel()
			}
			proof, err := verifyDevelopmentPublication(ctx, config, publicationPlans{plan: plan}, publicationBundles{bundle}, publicationPolicies{policy}, checker)
			if mode == "valid" || mode == "authorized" {
				if err != nil || proof.Profile != persistence.StrictDevelopmentPublicationProfile || proof.ImageReference != config.acceptance.image || !proof.ExpiresAt.After(time.Now()) || deployment.checks.Load() != 1 {
					t.Fatal("verified publication failed", err)
				}
			} else if err != errDevelopmentPublication {
				t.Fatal("invalid publication admitted or dependency detail disclosed", err)
			}
		})
	}
}

func TestPreparePublicationConfigurationMatchesAuthorizedConsumerPins(t *testing.T) {
	args := append([]string{"prepare-publication-configuration"}, publicationArguments()[1:7]...)
	config, err := loadDevelopmentPublication(args, mapEnvironment(validStrictAcceptanceEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := writePublicPublicationConfiguration(&output, config.input); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 9 || fields["contract"] != "dataground.api-governed-publication/v1" || fields["verificationDigest"] != config.input.VerificationDigest {
		t.Fatal(fields)
	}
	consumerArgs := append([]string{"reconcile-authorized-publication"}, args[1:]...)
	consumerArgs = append(consumerArgs, "--operation-id", "op_00000000000000000001")
	consumer, err := loadDevelopmentPublication(consumerArgs, mapEnvironment(validStrictAcceptanceEnvironment()))
	if err != nil || consumer.input.VerificationDigest != config.input.VerificationDigest || consumer.input.ActorID != "" || consumer.publicationPolicyContract() != reconcile.InvocationAuthorizationPolicyPublicationContract {
		t.Fatal(consumer, err)
	}
	for _, invalid := range [][]string{append(args, "--actor", "operator"), append(consumerArgs, "--actor", "operator"), consumerArgs[:len(consumerArgs)-2]} {
		if _, err := loadDevelopmentPublication(invalid, mapEnvironment(validStrictAcceptanceEnvironment())); err == nil {
			t.Fatal("ambiguous authority accepted")
		}
	}
}
