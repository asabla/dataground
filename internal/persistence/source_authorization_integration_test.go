package persistence_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	cedar "github.com/cedar-policy/cedar-go"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func installSourceReadPolicy(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scope, service, revision string) persistence.InvocationAuthorizationPolicyRecord {
	t.Helper()
	policy, err := reconcile.NewInvocationAuthorizationPolicyWithSourceEntities(reconcile.InvocationAuthorizationPolicyScope{IsolationDomainID: scope, ServiceID: service, RevisionID: revision}, "source-read", reconcile.CanonicalSourceCedarSchema(), []byte(`permit(principal in DataGround::Role::"source-reader", action == DataGround::Action::"read-source", resource);`), sourceReadEntities(t, true))
	if err != nil {
		t.Fatal(err)
	}
	reason := sha256.Sum256([]byte("source fixture"))
	record := persistence.InvocationAuthorizationPolicyRecord{Contract: policy.Contract, IsolationDomainID: scope, ServiceID: service, RevisionID: revision, PolicySetID: policy.PolicySetID, PolicyDigest: policy.Digest[:], Schema: policy.Schema, Policies: policy.Policies, Entities: policy.Entities, InstalledBy: "operator", InstallationCorrelationID: identity.New("cor"), ReasonDigest: reason[:]}
	directory := t.TempDir()
	policyPath, entityPath := filepath.Join(directory, "source.cedar"), filepath.Join(directory, "entities.json")
	if err := os.WriteFile(policyPath, policy.Policies, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entityPath, policy.Entities, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "go", "run", "../../cmd/dataground-policy-install", "-source-capable", "-isolation-domain", scope, "-service", service, "-revision", revision, "-policy-set", policy.PolicySetID, "-policy-file", policyPath, "-entity-file", entityPath, "-actor", record.InstalledBy, "-reason", "source fixture", "-correlation-id", record.InstallationCorrelationID)
	command.Env = append(os.Environ(), "DATAGROUND_DATABASE_URL="+testDatabaseURL(t))
	if output, err := command.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatal("source policy installer failed or emitted output", err)
	}
	if err := persistence.NewRepository(pool).InstallInvocationAuthorizationPolicy(ctx, record); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, err := persistence.OpenSQL(context.Background(), testDatabaseURL(t))
		if err != nil {
			t.Error(err)
			return
		}
		defer cleanup.Close()
		if _, err := cleanup.ExecContext(context.Background(), `TRUNCATE invocation_authorization_entity_activations,invocation_authorization_entity_generations,invocation_authorization_policy_withdrawals,invocation_authorization_policies`); err != nil {
			t.Error(err)
		}
	})
	return record
}

func sourceReadEntities(t *testing.T, member bool) []byte {
	t.Helper()
	parents := "[]"
	if member {
		parents = `[{"type":"DataGround::Role","id":"source-reader"}]`
	}
	value := fmt.Sprintf(`[{"uid":{"type":"DataGround::Actor","id":"source-requester"},"attrs":{},"parents":%s,"tags":{}},{"uid":{"type":"DataGround::Role","id":"source-reader"},"attrs":{},"parents":[],"tags":{}}]`, parents)
	var entities cedar.EntityMap
	if err := json.Unmarshal([]byte(value), &entities); err != nil {
		t.Fatal(err)
	}
	canonical, err := entities.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
func changeSourceReadMembership(t *testing.T, ctx context.Context, pool *pgxpool.Pool, policy persistence.InvocationAuthorizationPolicyRecord, generation int64, member bool) {
	t.Helper()
	entities := sourceReadEntities(t, member)
	digest := sha256.Sum256(entities)
	reason := sha256.Sum256([]byte("source membership"))
	repo := persistence.NewRepository(pool)
	if err := repo.PublishInvocationAuthorizationEntityGeneration(ctx, persistence.InvocationAuthorizationEntityGeneration{Contract: persistence.InvocationAuthorizationEntityGenerationContract, IsolationDomainID: policy.IsolationDomainID, ServiceID: policy.ServiceID, RevisionID: policy.RevisionID, Generation: generation, EntityDigest: digest[:], Entities: entities, PublishedBy: "operator", CorrelationID: identity.New("cor"), ReasonDigest: reason[:]}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ActivateInvocationAuthorizationEntityGeneration(ctx, persistence.InvocationAuthorizationEntityActivation{Contract: persistence.InvocationAuthorizationEntityActivationContract, IsolationDomainID: policy.IsolationDomainID, ServiceID: policy.ServiceID, RevisionID: policy.RevisionID, Generation: generation, InstalledPolicyDigest: policy.PolicyDigest, ActivatedBy: "operator", CorrelationID: identity.New("cor"), ReasonDigest: reason[:]}); err != nil {
		t.Fatal(err)
	}
}

type unavailableSourceEvaluator struct{}

func (unavailableSourceEvaluator) EvaluateSourceRead(context.Context, persistence.InvocationAuthorizationPolicyRecord, authz.SourceReadRequest) authz.Outcome {
	return authz.OutcomeUnavailable
}
