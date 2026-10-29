package persistence_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/asabla/dataground/internal/workspace"
	"github.com/jackc/pgx/v5/pgxpool"
)

type exportSourceObjects struct {
	record     workspace.SourceRecord
	data       []byte
	reads      int
	beforeRead func()
}

func (s *exportSourceObjects) WorkspaceSourceStorageDigest() string {
	return s.record.StorageProfileDigest
}
func (s *exportSourceObjects) OpenWorkspaceSource(_ context.Context, key string) (io.ReadCloser, error) {
	expected, _ := workspace.SourceObjectKey(s.record)
	if key != expected {
		return nil, workspace.ErrSourceConflict
	}
	s.reads++
	if s.beforeRead != nil {
		s.beforeRead()
	}
	return io.NopCloser(bytes.NewReader(s.data)), nil
}
func exportSnapshot(t *testing.T, content string) (*workspace.Snapshot, []byte) {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "private.bin", Mode: 0600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := buffer.Bytes()
	sum := sha256.Sum256(data)
	snapshot, err := workspace.Inspect(data, "sha256:"+hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, data
}
func prepareExportSourceFixture(t *testing.T, ctx context.Context, repo *persistence.Repository, scope string) (workspace.SourceRecord, *exportSourceObjects, *workspace.Snapshot) {
	t.Helper()
	snapshot, data := exportSnapshot(t, "base\x00\xff")
	receipt := snapshot.Receipt()
	source := workspace.SourceRecord{SchemaVersion: workspace.SourceRecordV1, IsolationDomainID: scope, WorkspaceID: identity.New("wsp"), ID: identity.New("src"), ArchiveDigest: receipt.ArchiveDigest, StorageProfileDigest: "sha256:" + strings.Repeat("b", 64), ArchiveBytes: int64(len(data)), EntryCount: receipt.EntryCount, SourceBytes: receipt.SourceBytes}
	if _, err := repo.CreateWorkspace(ctx, testIdempotency(scope, "export-workspace"), persistence.CreateWorkspaceInput{ID: source.WorkspaceID, Name: "Export fixture", ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	installation := workspace.SourceInstallation{Record: source, ActorID: "operator", CorrelationID: identity.New("cor")}
	if _, err := repo.PrepareWorkspaceSource(ctx, installation); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CompleteWorkspaceSource(ctx, installation); err != nil {
		t.Fatal(err)
	}
	return source, &exportSourceObjects{record: source, data: data}, snapshot
}

type exportArtifactObjects struct {
	data          []byte
	reads, writes int
	loseWrite     bool
}

func (o *exportArtifactObjects) OpenInvocationArtifactObject(context.Context, string) (io.ReadCloser, error) {
	o.reads++
	if o.data == nil {
		return nil, artifact.ErrInvocationArtifactObjectMissing
	}
	return io.NopCloser(bytes.NewReader(o.data)), nil
}
func (o *exportArtifactObjects) PutInvocationArtifactObjectIfAbsent(_ context.Context, _ string, r io.Reader, _ int64, _, _ string) error {
	o.writes++
	if o.data != nil {
		return artifact.ErrInvocationArtifactObjectConflict
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	o.data = data
	if o.loseWrite {
		return errors.New("lost write acknowledgement")
	}
	return nil
}

type exportCaptureProvider struct {
	ref     execution.ExecutionRef
	base    *workspace.Snapshot
	changes *workspace.ChangeSet
	calls   int
}

func (p *exportCaptureProvider) ExportWorkspaceChanges(ctx context.Context, ref execution.ExecutionRef, base *workspace.Snapshot) (*workspace.ChangeSet, error) {
	p.calls++
	if ctx.Err() != nil || ref != p.ref || base.Receipt() != p.base.Receipt() {
		return nil, workspace.ErrChangeExportConflict
	}
	return p.changes, nil
}

// Retained bytes and all claim, grant, decision and catalog transitions are real.
// Capture is an in-process provider fixture; cleanup is a trusted test observation.
func beginWorkspaceExportController(t *testing.T, ctx context.Context, pool *pgxpool.Pool, claim persistence.OperationClaim, effect persistence.EffectRecord, q persistence.InvocationWorkspaceQuiescence, service, revision string, source *exportSourceObjects, base *workspace.Snapshot) func(persistence.OperationClaim) {
	t.Helper()
	repo := persistence.NewRepository(pool)
	ref := execution.ExecutionRef{IsolationDomainID: claim.IsolationDomainID, ID: q.ExecutionID}
	read, expires, err := repo.ReadExportWorkspaceSource(ctx, claim, effect, source, q, 1, &reconcile.SourceCedarEvaluator{})
	if err != nil || read.Receipt() != base.Receipt() || !expires.After(time.Now()) {
		t.Fatal("original export base read", err)
	}
	var decisions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspace_source_use_decisions WHERE isolation_domain_id=$1 AND operation_id=$2 AND claim_contract='dataground.invocation-source-read/v5' AND cleanup_operation_id=$2 AND outcome='allowed' AND policy_contract='dataground.invocation-authorization-policy/v7'`, claim.IsolationDomainID, claim.ID).Scan(&decisions); err != nil || decisions != 2 {
		t.Fatal("source cleanup provenance", decisions, err)
	}
	before := source.reads
	wrong := q
	wrong.OwnerBinding = "sha256:" + strings.Repeat("f", 64)
	if _, _, err := repo.ReadExportWorkspaceSource(ctx, claim, effect, source, wrong, 1, &reconcile.SourceCedarEvaluator{}); err == nil || source.reads != before {
		t.Fatal("wrong owner reached source", err)
	}
	if _, _, err := repo.ReadExportWorkspaceSource(ctx, claim, effect, source, q, 2, &reconcile.SourceCedarEvaluator{}); err == nil || source.reads != before {
		t.Fatal("wrong grant reached source", err)
	}
	// Failure to commit delivery must prevent release of the verified snapshot.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_export_source_delivery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.claim_contract='dataground.invocation-source-read/v5' AND NEW.phase='read-delivery' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_export_source_delivery BEFORE INSERT ON workspace_source_use_decisions FOR EACH ROW EXECUTE FUNCTION reject_export_source_delivery()`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_export_source_delivery() CASCADE`)
	if snapshot, _, err := repo.ReadExportWorkspaceSource(ctx, claim, effect, source, q, 1, &reconcile.SourceCedarEvaluator{}); err == nil || snapshot != nil {
		t.Fatal("unaudited delivery released source")
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_export_source_delivery() CASCADE`); err != nil {
		t.Fatal(err)
	}
	result, _ := exportSnapshot(t, "updated\x00\xfe")
	changes, err := workspace.Compare(base, result)
	if err != nil {
		t.Fatal(err)
	}
	objects := &exportArtifactObjects{loseWrite: true}
	provider := &exportCaptureProvider{ref: ref, base: base, changes: changes}
	config := reconcile.InvocationWorkspaceConfig{Scope: reconcile.InvocationAuthorizationPolicyScope{IsolationDomainID: claim.IsolationDomainID, ServiceID: service, RevisionID: revision}, Source: q.Source, PlanDigest: q.PlanDigest, GrantGeneration: 1}
	exporter, err := reconcile.NewInvocationWorkspaceExporter(repo, source, objects, objects, config)
	if err != nil {
		t.Fatal(err)
	}
	check := func(context.Context) error { return nil }
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_export_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='workspace.changes.export.completed' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_export_completion BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION reject_export_completion()`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_export_completion() CASCADE`)
	if _, err := exporter.Export(ctx, claim, effect, ref, provider, check); err == nil {
		t.Fatal("failed completion audit returned success")
	}
	pending, err := repo.GetWorkspaceChangeExport(ctx, claim.IsolationDomainID, claim.ID)
	if err != nil || pending.Ready || pending.Record.Changes != changes.Receipt() || objects.writes != 1 || provider.calls != 1 {
		t.Fatal("pending exact export", pending.Ready, objects.writes, provider.calls, err)
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_export_completion() CASCADE`); err != nil {
		t.Fatal(err)
	}
	// Preserve a native result before worker replacement. The replacement driver
	// must complete this pending export without invoking any runtime transport.
	if _, err := repo.CompleteInvocationRuntimeAttempt(ctx, claim, effect, map[string]any{"status": "succeeded"}); err != nil {
		t.Fatal("retain native result", err)
	}
	// Revoke during a read, between its admission and delivery decisions.
	source.beforeRead = func() {
		source.beforeRead = nil
		if err := repo.ChangeWorkspaceSourceGrant(ctx, workspace.SourceGrantChange{Contract: workspace.SourceGrantV1, Record: q.Source, RevisionID: revision, Generation: 2, Operation: "revoke", ActorID: "operator", ReasonDigest: "sha256:" + strings.Repeat("a", 64), CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
	}
	if snapshot, _, err := repo.ReadExportWorkspaceSource(ctx, claim, effect, source, q, 1, &reconcile.SourceCedarEvaluator{}); err == nil || snapshot != nil {
		t.Fatal("source delivered after revocation")
	}
	return func(replacement persistence.OperationClaim) {
		before := objects.reads
		if _, err := exporter.Export(ctx, replacement, effect, ref, provider, check); err == nil || objects.reads != before || provider.calls != 1 || objects.writes != 1 {
			t.Fatal("revoked base grant reached export", err)
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		if err := repo.ChangeWorkspaceSourceGrant(ctx, workspace.SourceGrantChange{Contract: workspace.SourceGrantV1, Record: q.Source, RevisionID: revision, Generation: 3, Operation: "activate", ActivatedAt: now.Add(-time.Second), ExpiresAt: now.Add(30 * time.Minute), ActorID: "operator", ReasonDigest: "sha256:" + strings.Repeat("a", 64), CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
		config.GrantGeneration = 3
		exporter, err := reconcile.NewInvocationWorkspaceExporter(persistence.NewRepository(pool), source, objects, objects, config)
		if err != nil {
			t.Fatal(err)
		}
		beforeSource := source.reads
		if _, _, err := repo.ReadExportWorkspaceSource(ctx, claim, effect, source, q, 3, &reconcile.SourceCedarEvaluator{}); err == nil || source.reads != beforeSource {
			t.Fatal("stale claim read base", err)
		}
		if _, err := repo.ReadClaimedWorkspaceSource(ctx, replacement, effect, source, q.Source, 3, &reconcile.SourceCedarEvaluator{}); err == nil {
			t.Fatal("original runtime read boundary weakened")
		}
		recoverWorkspaceThroughRuntimeDriver(t, ctx, pool, replacement, effect, config, source, objects, provider)
		completed, err := repo.GetWorkspaceChangeExport(ctx, replacement.IsolationDomainID, replacement.ID)
		if err != nil || !completed.Ready || completed.Record != pending.Record || objects.writes != 1 || provider.calls != 1 {
			t.Fatal("replacement did not recover exact object", objects.writes, provider.calls, err)
		}
		if _, err := exporter.Export(ctx, replacement, effect, ref, provider, check); err != nil || objects.writes != 1 || provider.calls != 1 {
			t.Fatal("completed replay rewrote or recaptured", err)
		}
		retained := bytes.Clone(objects.data)
		objects.data[0] ^= 1
		if _, err := exporter.Export(ctx, replacement, effect, ref, provider, check); err == nil || objects.writes != 1 || provider.calls != 1 {
			t.Fatal("corrupt retained object was recaptured", err)
		}
		objects.data = nil
		if _, err := exporter.Export(ctx, replacement, effect, ref, provider, check); err == nil || objects.writes != 1 || provider.calls != 1 {
			t.Fatal("missing completed object was recreated", err)
		}
		objects.data = retained
		db, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := persistence.MigrateDownTo(ctx, db, 74); err == nil {
			t.Fatal("downgrade removed cleanup-bound source evidence")
		}
	}
}
