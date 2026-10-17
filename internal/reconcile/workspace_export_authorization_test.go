package reconcile

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/workspace"
)

func workspaceExportRequestFixture() authz.WorkspaceChangeExportRequest {
	s := sourceRequestFixture()
	r := authz.WorkspaceChangeExportRequest{Source: s.Use.Record, ServiceID: s.ServiceID, RevisionID: s.Use.RevisionID, InvocationID: s.InvocationID, OperationID: s.Use.OperationID, ExecutionID: "exe_" + strings.Repeat("a", 20), ActorID: s.Use.ActorID, CorrelationID: s.Use.CorrelationID, PlanDigest: s.PlanDigest, OwnerBinding: "sha256:" + strings.Repeat("d", 64), RuntimeToken: 2, FencingToken: 3, Phase: "capture"}
	r.EffectID = identity.Derived("eff", r.Source.IsolationDomainID+":invocation-execution:"+r.OperationID+":run-invocation")
	return r
}
func workspaceExportReceiptFixture(r authz.WorkspaceChangeExportRequest) workspace.ChangeSetReceipt {
	return workspace.ChangeSetReceipt{SchemaVersion: workspace.ChangeSetContract, Digest: "sha256:" + strings.Repeat("e", 64), SizeBytes: 256, BaseArchiveDigest: r.Source.ArchiveDigest, ResultArchiveDigest: "sha256:" + strings.Repeat("f", 64), ChangeCount: 1, EntryCount: 1, SourceBytes: 1}
}

func TestWorkspaceExportCedarRequiresVersionSevenAndExactContext(t *testing.T) {
	r := workspaceExportRequestFixture()
	scope := InvocationAuthorizationPolicyScope{r.Source.IsolationDomainID, r.ServiceID, r.RevisionID}
	text := `permit(principal in DataGround::Role::"invoker",action == DataGround::Action::"export-changes",resource == DataGround::WorkspaceSource::"` + r.Source.IsolationDomainID + ":" + r.Source.WorkspaceID + ":" + r.Source.ID + `") when { context.phase == "capture" && !context.changes.present && context.executionID == "` + r.ExecutionID + `" && context.ownerBinding == "` + r.OwnerBinding + `" && context.runtimeToken == 2 && context.fencingToken == 3 && context.planDigest == "` + r.PlanDigest + `" && context.archiveDigest == "` + r.Source.ArchiveDigest + `" && context.storageProfileDigest == "` + r.Source.StorageProfileDigest + `" && context.operationID == "` + r.OperationID + `" && context.invocationID == "` + r.InvocationID + `" && context.effectID == "` + r.EffectID + `" && context.correlationID == "` + r.CorrelationID + `" };`
	policy, err := NewInvocationAuthorizationPolicyWithWorkspaceExportEntities(scope, "export-test", CanonicalWorkspaceExportCedarSchema(), []byte(text), canonicalInvocationEntityFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	evaluator := &WorkspaceExportCedarEvaluator{}
	if got := evaluator.EvaluateWorkspaceChangeExport(context.Background(), sourcePolicyRecord(policy), r); got != authz.OutcomeAllowed {
		t.Fatal("exact export denied", got)
	}
	for name, change := range map[string]func(*authz.WorkspaceChangeExportRequest){
		"scope": func(r *authz.WorkspaceChangeExportRequest) {
			r.Source.IsolationDomainID = "iso_" + strings.Repeat("b", 20)
		},
		"workspace": func(r *authz.WorkspaceChangeExportRequest) { r.Source.WorkspaceID = "wsp_" + strings.Repeat("b", 20) },
		"source":    func(r *authz.WorkspaceChangeExportRequest) { r.Source.ID = "src_" + strings.Repeat("b", 20) },
		"archive": func(r *authz.WorkspaceChangeExportRequest) {
			r.Source.ArchiveDigest = "sha256:" + strings.Repeat("b", 64)
		},
		"storage": func(r *authz.WorkspaceChangeExportRequest) {
			r.Source.StorageProfileDigest = "sha256:" + strings.Repeat("c", 64)
		},
		"actor":         func(r *authz.WorkspaceChangeExportRequest) { r.ActorID = "other" },
		"correlation":   func(r *authz.WorkspaceChangeExportRequest) { r.CorrelationID = "cor_" + strings.Repeat("b", 20) },
		"service":       func(r *authz.WorkspaceChangeExportRequest) { r.ServiceID = "svc_" + strings.Repeat("b", 20) },
		"revision":      func(r *authz.WorkspaceChangeExportRequest) { r.RevisionID = "rev_" + strings.Repeat("b", 20) },
		"invocation":    func(r *authz.WorkspaceChangeExportRequest) { r.InvocationID = "inv_" + strings.Repeat("b", 20) },
		"operation":     func(r *authz.WorkspaceChangeExportRequest) { r.OperationID = "op_" + strings.Repeat("b", 20) },
		"effect":        func(r *authz.WorkspaceChangeExportRequest) { r.EffectID = "eff_" + strings.Repeat("b", 20) },
		"execution":     func(r *authz.WorkspaceChangeExportRequest) { r.ExecutionID = "exe_" + strings.Repeat("b", 20) },
		"plan":          func(r *authz.WorkspaceChangeExportRequest) { r.PlanDigest = "sha256:" + strings.Repeat("b", 64) },
		"owner":         func(r *authz.WorkspaceChangeExportRequest) { r.OwnerBinding = "sha256:" + strings.Repeat("b", 64) },
		"runtime token": func(r *authz.WorkspaceChangeExportRequest) { r.RuntimeToken++ },
		"current token": func(r *authz.WorkspaceChangeExportRequest) { r.FencingToken++ },
		"phase": func(r *authz.WorkspaceChangeExportRequest) {
			r.Phase = "retain"
			r.Changes = workspaceExportReceiptFixture(*r)
		},
		"unexpected receipt": func(r *authz.WorkspaceChangeExportRequest) { r.Changes = workspaceExportReceiptFixture(*r) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := r
			change(&changed)
			if evaluator.EvaluateWorkspaceChangeExport(context.Background(), sourcePolicyRecord(policy), changed) == authz.OutcomeAllowed {
				t.Fatal("substituted export allowed")
			}
		})
	}
	for _, old := range []struct {
		schema      []byte
		constructor func(InvocationAuthorizationPolicyScope, string, []byte, []byte, []byte) (InvocationAuthorizationPolicy, error)
	}{
		{CanonicalInvocationCedarEntitySchema(), NewInvocationAuthorizationPolicyWithEntities},
		{CanonicalInvocationCedarApprovalSchema(), NewInvocationAuthorizationPolicyWithApprovalEntities},
		{CanonicalInvocationCedarQuestionSchema(), NewInvocationAuthorizationPolicyWithQuestionEntities},
		{CanonicalPublicationCedarSchema(), NewInvocationAuthorizationPolicyWithPublicationEntities},
		{CanonicalSourceCedarSchema(), NewInvocationAuthorizationPolicyWithSourceEntities},
	} {
		p, err := old.constructor(scope, "old-wildcard", old.schema, []byte(`permit(principal,action,resource);`), canonicalInvocationEntityFixture(t))
		if err != nil {
			t.Fatal(err)
		}
		if got := evaluator.EvaluateWorkspaceChangeExport(context.Background(), sourcePolicyRecord(p), r); got != authz.OutcomeDenied {
			t.Fatal("old policy gained export", p.Contract, got)
		}
	}
	old, err := NewInvocationAuthorizationPolicy(scope, "v1", CanonicalInvocationCedarSchema(), []byte(`permit(principal,action,resource);`))
	if err != nil {
		t.Fatal(err)
	}
	if got := evaluator.EvaluateWorkspaceChangeExport(context.Background(), sourcePolicyRecord(old), r); got != authz.OutcomeDenied {
		t.Fatal("v1 granted export", got)
	}
	for _, schema := range [][]byte{CanonicalSourceCedarSchema(), []byte(`{}`)} {
		bad, err := NewInvocationAuthorizationPolicyWithWorkspaceExportEntities(scope, "wrong-schema", schema, []byte(`permit(principal,action,resource);`), canonicalInvocationEntityFixture(t))
		if err != nil {
			t.Fatal(err)
		}
		if got := evaluator.EvaluateWorkspaceChangeExport(context.Background(), sourcePolicyRecord(bad), r); got != authz.OutcomeUnavailable {
			t.Fatal("wrong schema allowed", got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := evaluator.EvaluateWorkspaceChangeExport(ctx, sourcePolicyRecord(policy), r); got != authz.OutcomeUnavailable {
		t.Fatal("cancelled evaluation", got)
	}
}

func TestWorkspaceExportRetentionBindsReceiptAndPreservesPriorActions(t *testing.T) {
	r := workspaceExportRequestFixture()
	r.Phase = "retain"
	r.Changes = workspaceExportReceiptFixture(r)
	scope := InvocationAuthorizationPolicyScope{r.Source.IsolationDomainID, r.ServiceID, r.RevisionID}
	policy, err := NewInvocationAuthorizationPolicyWithWorkspaceExportEntities(scope, "retention", CanonicalWorkspaceExportCedarSchema(), []byte(`permit(principal,action,resource) when { context.phase == "retain" && context.changes.present && context.changes.digest == "`+r.Changes.Digest+`" && context.changes.resultArchiveDigest == "`+r.Changes.ResultArchiveDigest+`" && context.changes.sizeBytes == 256 && context.changes.changeCount == 1 && context.changes.entryCount == 1 && context.changes.sourceBytes == 1 };`), canonicalInvocationEntityFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	e := &WorkspaceExportCedarEvaluator{}
	if got := e.EvaluateWorkspaceChangeExport(context.Background(), sourcePolicyRecord(policy), r); got != authz.OutcomeAllowed {
		t.Fatal("exact receipt denied", got)
	}
	for _, mutate := range []func(*workspace.ChangeSetReceipt){func(c *workspace.ChangeSetReceipt) { c.Digest = "sha256:" + strings.Repeat("a", 64) }, func(c *workspace.ChangeSetReceipt) { c.ResultArchiveDigest = "sha256:" + strings.Repeat("a", 64) }, func(c *workspace.ChangeSetReceipt) { c.SizeBytes++ }, func(c *workspace.ChangeSetReceipt) { c.ChangeCount++ }, func(c *workspace.ChangeSetReceipt) { c.EntryCount++ }, func(c *workspace.ChangeSetReceipt) { c.SourceBytes++ }, func(c *workspace.ChangeSetReceipt) { c.BaseArchiveDigest = "sha256:" + strings.Repeat("b", 64) }} {
		changed := r
		mutate(&changed.Changes)
		if got := e.EvaluateWorkspaceChangeExport(context.Background(), sourcePolicyRecord(policy), changed); got == authz.OutcomeAllowed {
			t.Fatal("changed receipt allowed")
		}
	}
	broad, err := NewInvocationAuthorizationPolicyWithWorkspaceExportEntities(scope, "explicit-v7", CanonicalWorkspaceExportCedarSchema(), []byte(`permit(principal,action,resource);`), canonicalInvocationEntityFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := (&SourceCedarEvaluator{}).EvaluateSourceRead(context.Background(), sourcePolicyRecord(broad), sourceRequestFixture()); got != authz.OutcomeAllowed {
		t.Fatal("v7 source read denied", got)
	}
	p := publicationRequestFixture()
	broad.IsolationDomainID = p.IsolationDomainID
	broad.ServiceID = p.ServiceID
	broad.RevisionID = p.RevisionID
	p.PolicyDigest = "sha256:" + hex.EncodeToString(broad.Digest[:])
	recorder := &publicationDecisionRecorder{}
	a, err := NewPublicationAuthorizer(publicationPolicySource{policy: broad}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AuthorizePublication(context.Background(), p); err != nil {
		t.Fatal("v7 publication denied", err)
	}
	if len(recorder.records) != 1 || !recorder.records[0].Valid() {
		t.Fatal("v7 publication audit invalid")
	}
}
