package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestInvocationRuntimeDriverPublishesDeclaredArtifactsBeforeSuccess(t *testing.T) {
	claim, effect, target := runtimeDriverFixture()
	declaration := dgruntime.ArtifactDeclaration{
		ID:          "report",
		Name:        "Report",
		SandboxPath: "/workspace/report.json",
		MediaType:   "application/json",
		Kind:        "file",
	}
	content := []byte("{\"answer\":42}")
	store := &runtimeStoreStub{target: target}
	provider := &runtimeProviderStub{
		observation: execution.Observation{
			IsolationDomainID: target.IsolationDomainID,
			ExecutionID:       "exe_runtime",
			State:             "ready",
		},
		exportResults: []execution.ExportResult{{
			IsolationDomainID: target.IsolationDomainID,
			ExecutionID:       "exe_runtime",
			Content:           content,
		}},
	}
	finalizer := &runtimeArtifactFinalizerStub{}
	driver, err := NewInvocationRuntimeDriver(
		store,
		&runtimeAuthorizerStub{},
		InvocationRuntimeRequestBuilderFunc(func(
			persistence.InvocationRuntimeTarget,
		) (dgruntime.StartRequest, error) {
			return dgruntime.StartRequest{
				Prompt:       "persisted prompt",
				Artifacts:    []dgruntime.ArtifactDeclaration{declaration},
				ApprovalMode: dgruntime.ApprovalLocked,
				SandboxMode:  dgruntime.SandboxWorkspaceWrite,
			}, nil
		}),
		&runtimeExecutionSourceStub{value: execution.Execution{
			IsolationDomainID: target.IsolationDomainID,
			ID:                "exe_runtime",
			State:             "ready",
		}},
		provider,
		&runtimeAdapterFactoryStub{adapter: &runtimeAdapterStub{turn: &runtimeTurnStub{
			events: runtimeEvents(
				dgruntime.Event{Sequence: 1, Type: "output.text.delta", Payload: map[string]any{"text": "done"}},
				dgruntime.Event{Sequence: 2, Type: "lifecycle.succeeded", Payload: map[string]any{"message": "finished"}},
			),
		}}},
		finalizer,
		InvocationRuntimeDriverConfig{LeaseDuration: time.Minute, RenewInterval: time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := driver.ApplyClaimed(context.Background(), claim, effect)
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "succeeded" || store.completeCalls != 1 ||
		store.renewCalls != 2 || len(provider.exports) != 1 || len(finalizer.values) != 1 {
		t.Fatalf("artifact completion = result %#v, store %#v, provider %#v, finalizer %#v", result, store, provider, finalizer)
	}
	value := finalizer.values[0]
	digest := sha256.Sum256(content)
	wantDigest := "sha256:" + hex.EncodeToString(digest[:])
	wantID := identity.Derived("art", target.IsolationDomainID+":"+target.InvocationID+":"+declaration.ID)
	if value.Binding.Record.ID != wantID ||
		value.Binding.Record.Digest != wantDigest ||
		value.Binding.Record.SizeBytes != int64(len(content)) ||
		!value.Binding.Record.Sensitive ||
		value.Binding.Record.OperationID != claim.ID ||
		value.Binding.Record.EffectID != effect.EffectID ||
		value.Binding.ActorID != claim.ActorID ||
		value.Binding.CorrelationID != claim.CorrelationID ||
		value.Binding.LeaseOwner != claim.LeaseOwner ||
		value.Binding.FencingToken != claim.FencingToken ||
		string(value.Content) != string(content) {
		t.Fatalf("artifact finalization = %#v", value)
	}
}

func TestInvocationRuntimeDriverRejectsMismatchedArtifactExport(t *testing.T) {
	claim, effect, target := runtimeDriverFixture()
	store := &runtimeStoreStub{target: target}
	provider := &runtimeProviderStub{
		observation: execution.Observation{
			IsolationDomainID: target.IsolationDomainID,
			ExecutionID:       "exe_runtime",
			State:             "ready",
		},
		exportResults: []execution.ExportResult{{
			IsolationDomainID: "iso_other",
			ExecutionID:       "exe_runtime",
			Content:           []byte("content"),
		}},
	}
	finalizer := &runtimeArtifactFinalizerStub{}
	driver, err := NewInvocationRuntimeDriver(
		store,
		&runtimeAuthorizerStub{},
		InvocationRuntimeRequestBuilderFunc(func(
			persistence.InvocationRuntimeTarget,
		) (dgruntime.StartRequest, error) {
			return dgruntime.StartRequest{
				Prompt: "persisted prompt",
				Artifacts: []dgruntime.ArtifactDeclaration{{
					ID: "report", Name: "Report", SandboxPath: "/workspace/report",
					MediaType: "application/json", Kind: "file",
				}},
				ApprovalMode: dgruntime.ApprovalLocked,
				SandboxMode:  dgruntime.SandboxWorkspaceWrite,
			}, nil
		}),
		&runtimeExecutionSourceStub{value: execution.Execution{
			IsolationDomainID: target.IsolationDomainID,
			ID:                "exe_runtime",
		}},
		provider,
		&runtimeAdapterFactoryStub{adapter: &runtimeAdapterStub{turn: &runtimeTurnStub{
			events: runtimeEvents(dgruntime.Event{Sequence: 1, Type: "output.text.delta", Payload: map[string]any{"text": "done"}}),
		}}},
		finalizer,
		InvocationRuntimeDriverConfig{LeaseDuration: time.Minute, RenewInterval: time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = driver.ApplyClaimed(context.Background(), claim, effect)
	if !errors.Is(err, ErrAmbiguousEffect) ||
		!errors.Is(err, ErrInvocationRuntimeTargetMismatch) {
		t.Fatalf("mismatched export = %v", err)
	}
	if store.completeCalls != 0 || store.failCalls != 0 || len(finalizer.values) != 0 {
		t.Fatalf("mismatched export crossed publication boundary: store %#v, finalizer %#v", store, finalizer)
	}
}

func TestInvocationRuntimeDriverRejectsTypedNilArtifactFinalizer(t *testing.T) {
	claim, _, _ := runtimeDriverFixture()
	var finalizer *runtimeArtifactFinalizerStub
	driver, err := NewInvocationRuntimeDriver(
		&runtimeStoreStub{},
		&runtimeAuthorizerStub{},
		InvocationRuntimeRequestBuilderFunc(func(
			persistence.InvocationRuntimeTarget,
		) (dgruntime.StartRequest, error) {
			return dgruntime.StartRequest{}, nil
		}),
		&runtimeExecutionSourceStub{},
		&runtimeProviderStub{},
		&runtimeAdapterFactoryStub{},
		finalizer,
		InvocationRuntimeDriverConfig{LeaseDuration: claim.LeaseExpiresAt.Sub(time.Now()), RenewInterval: time.Second},
	)
	if driver != nil || err == nil {
		t.Fatalf("typed nil artifact finalizer = (%#v, %v)", driver, err)
	}
}

var _ InvocationRuntimeArtifactFinalizer = (*runtimeArtifactFinalizerStub)(nil)
