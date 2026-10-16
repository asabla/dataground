package persistence_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/execution"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/asabla/dataground/internal/workspace"
	"github.com/jackc/pgx/v5/pgxpool"
)

type changeArtifactObjects interface {
	artifact.ObjectReader
	artifact.ObjectWriter
}
type observedChangeObjects struct {
	changeArtifactObjects
	writes, reads atomic.Int64
	loseWrite     bool
	corruptRead   bool
	afterRead     func()
	afterWrite    func()
}

func (s *observedChangeObjects) OpenInvocationArtifactObject(ctx context.Context, key string) (io.ReadCloser, error) {
	s.reads.Add(1)
	if s.corruptRead {
		return io.NopCloser(strings.NewReader("wrong bytes")), nil
	}
	object, err := s.changeArtifactObjects.OpenInvocationArtifactObject(ctx, key)
	if s.afterRead != nil {
		s.afterRead()
	}
	return object, err
}
func (s *observedChangeObjects) PutInvocationArtifactObjectIfAbsent(ctx context.Context, key string, r io.Reader, size int64, digest, media string) error {
	s.writes.Add(1)
	err := s.changeArtifactObjects.PutInvocationArtifactObjectIfAbsent(ctx, key, r, size, digest, media)
	if err == nil && s.afterWrite != nil {
		s.afterWrite()
	}
	if err == nil && s.loseWrite {
		return errors.New("lost test acknowledgement")
	}
	return err
}

// Called from the real source catalog/grant fixture, also selected by the live
// S3 conformance workflow. This second invocation owns independent claim state.
func testWorkspaceChangeRetention(t *testing.T, ctx context.Context, pool *pgxpool.Pool, source workspace.SourceRecord, service string, sourceObjects workspace.SourceObjects, objects changeArtifactObjects) {
	t.Helper()
	repo := persistence.NewRepository(pool)
	scope, invocationID, correlation := source.IsolationDomainID, identity.New("inv"), identity.New("cor")
	if _, err := repo.AcceptInvocation(ctx, testIdempotency(scope, "workspace-change-invocation"), persistence.AcceptInvocationInput{ID: invocationID, ServiceID: service, Alias: "source", Input: map[string]any{"prompt": "change source"}, ActorID: "source-requester", CorrelationID: correlation, Deadline: time.Now().Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	worker := reconcile.New(repo, reconcile.NewReferenceDriver(pool), "change-setup")
	for range 2 {
		if ran, err := worker.RunOne(ctx, persistence.OperationKindInvocation); err != nil || !ran {
			t.Fatal("advance change invocation", ran, err)
		}
	}
	claim, err := repo.ClaimNext(ctx, persistence.OperationKindInvocation, "change-worker", time.Minute)
	if err != nil || claim == nil || claim.ResourceID != invocationID {
		t.Fatal("claim change invocation", err)
	}
	effect, err := repo.PrepareEffect(ctx, *claim, "run-invocation", sha256.Sum256([]byte("change-runtime")))
	if err != nil {
		t.Fatal(err)
	}
	executions := executionpostgres.New(pool)
	placement, err := executions.ReservePlacement(ctx, execution.PlacementRequest{IsolationDomainID: scope, OperationID: claim.ID, RequiredCapabilities: []string{"codex.app-server"}})
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.ExecutionRef{IsolationDomainID: scope, ID: identity.New("exe")}
	if err := executions.SaveExecution(ctx, execution.ExecutionRecord{Execution: execution.Execution{IsolationDomainID: scope, ID: ref.ID, GatewayID: placement.GatewayID, State: "ready"}, OperationID: claim.ID, PlacementID: placement.ID, SandboxName: "change-retention"}); err != nil {
		t.Fatal(err)
	}
	preparation, err := repo.BeginInvocationWorkspacePreparation(ctx, *claim, effect, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := repo.ReadPreparingWorkspaceSource(ctx, *claim, effect, sourceObjects, preparation, &reconcile.SourceCedarEvaluator{})
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	receipt, err := base.Materialize(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteInvocationWorkspacePreparation(ctx, *claim, effect, preparation, receipt, &reconcile.SourceCedarEvaluator{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "workspace/src/private-file"), []byte("retained\x00\xff"), 0o600); err != nil {
		t.Fatal(err)
	}
	final, err := workspace.Capture(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := workspace.Compare(base, final)
	if err != nil {
		t.Fatal(err)
	}
	binding := workspace.ChangeExportBinding{Record: workspace.ChangeExportRecord{SchemaVersion: workspace.ChangeExportContract, Source: source, InvocationID: invocationID, OperationID: claim.ID, EffectID: effect.EffectID, ExecutionID: ref.ID, PlanDigest: preparation.PlanDigest, Changes: changes.Receipt()}, ActorID: "source-requester", CorrelationID: correlation, LeaseOwner: claim.LeaseOwner, FencingToken: claim.FencingToken, StateMachineVersion: 2}
	observed := &observedChangeObjects{changeArtifactObjects: objects, loseWrite: true}
	retain := func(b workspace.ChangeExportBinding) (workspace.ChangeExportEntry, error) {
		return workspace.RetainChanges(ctx, persistence.NewRepository(pool), observed, observed, b, changes)
	}
	if entry, err := retain(binding); err == nil || entry.Ready || observed.reads.Load() != 0 || observed.writes.Load() != 0 {
		t.Fatal("export before runtime attempt", err)
	}
	if _, err := repo.BeginInvocationRuntimeAttempt(ctx, *claim, effect); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CompleteWorkspaceChangeExport(ctx, binding); err == nil {
		t.Fatal("completion without intent")
	}
	for name, mutate := range map[string]func(*workspace.ChangeExportBinding){
		"scope":           func(b *workspace.ChangeExportBinding) { b.Record.Source.IsolationDomainID = identity.New("iso") },
		"invocation":      func(b *workspace.ChangeExportBinding) { b.Record.InvocationID = identity.New("inv") },
		"execution":       func(b *workspace.ChangeExportBinding) { b.Record.ExecutionID = identity.New("exe") },
		"source":          func(b *workspace.ChangeExportBinding) { b.Record.Source.ID = identity.New("src") },
		"source metadata": func(b *workspace.ChangeExportBinding) { b.Record.Source.EntryCount++ },
		"plan":            func(b *workspace.ChangeExportBinding) { b.Record.PlanDigest = "sha256:" + strings.Repeat("a", 64) },
		"actor":           func(b *workspace.ChangeExportBinding) { b.ActorID = "other" },
		"correlation":     func(b *workspace.ChangeExportBinding) { b.CorrelationID = identity.New("cor") },
		"owner":           func(b *workspace.ChangeExportBinding) { b.LeaseOwner = "other" },
		"token":           func(b *workspace.ChangeExportBinding) { b.FencingToken++ },
	} {
		t.Run(name, func(t *testing.T) {
			wrong := binding
			mutate(&wrong)
			if _, err := retain(wrong); err == nil || observed.reads.Load() != 0 || observed.writes.Load() != 0 {
				t.Fatal("substitution reached storage", err)
			}
		})
	}
	inject := func(action string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_change_export_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='`+action+`' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_change_export_audit BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION reject_change_export_audit()`); err != nil {
			t.Fatal(err)
		}
	}
	clearFailure := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `DROP FUNCTION reject_change_export_audit() CASCADE`); err != nil {
			t.Fatal(err)
		}
	}
	defer pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_change_export_audit() CASCADE`)
	inject("workspace.changes.export.prepared")
	if _, err := retain(binding); err == nil || observed.writes.Load() != 0 {
		t.Fatal("failed intent audit reached storage")
	}
	if _, err := repo.GetWorkspaceChangeExport(ctx, scope, claim.ID); !errors.Is(err, workspace.ErrChangeExportMissing) {
		t.Fatal("failed intent retained", err)
	}
	clearFailure()
	setLease := func(expired bool) {
		t.Helper()
		duration := "1 minute"
		if expired {
			duration = "-1 second"
		}
		if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp()+$3::interval WHERE isolation_domain_id=$1 AND id=$2`, scope, claim.ID, duration); err != nil {
			t.Fatal(err)
		}
	}
	// Lose the lease after the initial claim check and missing-object observation.
	observed.afterRead = func() { setLease(true) }
	if entry, err := retain(binding); err == nil || entry.Ready || observed.writes.Load() != 0 {
		t.Fatal("expired claim wrote an object", err)
	}
	observed.afterRead = nil
	setLease(false)
	// A successful external write can outlive its lease. Keep its intent pending.
	observed.afterWrite = func() { setLease(true) }
	if entry, err := retain(binding); err == nil || entry.Ready || observed.writes.Load() != 1 {
		t.Fatal("expired completion confirmed export", err)
	}
	observed.afterWrite = nil
	setLease(false)
	inject("workspace.changes.export.completed")
	if entry, err := retain(binding); err == nil || entry.Ready || observed.writes.Load() != 1 {
		t.Fatal("failed completion audit confirmed export", err)
	}
	entry, err := repo.GetWorkspaceChangeExport(ctx, scope, claim.ID)
	if err != nil || entry.Ready || entry.Record != binding.Record {
		t.Fatal("pending intent lost", err)
	}
	if _, err := repo.GetInvocationArtifactRecord(ctx, scope, binding.Record.ArtifactRecord().ID); !errors.Is(err, artifact.ErrInvocationArtifactMissing) {
		t.Fatal("failed completion exposed artifact", err)
	}
	clearFailure()
	old := binding
	binding.LeaseOwner = "replacement-change-worker"
	binding.FencingToken++
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_owner=$3,lease_token=$4,lease_expires_at=clock_timestamp()+interval '1 minute' WHERE isolation_domain_id=$1 AND id=$2`, scope, claim.ID, binding.LeaseOwner, binding.FencingToken); err != nil {
		t.Fatal(err)
	}
	before := observed.reads.Load()
	if _, err := retain(old); err == nil || observed.reads.Load() != before || observed.writes.Load() != 1 {
		t.Fatal("stale worker retained authority")
	}
	entry, err = retain(binding)
	if err != nil || !entry.Ready || observed.writes.Load() != 1 {
		t.Fatal("replacement did not recover retained object without another write", err)
	}
	artifactRecord, err := repo.GetInvocationArtifactRecord(ctx, scope, binding.Record.ArtifactRecord().ID)
	if err != nil || artifactRecord != binding.Record.ArtifactRecord() {
		t.Fatal("governed artifact mismatch", err)
	}
	object, err := objects.OpenInvocationArtifactObject(ctx, artifactRecord.ObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := io.ReadAll(object)
	closeErr := object.Close()
	if err != nil || closeErr != nil {
		t.Fatal("retained object read", err, closeErr)
	}
	restored, err := workspace.ApplyChanges(base, encoded, artifactRecord.Digest)
	if err != nil {
		t.Fatal("retained changes do not apply", err)
	}
	var expected, actual bytes.Buffer
	if err := final.WriteArchive(ctx, &expected); err != nil {
		t.Fatal(err)
	}
	if err := restored.WriteArchive(ctx, &actual); err != nil || !bytes.Equal(actual.Bytes(), expected.Bytes()) {
		t.Fatal("retained final tree mismatch", err)
	}
	replacement := *claim
	replacement.LeaseOwner = binding.LeaseOwner
	replacement.FencingToken = binding.FencingToken
	if _, err := repo.BeginInvocationRuntimeAttempt(ctx, replacement, effect); err == nil {
		t.Fatal("export recovery restarted original runtime")
	}
	var originalToken int64
	if err := pool.QueryRow(ctx, `SELECT fencing_token FROM invocation_runtime_attempts WHERE isolation_domain_id=$1 AND operation_id=$2`, scope, claim.ID).Scan(&originalToken); err != nil || originalToken != old.FencingToken {
		t.Fatal("export rewrote runtime authority", err)
	}
	for _, sql := range []string{`DELETE FROM invocation_workspace_change_exports WHERE isolation_domain_id=$1`, `UPDATE invocation_workspace_change_exports SET fencing_token=fencing_token+1 WHERE isolation_domain_id=$1`, `DELETE FROM invocation_workspace_change_completions WHERE isolation_domain_id=$1`, `UPDATE invocation_workspace_change_completions SET fencing_token=fencing_token+1 WHERE isolation_domain_id=$1`} {
		if _, err := pool.Exec(ctx, sql, scope); err == nil {
			t.Fatal("export evidence mutable")
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE isolation_domain_id=$1 AND id=$2`, scope, claim.ID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := retain(binding)
			if err == nil && !result.Ready {
				err = errors.New("incomplete replay")
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal("historical concurrent replay", err)
		}
	}
	if observed.writes.Load() != 1 {
		t.Fatal("historical replay repeated write")
	}
	for _, action := range []string{"workspace.changes.export.prepared", "workspace.changes.export.completed"} {
		var audits, outbox int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND operation_id=$2 AND action=$3`, scope, claim.ID, action).Scan(&audits); err != nil || audits != 1 {
			t.Fatal("audit count", action, audits, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND aggregate_id=$2 AND event_type=$3`, scope, artifactRecord.ID, action).Scan(&outbox); err != nil || outbox != 1 {
			t.Fatal("outbox count", action, outbox, err)
		}
	}
	observed.corruptRead = true
	if entry, err := retain(binding); err == nil || entry.Ready || observed.writes.Load() != 1 {
		t.Fatal("corrupt retained object was accepted or overwritten")
	}
	observed.corruptRead = false
	database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 71); err == nil {
		t.Fatal("downgrade removed change export evidence")
	}
	if _, err := pool.Exec(ctx, `UPDATE artifacts SET state='deleted' WHERE isolation_domain_id=$1 AND id=$2`, scope, artifactRecord.ID); err != nil {
		t.Fatal(err)
	}
	before = observed.reads.Load()
	if entry, err := retain(binding); err == nil || entry.Ready || observed.reads.Load() != before || observed.writes.Load() != 1 {
		t.Fatal("historical replay resurrected a deleted artifact", err)
	}
	// The retained metadata is bounded and contains no sensitive paths or bytes.
	data, _ := json.Marshal(binding.Record)
	if bytes.Contains(data, []byte("private-file")) || bytes.Contains(data, []byte("retained")) {
		t.Fatal("private content entered export metadata")
	}
}
