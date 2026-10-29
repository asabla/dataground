package persistence_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution/s3store"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/workspace"
)

type sourceReadObserver struct {
	workspace.SourceObjects
	reads      atomic.Int64
	beforeRead func()
}

func (s *sourceReadObserver) OpenWorkspaceSource(ctx context.Context, key string) (io.ReadCloser, error) {
	s.reads.Add(1)
	if s.beforeRead != nil {
		s.beforeRead()
	}
	return s.SourceObjects.OpenWorkspaceSource(ctx, key)
}
func TestWorkspaceSourceGrantsFenceReadsAndPersistDecisions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	defer func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE workspace_change_export_decisions,invocation_workspace_quiescence_completions,invocation_workspace_quiescence_intents,invocation_workspace_change_completions,invocation_workspace_change_exports,invocation_workspace_completions,invocation_workspace_preparations,service_revision_execution_plans,workspace_source_grants,workspace_source_use_decisions,workspace_source_completions,workspace_source_installations,workspaces CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	repo := persistence.NewRepository(pool)
	scope, wsp, src, service, revision := identity.New("iso"), identity.New("wsp"), identity.New("src"), identity.New("svc"), identity.New("rev")
	if _, err := repo.CreateWorkspace(ctx, testIdempotency(scope, "source-access"), persistence.CreateWorkspaceInput{ID: wsp, Name: "Source access", ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_services (isolation_domain_id,id,name,created_at,updated_at,created_by) VALUES ($1,$2,'Source access',now(),now(),'fixture')`, scope, service); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO service_revisions (isolation_domain_id,id,service_id,revision_number,state,runtime_profile,created_at,updated_at,created_by,published_at) VALUES ($1,$2,$3,1,'draft','codex',now(),now(),'fixture',now())`, scope, revision, service); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "src/private-file", Mode: 0o600, Size: 7}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte("private"))
	tw.Close()
	data := archive.Bytes()
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	var mu sync.Mutex
	content := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			if _, ok := content[r.URL.Path]; ok {
				w.WriteHeader(412)
				return
			}
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			content[r.URL.Path] = b
		case http.MethodGet:
			if b, ok := content[r.URL.Path]; ok {
				w.Write(b)
			} else {
				w.WriteHeader(404)
			}
		default:
			w.WriteHeader(405)
		}
	}))
	defer server.Close()
	endpoint, bucket := server.URL, "sources"
	if configured, configuredBucket := os.Getenv("DATAGROUND_TEST_S3_ENDPOINT"), os.Getenv("DATAGROUND_TEST_S3_BUCKET"); configured != "" || configuredBucket != "" {
		if configured == "" || configuredBucket == "" {
			t.Fatal("incomplete S3 configuration")
		}
		endpoint, bucket = configured, configuredBucket
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	base, err := s3store.New(s3store.Config{Endpoint: endpoint, Bucket: bucket, AddressingStyle: s3store.PathStyle, AllowHTTPForLoopback: true, HTTPClient: &http.Client{Transport: transport, Timeout: 5 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := s3store.NewWorkspaceSourceStore(base)
	if err != nil {
		t.Fatal(err)
	}
	record := workspace.SourceRecord{SchemaVersion: workspace.SourceRecordV1, IsolationDomainID: scope, WorkspaceID: wsp, ID: src, ArchiveDigest: digest, StorageProfileDigest: store.WorkspaceSourceStorageDigest(), ArchiveBytes: int64(len(data)), EntryCount: 2, SourceBytes: 7}
	if _, err := workspace.InstallSource(ctx, repo, store, workspace.SourceInstallation{Record: record, ActorID: "operator", CorrelationID: identity.New("cor")}, data); err != nil {
		t.Fatal(err)
	}
	t.Run("revision source binding", func(t *testing.T) { testRevisionSourceBinding(t, ctx, pool, record, service, revision) })
	observed := &sourceReadObserver{SourceObjects: store}
	use := workspace.SourceUse{Contract: workspace.SourceUseV1, Record: record, RevisionID: revision, OperationID: identity.New("op"), GrantGeneration: 1, ActorID: "initiating-actor", CorrelationID: identity.New("cor")}
	if snapshot, err := workspace.ReadSource(ctx, repo, observed, repo, use); snapshot != nil || !errors.Is(err, workspace.ErrSourceUnauthorized) || observed.reads.Load() != 0 {
		t.Fatal("missing grant reached source", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	grant := workspace.SourceGrantChange{Contract: workspace.SourceGrantV1, Record: record, RevisionID: revision, Generation: 1, Operation: "activate", ActivatedAt: now.Add(-time.Second), ExpiresAt: now.Add(30 * time.Minute), ActorID: "operator", ReasonDigest: "sha256:" + strings.Repeat("a", 64), CorrelationID: identity.New("cor")}

	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_source_grant_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='workspace.source.grant.activate' THEN RAISE EXCEPTION 'injected grant audit failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_source_grant_audit BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION reject_source_grant_audit()`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_source_grant_audit() CASCADE`)
	if err := repo.ChangeWorkspaceSourceGrant(ctx, grant); !errors.Is(err, workspace.ErrSourceUnavailable) {
		t.Fatal("grant audit failure did not fail closed", err)
	}
	var prepared int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspace_source_grants WHERE isolation_domain_id=$1`, scope).Scan(&prepared); err != nil || prepared != 0 {
		t.Fatal("failed audit retained a grant", prepared, err)
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_source_grant_audit() CASCADE`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- repo.ChangeWorkspaceSourceGrant(ctx, grant) }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspace_source_grants WHERE isolation_domain_id=$1`, scope).Scan(&count); err != nil || count != 1 {
		t.Fatal("concurrent activation duplicated", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND event_type='workspace.source.grant.activate'`, scope).Scan(&count); err != nil || count != 1 {
		t.Fatal("activation outbox duplicated", count, err)
	}
	artifactObjects, err := s3store.NewArtifactStore(base, workspace.MaximumChangeSetBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("invocation claim", func(t *testing.T) {
		testClaimedWorkspaceSource(t, ctx, pool, record, service, revision, store, artifactObjects)
	})
	snapshot, err := workspace.ReadSource(ctx, persistence.NewRepository(pool), observed, persistence.NewRepository(pool), use)
	if err != nil || snapshot == nil {
		t.Fatal("authorized source read failed", err)
	}
	if _, err := snapshot.Materialize(ctx, t.TempDir()); err != nil {
		t.Fatal("verified snapshot cannot materialize", err)
	}
	before := observed.reads.Load()
	for _, change := range []func(*workspace.SourceUse){func(v *workspace.SourceUse) { v.Record.IsolationDomainID = identity.New("iso") }, func(v *workspace.SourceUse) { v.Record.WorkspaceID = identity.New("wsp") }, func(v *workspace.SourceUse) { v.RevisionID = identity.New("rev") }, func(v *workspace.SourceUse) { v.GrantGeneration = 2 }, func(v *workspace.SourceUse) { v.Record.ArchiveDigest = "sha256:" + strings.Repeat("b", 64) }, func(v *workspace.SourceUse) { v.Record.StorageProfileDigest = "sha256:" + strings.Repeat("c", 64) }, func(v *workspace.SourceUse) { v.Record.EntryCount++ }} {
		changed := use
		change(&changed)
		if snapshot, err := workspace.ReadSource(ctx, repo, observed, repo, changed); snapshot != nil || !errors.Is(err, workspace.ErrSourceUnauthorized) || observed.reads.Load() != before {
			t.Fatal("changed source authority reached storage", err)
		}
	}
	changed := grant
	changed.ActorID = "replacement"
	if err := repo.ChangeWorkspaceSourceGrant(ctx, changed); !errors.Is(err, workspace.ErrSourceConflict) {
		t.Fatal("changed replay accepted", err)
	}
	changed = grant
	changed.Generation = 3
	changed.CorrelationID = identity.New("cor")
	if err := repo.ChangeWorkspaceSourceGrant(ctx, changed); !errors.Is(err, workspace.ErrSourceConflict) {
		t.Fatal("generation gap accepted", err)
	}
	revoke := grant
	revoke.Generation = 2
	revoke.Operation = "revoke"
	revoke.ActivatedAt = time.Time{}
	revoke.ExpiresAt = time.Time{}
	revoke.CorrelationID = identity.New("cor")
	observed.beforeRead = func() {
		if err := repo.ChangeWorkspaceSourceGrant(ctx, revoke); err != nil {
			t.Error(err)
		}
	}
	if snapshot, err := workspace.ReadSource(ctx, repo, observed, repo, use); snapshot != nil || !errors.Is(err, workspace.ErrSourceUnauthorized) {
		t.Fatal("revocation during read released source", err)
	}
	observed.beforeRead = nil
	if err := repo.ChangeWorkspaceSourceGrant(ctx, grant); err != nil {
		t.Fatal("historical grant replay failed", err)
	}
	if _, err := repo.AuthorizeWorkspaceSource(ctx, use, workspace.SourceReadDelivery); !errors.Is(err, workspace.ErrSourceUnauthorized) {
		t.Fatal("historical replay reactivated source", err)
	}
	expiring := grant
	expiring.Generation = 3
	expiring.ActivatedAt = time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	expiring.ExpiresAt = time.Now().UTC().Add(2 * time.Second).Truncate(time.Microsecond)
	expiring.CorrelationID = identity.New("cor")
	if err := repo.ChangeWorkspaceSourceGrant(ctx, expiring); err != nil {
		t.Fatal(err)
	}
	use.GrantGeneration = 3
	time.Sleep(time.Until(expiring.ExpiresAt) + 10*time.Millisecond)
	if _, err := repo.AuthorizeWorkspaceSource(ctx, use, workspace.SourceReadAdmission); !errors.Is(err, workspace.ErrSourceUnauthorized) {
		t.Fatal("expired grant allowed", err)
	}
	current := grant
	current.Generation = 4
	current.CorrelationID = identity.New("cor")
	if err := repo.ChangeWorkspaceSourceGrant(ctx, current); err != nil {
		t.Fatal(err)
	}
	use.GrantGeneration = 4
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_source_use_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected source decision failure'; END; $$; CREATE TRIGGER reject_source_use_audit BEFORE INSERT ON workspace_source_use_decisions FOR EACH ROW EXECUTE FUNCTION reject_source_use_audit()`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_source_use_audit() CASCADE`)
	before = observed.reads.Load()
	if snapshot, err := workspace.ReadSource(ctx, repo, observed, repo, use); snapshot != nil || !errors.Is(err, workspace.ErrSourceUnavailable) || observed.reads.Load() != before {
		t.Fatal("audit failure released source", err)
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_source_use_audit() CASCADE`); err != nil {
		t.Fatal(err)
	}
	// Direct SQL uses the same sequence lock as application authorization.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `INSERT INTO workspace_source_grants (contract,isolation_domain_id,workspace_id,source_id,revision_id,archive_digest,storage_profile_digest,generation,operation,actor_id,reason_digest,correlation_id) VALUES ($1,$2,$3,$4,$5,$6,$7,5,'revoke','operator',$8,$9)`, workspace.SourceGrantV1, scope, wsp, src, revision, digest, record.StorageProfileDigest, grant.ReasonDigest, identity.New("cor")); err != nil {
		t.Fatal(err)
	}
	waiting := make(chan error, 1)
	go func() {
		_, err := persistence.NewRepository(pool).AuthorizeWorkspaceSource(ctx, use, workspace.SourceReadAdmission)
		waiting <- err
	}()
	select {
	case err := <-waiting:
		t.Fatal("authorization did not wait for direct grant change", err)
	case <-time.After(75 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waiting:
		if !errors.Is(err, workspace.ErrSourceUnauthorized) {
			t.Fatal("direct revocation ignored", err)
		}
	case <-time.After(time.Second):
		t.Fatal("authorization did not resume")
	}
	current.Generation = 6
	current.CorrelationID = identity.New("cor")
	if err := repo.ChangeWorkspaceSourceGrant(ctx, current); err != nil {
		t.Fatal(err)
	}
	use.GrantGeneration = 6

	observed.beforeRead = func() {
		if _, err := pool.Exec(ctx, `UPDATE service_revisions SET state='retired' WHERE isolation_domain_id=$1 AND id=$2`, scope, revision); err != nil {
			t.Error(err)
		}
	}
	if snapshot, err := workspace.ReadSource(ctx, repo, observed, repo, use); snapshot != nil || !errors.Is(err, workspace.ErrSourceUnauthorized) {
		t.Fatal("retirement during read released source", err)
	}
	observed.beforeRead = nil

	if _, err := repo.AuthorizeWorkspaceSource(ctx, use, workspace.SourceReadAdmission); !errors.Is(err, workspace.ErrSourceUnauthorized) {
		t.Fatal("retired revision retained source use", err)
	}
	revoke.Generation = 7
	revoke.CorrelationID = identity.New("cor")
	if err := repo.ChangeWorkspaceSourceGrant(ctx, revoke); err != nil {
		t.Fatal("retired revision could not revoke source", err)
	}
	for _, query := range []string{`UPDATE workspace_source_grants SET actor_id='changed'`, `DELETE FROM workspace_source_grants`, `DELETE FROM workspace_source_use_decisions`} {
		if _, err := pool.Exec(ctx, query); err == nil {
			t.Fatal("source grant evidence was mutable")
		}
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspace_source_use_decisions WHERE isolation_domain_id=$1 AND phase='read-delivery' AND outcome='denied' AND observed_generation=2`, scope).Scan(&count); err != nil || count != 2 {
		t.Fatal("delivery revocation decisions missing", count, err)
	}
	database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 65); err == nil {
		t.Fatal("downgrade removed source grant evidence")
	}
}
