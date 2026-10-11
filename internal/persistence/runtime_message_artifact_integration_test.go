package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestLargeRuntimeResultArtifactSurvivesReplacement(t *testing.T) {
	for _, tc := range []struct {
		name    string
		invalid bool
		text    string
		plain   bool
	}{
		{name: "valid"},
		{name: "invalid", invalid: true},
		{name: "plain-nul", text: "before\x00after 界", plain: true},
		{name: "large-nul", text: "before\x00" + strings.Repeat("界", 30000), plain: true},
		{name: "invalid-nul", text: "before\x00after", invalid: true},
		{name: "structured-nul", text: `{"answer":"before\u0000after"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := tc.invalid
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			schema := map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}, "required": []any{"answer"}}
			if tc.plain {
				schema = nil
			}
			fixture := newRuntimeQuestionFixtureWithProfile(t, ctx, false, schema, persistence.GovernedInvocationRuntimeProfile)
			t.Cleanup(func() {
				if _, err := fixture.pool.Exec(context.Background(), `DELETE FROM invocation_runtime_attempts WHERE isolation_domain_id=$1`, fixture.claim.IsolationDomainID); err != nil {
					t.Error(err)
				}
			})
			unbound := dgruntime.MessageArtifact{ArtifactID: identity.Derived("art", fixture.claim.IsolationDomainID+":"+fixture.target.InvocationID+":runtime-message:2"), Digest: "sha256:" + strings.Repeat("0", 64), SizeBytes: 70000, Phase: "final", Preview: "preview"}
			if _, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, persistence.InvocationRuntimeEvent{SourceSequence: 2, Type: dgruntime.MessageArtifactEvent, Payload: unbound.Payload()}); err == nil {
				t.Fatal("unbound snapshot entered journal")
			}
			if _, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, persistence.InvocationRuntimeEvent{SourceSequence: 2, Type: dgruntime.MessageCompletedEvent, Payload: dgruntime.CompletedMessage{Text: strings.Repeat("x", dgruntime.MaximumInlineMessageTextBytes+1), Phase: "final"}.Payload()}); err == nil {
				t.Fatal("large snapshot entered inline journal")
			}

			if _, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, persistence.InvocationRuntimeEvent{SourceSequence: 2, Type: dgruntime.MessageCompletedEvent, Payload: dgruntime.CompletedMessage{Text: "before\x00after", Phase: "final"}.Payload()}); !errors.Is(err, persistence.ErrInvocationRuntimeEventInvalid) {
				t.Fatal("NUL snapshot did not fail before database insertion", err)
			}

			value := map[string]any{"answer": strings.Repeat("retained result ", 10000)}
			if invalid {
				value = map[string]any{"wrong": strings.Repeat("retained result ", 10000)}
			}
			content, _ := json.Marshal(value)
			if tc.text != "" {
				content = []byte(tc.text)
			}
			transport := &largeOutputTransport{interruptionTransport: interruptionTransport{questionBridgeTransport: questionBridgeTransport{scope: fixture.target.IsolationDomainID}}, text: string(content)}
			objects := &retentionObjects{values: map[string][]byte{}}
			finalizer, err := artifact.NewFinalizer(fixture.repository, objects, objects, artifact.FinalizerConfig{MaximumBytes: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			driverFor := func(repository *persistence.Repository) *reconcile.InvocationRuntimeDriver {
				var store reconcile.InvocationRuntimeStore = repository
				if invalid && repository == fixture.repository {
					store = &retainedFailureStore{Repository: repository}
				}
				driver, err := reconcile.NewInvocationRuntimeDriver(store, transport, reconcile.InvocationRuntimeRequestBuilderFunc(func(target persistence.InvocationRuntimeTarget) (dgruntime.StartRequest, error) {
					return dgruntime.StartRequest{Prompt: "large result", OutputSchema: target.OutputSchema, ApprovalMode: dgruntime.ApprovalLocked, SandboxMode: dgruntime.SandboxReadOnly}, nil
				}), transport, transport, transport, finalizer, reconcile.InvocationRuntimeDriverConfig{LeaseDuration: time.Minute, RenewInterval: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				return driver
			}
			result, err := driverFor(fixture.repository).ApplyClaimed(ctx, fixture.claim, fixture.effect)
			terminal := "succeeded"
			if invalid {
				terminal = "failed"
				if !errors.Is(err, reconcile.ErrAmbiguousEffect) {
					t.Fatal("invalid large output did not fail", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			attempt, err := fixture.repository.GetInvocationRuntimeAttempt(ctx, fixture.claim.IsolationDomainID, fixture.claim.ID)
			expectedAttempt := terminal
			if invalid {
				expectedAttempt = "output_invalid"
			}
			if err != nil || attempt.Status != expectedAttempt {
				t.Fatal("unexpected attempt", attempt, err)
			}
			artifactID := ""
			if invalid {
				artifactID = attempt.Result["artifactId"].(string)
			} else {
				if result["schemaVersion"] != "dataground.invocation-artifact-result/v1" || result["output"] != nil {
					t.Fatal("large result returned inline", result)
				}
				artifactID = result["outputArtifact"].(map[string]any)["artifactId"].(string)
			}
			record, err := fixture.repository.GetInvocationArtifactRecord(ctx, fixture.claim.IsolationDomainID, artifactID)
			if err != nil || !record.Sensitive || string(objects.values[record.ObjectKey]) != string(content) || len(objects.values) != 1 {
				t.Fatal("snapshot was lost or duplicated", err)
			}
			events, err := fixture.repository.ListEvents(ctx, fixture.claim.IsolationDomainID, fixture.target.InvocationID, 0)
			if err != nil {
				t.Fatal(err)
			}
			messageEvents := 0
			for _, event := range events {
				encoded, _ := json.Marshal(event.Payload)
				if len(encoded) > 8192 {
					t.Fatal("large content escaped into public journal", event.Type, len(encoded))
				}
				if event.Type == "output.text.delta" && event.Payload["text"] != `before\u0000after` {
					t.Fatal("unsafe delta preview")
				}
				if event.Type == dgruntime.MessageArtifactEvent {
					messageEvents++
					if strings.ContainsRune(event.Payload["preview"].(string), 0) {
						t.Fatal("NUL escaped into JSONB preview")
					}
					if event.Payload["artifactId"] != artifactID || event.Payload["text"] != nil {
						t.Fatal("incorrect snapshot reference")
					}
				}
			}
			if messageEvents != 1 {
				t.Fatal("missing message reference", messageEvents)
			}

			if invalid && len(content) > dgruntime.MaximumInlineMessageTextBytes {
				db, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
				if err != nil {
					t.Fatal(err)
				}
				if err := persistence.MigrateDownTo(ctx, db, 61); err == nil {
					t.Fatal("large pending retention allowed incompatible downgrade")
				}
				db.Close()
			} else if !invalid {
				changed := map[string]any{"schemaVersion": result["schemaVersion"], "status": "succeeded", "outputArtifact": map[string]any{"artifactId": artifactID, "digest": "sha256:" + strings.Repeat("0", 64), "sizeBytes": len(content), "mediaType": "text/plain; charset=utf-8"}}
				if _, err := fixture.repository.CompleteInvocationRuntimeAttempt(ctx, fixture.claim, fixture.effect, changed); err == nil {
					t.Fatal("changed result artifact was accepted")
				}
			}
			if err := fixture.repository.ScheduleRetry(ctx, fixture.claim, "unknown", "WORKER_REPLACED", time.Now().Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			replacement := persistence.NewRepository(fixture.pool)
			runToTerminal(t, ctx, reconcile.New(replacement, driverFor(replacement), "large-output-replacement"), replacement, fixture.claim.IsolationDomainID, fixture.claim.ID, terminal)
			if transport.starts != 1 || transport.exports != 0 {
				t.Fatal("replacement repeated runtime effects")
			}
		})
	}
}

type largeOutputTransport struct {
	interruptionTransport
	text string
}

func (transport *largeOutputTransport) New(execution.RuntimeSession) (reconcile.InvocationRuntimeAdapter, error) {
	return transport, nil
}
func (transport *largeOutputTransport) Start(context.Context, dgruntime.StartRequest) (dgruntime.Turn, error) {
	transport.starts++
	events := make(chan dgruntime.Event, 4)
	if strings.ContainsRune(transport.text, 0) {
		events <- dgruntime.Event{Sequence: 1, Type: "output.text.delta", Payload: map[string]any{"text": "before\x00after"}}
	} else {
		events <- dgruntime.Event{Sequence: 1, Type: "lifecycle.started", Payload: map[string]any{"message": "started"}}
	}
	events <- dgruntime.Event{Sequence: 2, Type: dgruntime.MessageCompletedEvent, Payload: dgruntime.CompletedMessage{Text: transport.text, Phase: "final"}.Payload()}
	events <- dgruntime.Event{Sequence: 3, Type: "lifecycle.succeeded", Payload: map[string]any{"message": "completed"}}
	close(events)
	return &completedOutputTurn{interruptionTurn{events: events}}, nil
}

type retainedFailureStore struct{ *persistence.Repository }

func (store *retainedFailureStore) FailInvocationRuntimeAttempt(context.Context, persistence.OperationClaim, persistence.EffectRecord, map[string]any) (persistence.InvocationRuntimeAttempt, error) {
	return persistence.InvocationRuntimeAttempt{}, errors.New("failure receipt unavailable")
}
