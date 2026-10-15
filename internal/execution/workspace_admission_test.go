package execution

import (
	"context"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/workspace"
)

type workspaceAdmissionPlans struct {
	ExecutionPlanStore
	plan ExecutionPlan
}

func (p workspaceAdmissionPlans) GetExecutionPlan(context.Context, string, string) (ExecutionPlan, error) {
	return p.plan, nil
}

func TestWorkspaceAdmissionRequiresExactSourcePlanAndProviderAuthority(t *testing.T) {
	for _, mode := range []string{"valid", "source", "scope", "revision", "image", "source-free", "default", "entry-denial", "effect-denial"} {
		t.Run(mode, func(t *testing.T) {
			_, bundles, provider, request, expected, _ := preparedAdmission(t)
			expected.SchemaVersion = ExecutionPlanSchemaV2
			expected.WorkspaceSource = &workspace.SourceRecord{SchemaVersion: workspace.SourceRecordV1, IsolationDomainID: expected.IsolationDomainID, WorkspaceID: "wsp_0123456789abcdefghij", ID: "src_0123456789abcdefghij", ArchiveDigest: "sha256:" + strings.Repeat("a", 64), StorageProfileDigest: "sha256:" + strings.Repeat("b", 64), ArchiveBytes: 2048, EntryCount: 1, SourceBytes: 1}
			request.ActorID, request.CorrelationID = "operator", "cor_0123456789abcdefghij"
			plan := expected
			source := *expected.WorkspaceSource
			plan.WorkspaceSource = &source
			credentials := &recordingProviderCredentialAuthorizer{}
			switch mode {
			case "source":
				plan.WorkspaceSource.ID = "src_00000000000000000001"
			case "scope":
				plan.IsolationDomainID = "iso_00000000000000000001"
			case "revision":
				plan.RevisionID = "rev_00000000000000000001"
			case "image":
				plan.ImageReference += "different"
			case "source-free":
				plan.SchemaVersion, plan.WorkspaceSource = ExecutionPlanSchemaV1, nil
			case "entry-denial":
				credentials.denyPhase = ProviderCredentialPhaseAdmission
			case "effect-denial":
				credentials.denyPhase = ProviderCredentialPhaseEffect
			}
			plans := workspaceAdmissionPlans{plan: plan}
			admission, err := NewWorkspaceCredentialMediatedAdmission(plans, bundles, provider, credentials, expected)
			if mode == "default" {
				admission, err = NewCredentialMediatedAdmission(plans, bundles, provider, credentials)
			}
			if err != nil {
				t.Fatal(err)
			}
			// The constructor retains a digest, never the caller's mutable source pointer.
			expected.WorkspaceSource.ID = "src_99999999999999999999"
			_, err = admission.Admit(context.Background(), request)
			if mode == "valid" {
				if err != nil || len(provider.creates) != 1 || len(credentials.uses) != 2 {
					t.Fatal("exact source placement failed", err)
				}
			} else if err == nil || len(provider.creates) != 0 || (mode != "effect-denial" && len(provider.placements) != 0) {
				t.Fatal("invalid source plan or authority reached provider", err)
			}
		})
	}
}
