package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/workspace"
	"strings"
	"testing"
)

func sourceRequestFixture() authz.SourceReadRequest {
	return authz.SourceReadRequest{Use: workspace.SourceUse{Contract: workspace.SourceUseV1, Record: workspace.SourceRecord{SchemaVersion: workspace.SourceRecordV1, IsolationDomainID: "iso_" + strings.Repeat("a", 20), WorkspaceID: "wsp_" + strings.Repeat("a", 20), ID: "src_" + strings.Repeat("a", 20), ArchiveDigest: "sha256:" + strings.Repeat("a", 64), StorageProfileDigest: "sha256:" + strings.Repeat("b", 64), ArchiveBytes: 2048, EntryCount: 1, SourceBytes: 1}, RevisionID: "rev_" + strings.Repeat("a", 20), OperationID: "op_" + strings.Repeat("a", 20), GrantGeneration: 1, ActorID: "actor_1", CorrelationID: "cor_" + strings.Repeat("a", 20)}, ServiceID: "svc_" + strings.Repeat("a", 20), InvocationID: "inv_" + strings.Repeat("a", 20), EffectID: "eff_" + strings.Repeat("a", 20), FencingToken: 3, PlanDigest: "sha256:" + strings.Repeat("c", 64), Phase: workspace.SourceReadAdmission}
}
func sourcePolicyRecord(p InvocationAuthorizationPolicy) persistence.InvocationAuthorizationPolicyRecord {
	reason := sha256.Sum256([]byte("test"))
	return persistence.InvocationAuthorizationPolicyRecord{Contract: p.Contract, IsolationDomainID: p.IsolationDomainID, ServiceID: p.ServiceID, RevisionID: p.RevisionID, PolicySetID: p.PolicySetID, PolicyDigest: p.Digest[:], Schema: p.Schema, Policies: p.Policies, Entities: p.Entities, InstalledBy: "operator", InstallationCorrelationID: "cor_" + strings.Repeat("a", 20), ReasonDigest: reason[:]}
}
func TestSourceCedarRequiresExplicitVersionAndExactContext(t *testing.T) {
	request := sourceRequestFixture()
	r := request.Use.Record
	scope := InvocationAuthorizationPolicyScope{r.IsolationDomainID, request.ServiceID, request.Use.RevisionID}
	text := `permit(principal in DataGround::Role::"invoker", action == DataGround::Action::"read-source", resource == DataGround::WorkspaceSource::"` + r.IsolationDomainID + ":" + r.WorkspaceID + ":" + r.ID + `") when { context.phase == "read-admission" && context.grantGeneration == 1 && context.fencingToken == 3 && context.planDigest == "` + request.PlanDigest + `" && context.archiveDigest == "` + r.ArchiveDigest + `" && context.storageProfileDigest == "` + r.StorageProfileDigest + `" && context.operationID == "` + request.Use.OperationID + `" && context.invocationID == "` + request.InvocationID + `" && context.effectID == "` + request.EffectID + `" };`
	policy, err := NewInvocationAuthorizationPolicyWithSourceEntities(scope, "source-test", CanonicalSourceCedarSchema(), []byte(text), canonicalInvocationEntityFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	evaluator := &SourceCedarEvaluator{}
	if got := evaluator.EvaluateSourceRead(context.Background(), sourcePolicyRecord(policy), request); got != authz.OutcomeAllowed {
		t.Fatal("valid source policy", got)
	}
	for name, change := range map[string]func(*authz.SourceReadRequest){
		"actor":     func(r *authz.SourceReadRequest) { r.Use.ActorID = "other" },
		"domain":    func(r *authz.SourceReadRequest) { r.Use.Record.IsolationDomainID = "iso_" + strings.Repeat("b", 20) },
		"workspace": func(r *authz.SourceReadRequest) { r.Use.Record.WorkspaceID = "wsp_" + strings.Repeat("b", 20) },
		"source":    func(r *authz.SourceReadRequest) { r.Use.Record.ID = "src_" + strings.Repeat("b", 20) },
		"archive":   func(r *authz.SourceReadRequest) { r.Use.Record.ArchiveDigest = "sha256:" + strings.Repeat("b", 64) },
		"storage": func(r *authz.SourceReadRequest) {
			r.Use.Record.StorageProfileDigest = "sha256:" + strings.Repeat("a", 64)
		},
		"phase":      func(r *authz.SourceReadRequest) { r.Phase = workspace.SourceReadDelivery },
		"generation": func(r *authz.SourceReadRequest) { r.Use.GrantGeneration++ },
		"token":      func(r *authz.SourceReadRequest) { r.FencingToken++ },
		"plan":       func(r *authz.SourceReadRequest) { r.PlanDigest = "sha256:" + strings.Repeat("d", 64) },
		"operation":  func(r *authz.SourceReadRequest) { r.Use.OperationID = "op_" + strings.Repeat("b", 20) },
		"invocation": func(r *authz.SourceReadRequest) { r.InvocationID = "inv_" + strings.Repeat("b", 20) },
		"effect":     func(r *authz.SourceReadRequest) { r.EffectID = "eff_" + strings.Repeat("b", 20) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := request
			change(&changed)
			if evaluator.EvaluateSourceRead(context.Background(), sourcePolicyRecord(policy), changed) == authz.OutcomeAllowed {
				t.Fatal("changed request allowed")
			}
		})
	}
	bad, err := NewInvocationAuthorizationPolicyWithSourceEntities(scope, "bad-source", CanonicalSourceCedarSchema(), []byte(`permit(principal,action,resource) when { context.privateMissing == "secret" };`), canonicalInvocationEntityFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := evaluator.EvaluateSourceRead(context.Background(), sourcePolicyRecord(bad), request); got != authz.OutcomeUnavailable {
		t.Fatal("invalid policy did not fail closed", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := evaluator.EvaluateSourceRead(ctx, sourcePolicyRecord(policy), request); got != authz.OutcomeUnavailable {
		t.Fatal("cancelled evaluation", got)
	}
	for _, candidate := range []struct {
		schema      []byte
		constructor func(InvocationAuthorizationPolicyScope, string, []byte, []byte, []byte) (InvocationAuthorizationPolicy, error)
	}{
		{CanonicalInvocationCedarEntitySchema(), NewInvocationAuthorizationPolicyWithEntities},
		{CanonicalInvocationCedarApprovalSchema(), NewInvocationAuthorizationPolicyWithApprovalEntities},
		{CanonicalInvocationCedarQuestionSchema(), NewInvocationAuthorizationPolicyWithQuestionEntities},
		{CanonicalPublicationCedarSchema(), NewInvocationAuthorizationPolicyWithPublicationEntities},
	} {
		old, err := candidate.constructor(scope, "old-wildcard", candidate.schema, []byte(`permit(principal,action,resource);`), canonicalInvocationEntityFixture(t))
		if err != nil {
			t.Fatal(err)
		}
		if got := evaluator.EvaluateSourceRead(context.Background(), sourcePolicyRecord(old), request); got != authz.OutcomeDenied {
			t.Fatal("old wildcard granted source", old.Contract, got)
		}
	}
	old, err := NewInvocationAuthorizationPolicy(scope, "old-v1", CanonicalInvocationCedarSchema(), []byte(`permit(principal,action,resource);`))
	if err != nil {
		t.Fatal(err)
	}
	if got := evaluator.EvaluateSourceRead(context.Background(), sourcePolicyRecord(old), request); got != authz.OutcomeDenied {
		t.Fatal("v1 wildcard granted source", got)
	}
}

func TestSourcePolicyRetainsPublicationAuthorization(t *testing.T) {
	request := publicationRequestFixture()
	policy, err := NewInvocationAuthorizationPolicyWithSourceEntities(InvocationAuthorizationPolicyScope{request.IsolationDomainID, request.ServiceID, request.RevisionID}, "source-publication", CanonicalSourceCedarSchema(), []byte(`permit(principal in DataGround::Role::"invoker",action == DataGround::Action::"publish",resource);`), canonicalInvocationEntityFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	request.PolicyDigest = "sha256:" + hex.EncodeToString(policy.Digest[:])
	recorder := &publicationDecisionRecorder{}
	authorizer, err := NewPublicationAuthorizer(publicationPolicySource{policy: policy}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	if err := authorizer.AuthorizePublication(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(recorder.records) != 1 || recorder.records[0].PolicyContract != InvocationAuthorizationPolicySourceContract || !recorder.records[0].Valid() {
		t.Fatal("version 6 publication audit missing")
	}
}
