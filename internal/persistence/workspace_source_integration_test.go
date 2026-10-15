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
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution/s3store"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/workspace"
)

func TestWorkspaceSourcesBindDurableOwnershipAndRecoverStorage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	defer func() {
		_, err := pool.Exec(context.Background(), `TRUNCATE workspace_source_grants, workspace_source_use_decisions, workspace_source_completions, workspace_source_installations, workspaces`)
		if err != nil {
			t.Error(err)
		}
	}()
	repo := persistence.NewRepository(pool)
	scope, wsp, src := identity.New("iso"), identity.New("wsp"), identity.New("src")
	if _, err := repo.CreateWorkspace(ctx, testIdempotency(scope, "source-workspace"), persistence.CreateWorkspaceInput{ID: wsp, Name: "Source", ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "private-path", Mode: 0o600, Size: 7}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte("private"))
	tw.Close()
	data := archive.Bytes()
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	var mu sync.Mutex
	objects := map[string][]byte{}
	puts := 0
	lose := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			if content, ok := objects[r.URL.Path]; ok {
				w.Write(content)
			} else {
				w.WriteHeader(404)
			}
		case http.MethodPut:
			puts++
			if r.Header.Get("If-None-Match") != "*" || r.Header.Get("x-amz-checksum-sha256") == "" {
				t.Error("missing immutable write headers")
				w.WriteHeader(400)
				return
			}
			if _, ok := objects[r.URL.Path]; ok {
				w.WriteHeader(412)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			objects[r.URL.Path] = body
			if lose {
				lose = false
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(200)
		default:
			t.Error("unexpected object method")
			w.WriteHeader(405)
		}
	}))
	defer server.Close()
	base, err := s3store.New(s3store.Config{Endpoint: server.URL, Bucket: "sources", AddressingStyle: s3store.PathStyle, AllowHTTPForLoopback: true, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	store, err := s3store.NewWorkspaceSourceStore(base)
	if err != nil {
		t.Fatal(err)
	}
	binding := workspace.SourceInstallation{Record: workspace.SourceRecord{SchemaVersion: workspace.SourceRecordV1, IsolationDomainID: scope, WorkspaceID: wsp, ID: src, ArchiveDigest: digest, StorageProfileDigest: store.WorkspaceSourceStorageDigest(), ArchiveBytes: int64(len(data)), EntryCount: 1, SourceBytes: 7}, ActorID: "operator", CorrelationID: identity.New("cor")}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := workspace.InstallSource(ctx, persistence.NewRepository(pool), store, binding, data)
			if err == nil && (!v.Ready || v.Installation != binding) {
				err = errors.New("source completion changed")
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	restarted := persistence.NewRepository(pool)
	got, err := restarted.GetWorkspaceSource(ctx, scope, wsp, src)
	if err != nil || !got.Ready || got.Installation != binding {
		t.Fatal("restart lost source ownership", err)
	}
	before := puts
	if _, err := workspace.InstallSource(ctx, restarted, store, binding, data); err != nil || puts != before {
		t.Fatal("replay rewrote immutable object", err)
	}
	for _, change := range []string{"actor", "correlation", "digest", "storage"} {
		changed := binding
		switch change {
		case "actor":
			changed.ActorID = "other"
		case "correlation":
			changed.CorrelationID = identity.New("cor")
		case "digest":
			changed.Record.ArchiveDigest = "sha256:" + strings.Repeat("a", 64)
		case "storage":
			changed.Record.StorageProfileDigest = "sha256:" + strings.Repeat("b", 64)
		}
		if _, err := repo.PrepareWorkspaceSource(ctx, changed); !errors.Is(err, workspace.ErrSourceConflict) {
			t.Fatal("changed replay accepted", change, err)
		}
	}
	for _, ids := range [][3]string{{identity.New("iso"), wsp, src}, {scope, identity.New("wsp"), src}} {
		if _, err := repo.GetWorkspaceSource(ctx, ids[0], ids[1], ids[2]); !errors.Is(err, workspace.ErrSourceMissing) {
			t.Fatal("cross-scope lookup escaped", err)
		}
	}
	foreign := binding
	foreign.Record.IsolationDomainID = identity.New("iso")
	if _, err := workspace.InstallSource(ctx, repo, store, foreign, data); !errors.Is(err, workspace.ErrSourceMissing) || puts != before {
		t.Fatal("foreign workspace reached storage", err)
	}

	if _, err := repo.CreateWorkspace(ctx, testIdempotency(foreign.Record.IsolationDomainID, "source-workspace"), persistence.CreateWorkspaceInput{ID: wsp, Name: "Other source", ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if value, err := workspace.InstallSource(ctx, repo, store, foreign, data); err != nil || !value.Ready || value.Installation.Record.IsolationDomainID != foreign.Record.IsolationDomainID {
		t.Fatal("independent colliding source identity failed", err)
	}
	primaryKey, _ := workspace.SourceObjectKey(binding.Record)
	foreignKey, _ := workspace.SourceObjectKey(foreign.Record)
	if primaryKey == foreignKey {
		t.Fatal("source object scope collided")
	}
	if value, err := repo.GetWorkspaceSource(ctx, scope, wsp, src); err != nil || value.Installation != binding {
		t.Fatal("foreign source replaced original ownership", err)
	}
	var audits, outbox int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND action LIKE 'workspace.source.%'`, scope).Scan(&audits); err != nil || audits != 2 {
		t.Fatal("audit replay changed", audits, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND event_type LIKE 'workspace.source.%'`, scope).Scan(&outbox); err != nil || outbox != 2 {
		t.Fatal("outbox replay changed", outbox, err)
	}

	failed := binding
	failed.Record.ID = identity.New("src")
	failed.CorrelationID = identity.New("cor")
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_source_completion_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='workspace.source.completed' THEN RAISE EXCEPTION 'injected completion audit failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_source_completion_audit BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION reject_source_completion_audit()`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_source_completion_audit() CASCADE`)
	if _, err := workspace.InstallSource(ctx, repo, store, failed, data); !errors.Is(err, workspace.ErrSourceUnavailable) {
		t.Fatal("audit failure did not block completion", err)
	}
	pending, err := repo.GetWorkspaceSource(ctx, scope, wsp, failed.Record.ID)
	if err != nil || pending.Ready {
		t.Fatal("failed audit exposed a ready source", err)
	}
	retainedPuts := puts
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_source_completion_audit() CASCADE`); err != nil {
		t.Fatal(err)
	}
	if ready, err := workspace.InstallSource(ctx, persistence.NewRepository(pool), store, failed, data); err != nil || !ready.Ready || puts != retainedPuts {
		t.Fatal("retained source did not recover after audit failure", err)
	}
	for _, query := range []string{`UPDATE workspace_source_installations SET actor_id='changed'`, `DELETE FROM workspace_source_installations`, `DELETE FROM workspace_source_completions`} {
		if _, err := pool.Exec(ctx, query); err == nil {
			t.Fatal("append-only source record changed")
		}
	}
	database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 64); err == nil {
		t.Fatal("downgrade removed source evidence")
	}

	if endpoint, bucket := os.Getenv("DATAGROUND_TEST_S3_ENDPOINT"), os.Getenv("DATAGROUND_TEST_S3_BUCKET"); endpoint != "" || bucket != "" {
		if endpoint == "" || bucket == "" {
			t.Fatal("source S3 conformance configuration is incomplete")
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		defer transport.CloseIdleConnections()
		liveBase, err := s3store.New(s3store.Config{Endpoint: endpoint, Bucket: bucket, AddressingStyle: s3store.PathStyle, AllowHTTPForLoopback: true, HTTPClient: &http.Client{Transport: transport, Timeout: 10 * time.Second}})
		if err != nil {
			t.Fatal(err)
		}
		liveStore, err := s3store.NewWorkspaceSourceStore(liveBase)
		if err != nil {
			t.Fatal(err)
		}
		live := binding
		live.Record.ID = identity.New("src")
		live.Record.StorageProfileDigest = liveStore.WorkspaceSourceStorageDigest()
		live.CorrelationID = identity.New("cor")
		lost := &sourceCompletionLoss{SourceCatalog: repo}
		if _, err := workspace.InstallSource(ctx, lost, liveStore, live, data); !errors.Is(err, workspace.ErrSourceUnavailable) {
			t.Fatal("source commit acknowledgement was not lost", err)
		}
		got, err := workspace.InstallSource(ctx, persistence.NewRepository(pool), liveStore, live, data)
		if err != nil || !got.Ready {
			t.Fatal("live source recovery failed", err)
		}
		key, err := workspace.SourceObjectKey(live.Record)
		if err != nil {
			t.Fatal(err)
		}
		if err := liveStore.PutWorkspaceSourceIfAbsent(ctx, key, data, digest); !errors.Is(err, workspace.ErrSourceConflict) {
			t.Fatal("live backend did not reject replacement", err)
		}
	}
	mu.Lock()
	for key := range objects {
		objects[key] = []byte("corrupt")
	}
	mu.Unlock()
	if _, err := workspace.InstallSource(ctx, repo, store, binding, data); !errors.Is(err, workspace.ErrSourceConflict) {
		t.Fatal("stored archive drift passed", err)
	}
}

type sourceCompletionLoss struct {
	workspace.SourceCatalog
	lost bool
}

func (c *sourceCompletionLoss) CompleteWorkspaceSource(ctx context.Context, v workspace.SourceInstallation) (workspace.SourceCatalogEntry, error) {
	entry, err := c.SourceCatalog.CompleteWorkspaceSource(ctx, v)
	if err == nil && !c.lost {
		c.lost = true
		return workspace.SourceCatalogEntry{}, workspace.ErrSourceUnavailable
	}
	return entry, err
}
