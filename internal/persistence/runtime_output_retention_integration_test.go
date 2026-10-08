package persistence_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

const invalidResultText = `{"answer": "invalid integer"}`

func TestInvalidRuntimeOutputRetentionRecoversWithoutNativeRestart(t *testing.T) {
	for _, boundary := range []string{"before-bind", "after-bind"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			fixture := newRuntimeQuestionFixtureWithSchema(t, ctx, false, map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "integer"}}})
			t.Cleanup(func() {
				if _, err := fixture.pool.Exec(context.Background(), `DELETE FROM invocation_runtime_attempts WHERE isolation_domain_id=$1`, fixture.claim.IsolationDomainID); err != nil {
					t.Error(err)
				}
			})
			transport := &invalidOutputTransport{interruptionTransport: interruptionTransport{questionBridgeTransport: questionBridgeTransport{scope: fixture.target.IsolationDomainID}}}
			objects := &retentionObjects{values: map[string][]byte{}}
			catalog := &retentionCatalog{Repository: fixture.repository, fail: boundary}
			finalizer, err := artifact.NewFinalizer(catalog, objects, objects, artifact.FinalizerConfig{MaximumBytes: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			driverFor := func(repository *persistence.Repository) *reconcile.InvocationRuntimeDriver {
				driver, err := reconcile.NewInvocationRuntimeDriver(repository, transport, reconcile.InvocationRuntimeRequestBuilderFunc(func(target persistence.InvocationRuntimeTarget) (dgruntime.StartRequest, error) {
					return dgruntime.StartRequest{Prompt: "invalid output", OutputSchema: target.OutputSchema, ApprovalMode: dgruntime.ApprovalLocked, SandboxMode: dgruntime.SandboxReadOnly}, nil
				}), transport, transport, transport, finalizer, reconcile.InvocationRuntimeDriverConfig{LeaseDuration: time.Minute, RenewInterval: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				return driver
			}
			if _, err := driverFor(fixture.repository).ApplyClaimed(ctx, fixture.claim, fixture.effect); !errors.Is(err, reconcile.ErrAmbiguousEffect) {
				t.Fatal("retention outage did not remain recoverable", err)
			}
			attempt, err := fixture.repository.GetInvocationRuntimeAttempt(ctx, fixture.claim.IsolationDomainID, fixture.claim.ID)
			if err != nil || attempt.Status != "output_invalid" {
				t.Fatal("retention intent missing", attempt, err)
			}
			if _, err := fixture.repository.CompleteInvocationRuntimeAttempt(ctx, fixture.claim, fixture.effect, map[string]any{"status": "succeeded"}); err == nil {
				t.Fatal("retention became success")
			}
			if _, err := fixture.pool.Exec(ctx, `UPDATE invocation_runtime_attempts SET result=result || '{"artifactId":"art_00000000000000000001"}' WHERE isolation_domain_id=$1 AND operation_id=$2`, fixture.claim.IsolationDomainID, fixture.claim.ID); err == nil {
				t.Fatal("retention intent was mutable")
			}
			db, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := persistence.MigrateDownTo(ctx, db, 60); err == nil {
				t.Fatal("pending retention allowed downgrade")
			}
			db.Close()
			if err := fixture.repository.ScheduleRetry(ctx, fixture.claim, "unknown", "RETENTION_RETRY", time.Now().Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			repository := persistence.NewRepository(fixture.pool)
			worker := reconcile.New(repository, driverFor(repository), "retention-replacement")
			runToTerminal(t, ctx, worker, repository, fixture.claim.IsolationDomainID, fixture.claim.ID, "failed")
			attempt, err = repository.GetInvocationRuntimeAttempt(ctx, fixture.claim.IsolationDomainID, fixture.claim.ID)
			if err != nil || attempt.Status != "failed" || attempt.Result["code"] != "RUNTIME_OUTPUT_INVALID" {
				t.Fatal("failure not settled", attempt, err)
			}
			record, err := repository.GetInvocationArtifactRecord(ctx, fixture.claim.IsolationDomainID, attempt.Result["artifactId"].(string))
			if err != nil || !record.Sensitive || record.MediaType != "text/plain; charset=utf-8" {
				t.Fatal("retained descriptor missing", record, err)
			}
			if string(objects.values[record.ObjectKey]) != invalidResultText || len(objects.values) != 1 || transport.starts != 1 || transport.exports != 0 {
				t.Fatal("retention lost bytes or repeated runtime execution")
			}
			if _, err := fixture.repository.PrepareInvocationRuntimeOutputFailure(ctx, fixture.claim, fixture.effect, 2); err == nil {
				t.Fatal("stale owner regained retention authority")
			}
			var count int
			if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND action='invocation-artifact.bind'`, fixture.claim.IsolationDomainID).Scan(&count); err != nil || count != 1 {
				t.Fatal("artifact binding audit duplicated", count, err)
			}
		})
	}
}

func TestInvalidRuntimeOutputRetentionFencesSourceAndArtifact(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	t.Cleanup(func() {
		if _, err := fixture.pool.Exec(context.Background(), `DELETE FROM invocation_runtime_attempts WHERE isolation_domain_id=$1`, fixture.claim.IsolationDomainID); err != nil {
			t.Error(err)
		}
	})
	// This sequence cannot round-trip through a JSON floating-point number.
	const sequence = uint64(9007199254740993)
	event := persistence.InvocationRuntimeEvent{SourceSequence: sequence, Type: dgruntime.MessageCompletedEvent, Payload: dgruntime.CompletedMessage{Text: invalidResultText, Phase: "final"}.Payload()}
	if _, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, event); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.PrepareInvocationRuntimeOutputFailure(ctx, fixture.claim, fixture.effect, sequence); err == nil {
		t.Fatal("unfinished turn acquired retention authority")
	}
	if _, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, persistence.InvocationRuntimeEvent{SourceSequence: sequence + 1, Type: "lifecycle.succeeded", Payload: map[string]any{"message": "completed"}}); err != nil {
		t.Fatal(err)
	}
	failure, err := fixture.repository.PrepareInvocationRuntimeOutputFailure(ctx, fixture.claim, fixture.effect, sequence)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := fixture.repository.PrepareInvocationRuntimeOutputFailure(ctx, fixture.claim, fixture.effect, 0)
	if err != nil || resumed.Result["sourceSequence"] != "9007199254740993" || !bytes.Equal(resumed.Artifact.Content, failure.Artifact.Content) {
		t.Fatal("source precision was lost", err)
	}
	if _, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, event); err != nil {
		t.Fatal("exact event replay failed", err)
	}
	event.SourceSequence += 2
	if _, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, event); err == nil {
		t.Fatal("frozen source accepted another completed message")
	}
	if _, err := fixture.repository.FailInvocationRuntimeAttempt(ctx, fixture.claim, fixture.effect, failure.Result); err == nil {
		t.Fatal("failure committed before artifact binding")
	}
	if _, err := fixture.repository.PrepareInvocationRuntimeOutputFailure(ctx, fixture.claim, fixture.effect, sequence+1); err == nil {
		t.Fatal("retention source changed")
	}
	for _, change := range []func(*artifact.Binding){
		func(b *artifact.Binding) { b.Record.ID = identity.New("art") },
		func(b *artifact.Binding) { b.Record.Digest = "sha256:" + string(bytes.Repeat([]byte("0"), 64)) },
		func(b *artifact.Binding) { b.Record.SizeBytes++ },
		func(b *artifact.Binding) { b.Record.Sensitive = false },
		func(b *artifact.Binding) { b.Record.Name = "Other" },
		func(b *artifact.Binding) { b.Record.MediaType = "application/json" },
		func(b *artifact.Binding) { b.Record.Kind = "structured-output" },
		func(b *artifact.Binding) { b.FencingToken++ },
		func(b *artifact.Binding) { b.Record.IsolationDomainID = identity.New("iso") },
	} {
		binding := failure.Artifact.Binding
		change(&binding)
		if _, err := fixture.repository.BindInvocationArtifact(ctx, binding); err == nil {
			t.Fatal("substituted retention artifact was bound", binding.Record)
		}
	}
	stale := fixture.claim
	stale.FencingToken++
	if _, err := fixture.repository.PrepareInvocationRuntimeOutputFailure(ctx, stale, fixture.effect, 0); err == nil {
		t.Fatal("stale claim adopted retention")
	}
	foreign := fixture.claim
	foreign.IsolationDomainID = identity.New("iso")
	if _, err := fixture.repository.PrepareInvocationRuntimeOutputFailure(ctx, foreign, fixture.effect, 0); err == nil {
		t.Fatal("foreign claim adopted retention")
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE isolation_domain_id=$1 AND id=$2`, fixture.claim.IsolationDomainID, fixture.claim.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.PrepareInvocationRuntimeOutputFailure(ctx, fixture.claim, fixture.effect, 0); err == nil {
		t.Fatal("expired claim adopted retention")
	}
	if _, err := fixture.repository.BindInvocationArtifact(ctx, failure.Artifact.Binding); err == nil {
		t.Fatal("expired claim bound retention")
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp()+interval '1 minute' WHERE isolation_domain_id=$1 AND id=$2`, fixture.claim.IsolationDomainID, fixture.claim.ID); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.repository.AcceptCancellation(ctx, testIdempotency(fixture.claim.IsolationDomainID, "retention-cancel"), persistence.AcceptCancellationInput{InvocationID: fixture.target.InvocationID, ActorID: "requester", CorrelationID: identity.New("cor")})
	if err != nil || result.Status != http.StatusAccepted {
		t.Fatal("cancellation failed", result.Status, err)
	}
	if _, err := fixture.repository.PrepareInvocationRuntimeOutputFailure(ctx, fixture.claim, fixture.effect, 0); err == nil {
		t.Fatal("cancelled claim adopted retention")
	}
	if _, err := fixture.repository.BindInvocationArtifact(ctx, failure.Artifact.Binding); err == nil {
		t.Fatal("cancelled claim bound retention")
	}
	if _, err := fixture.repository.FailInvocationRuntimeAttempt(ctx, fixture.claim, fixture.effect, failure.Result); err == nil {
		t.Fatal("cancelled claim committed failure")
	}
}

func TestInvalidRuntimeOutputRetentionRejectsWrongSource(t *testing.T) {
	for _, kind := range []string{"commentary", "superseded", "failed", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			fixture := newRuntimeQuestionFixture(t, ctx)
			t.Cleanup(func() {
				if _, err := fixture.pool.Exec(context.Background(), `DELETE FROM invocation_runtime_attempts WHERE isolation_domain_id=$1`, fixture.claim.IsolationDomainID); err != nil {
					t.Error(err)
				}
			})
			phase := "final"
			if kind == "commentary" {
				phase = "commentary"
			}
			events := []persistence.InvocationRuntimeEvent{{SourceSequence: 1, Type: dgruntime.MessageCompletedEvent, Payload: dgruntime.CompletedMessage{Text: invalidResultText, Phase: phase}.Payload()}}
			if kind == "superseded" {
				events = append(events, persistence.InvocationRuntimeEvent{SourceSequence: 2, Type: dgruntime.MessageCompletedEvent, Payload: dgruntime.CompletedMessage{Text: "new answer", Phase: "unspecified"}.Payload()})
			}
			terminal := "lifecycle.succeeded"
			if kind == "failed" || kind == "cancelled" {
				terminal = "lifecycle." + kind
			}
			events = append(events, persistence.InvocationRuntimeEvent{SourceSequence: 3, Type: terminal, Payload: map[string]any{"message": "finished"}})
			for _, event := range events {
				if _, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, event); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := fixture.repository.PrepareInvocationRuntimeOutputFailure(ctx, fixture.claim, fixture.effect, 1); err == nil {
				t.Fatal("invalid source acquired retention authority")
			}
			attempt, err := fixture.repository.GetInvocationRuntimeAttempt(ctx, fixture.claim.IsolationDomainID, fixture.claim.ID)
			if err != nil || attempt.Status != "reserved" {
				t.Fatal("rejected source changed attempt", attempt, err)
			}
		})
	}
}

type invalidOutputTransport struct{ interruptionTransport }

func (transport *invalidOutputTransport) New(execution.RuntimeSession) (reconcile.InvocationRuntimeAdapter, error) {
	return transport, nil
}
func (transport *invalidOutputTransport) Start(context.Context, dgruntime.StartRequest) (dgruntime.Turn, error) {
	transport.starts++
	events := make(chan dgruntime.Event, 3)
	events <- dgruntime.Event{Sequence: 1, Type: "lifecycle.started", Payload: map[string]any{"message": "started"}}
	events <- dgruntime.Event{Sequence: 2, Type: dgruntime.MessageCompletedEvent, Payload: dgruntime.CompletedMessage{Text: invalidResultText, Phase: "final"}.Payload()}
	events <- dgruntime.Event{Sequence: 3, Type: "lifecycle.succeeded", Payload: map[string]any{"message": "completed"}}
	close(events)
	return &completedOutputTurn{interruptionTurn{events: events}}, nil
}

type retentionCatalog struct {
	*persistence.Repository
	fail string
}

func (catalog *retentionCatalog) BindInvocationArtifact(ctx context.Context, binding artifact.Binding) (artifact.Record, error) {
	boundary := catalog.fail
	catalog.fail = ""
	if boundary == "before-bind" {
		return artifact.Record{}, errors.New("catalog unavailable")
	}
	record, err := catalog.Repository.BindInvocationArtifact(ctx, binding)
	if err == nil && boundary == "after-bind" {
		return artifact.Record{}, errors.New("catalog acknowledgement lost")
	}
	return record, err
}

type retentionObjects struct{ values map[string][]byte }

func (objects *retentionObjects) OpenInvocationArtifactObject(_ context.Context, key string) (io.ReadCloser, error) {
	value, ok := objects.values[key]
	if !ok {
		return nil, artifact.ErrInvocationArtifactObjectMissing
	}
	return io.NopCloser(bytes.NewReader(value)), nil
}
func (objects *retentionObjects) PutInvocationArtifactObjectIfAbsent(_ context.Context, key string, reader io.Reader, _ int64, _, _ string) error {
	value, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	if old, ok := objects.values[key]; ok && !bytes.Equal(old, value) {
		return artifact.ErrInvocationArtifactObjectConflict
	}
	objects.values[key] = value
	return nil
}
