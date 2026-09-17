package persistence_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestResourceAuditSnapshotDisclosureAndAuthorization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newRuntimeQuestionFixture(t, ctx)
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(), `TRUNCATE resource_audit_read_receipts,api_authorization_decisions,invocation_authorization_decisions,publication_authorization_decisions`); err != nil {
			t.Error(err)
		}
	})
	scope, invocationID, revisionID := f.target.IsolationDomainID, f.target.InvocationID, f.target.RevisionID
	principal := func(id string, kind authn.PrincipalKind) authn.Principal {
		t.Helper()
		p, err := authn.NewPrincipal(authn.PrincipalInput{ID: id, Kind: kind, Issuer: "test", Subject: id, Audience: authn.APIAudience, IsolationDomains: []string{scope}})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	reader := principal("auditor", authn.PrincipalHuman)
	repo := f.repository
	read := func(kind, id, cursor string, limit int) domain.ResourceAuditPage {
		t.Helper()
		page, err := repo.ReadResourceAudit(ctx, reader, scope, kind, id, identity.New("cor"), cursor, limit)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(page)
		digest := sha256.Sum256(encoded)
		var retained []byte
		if err := f.pool.QueryRow(ctx, `SELECT content_digest FROM resource_audit_read_receipts WHERE id=$1 AND isolation_domain_id=$2`, page.ReceiptID, scope).Scan(&retained); err != nil || !bytes.Equal(digest[:], retained) {
			t.Fatal("missing exact disclosure receipt", err)
		}
		return page
	}
	type executor interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	}
	insert := func(db executor, actor string) {
		t.Helper()
		_, err := db.Exec(ctx, `INSERT INTO audit_records (id,isolation_domain_id,actor_id,action,resource_type,resource_id,outcome,correlation_id,operation_id,safe_metadata,occurred_at)
            VALUES ($1,$2,$3,'invocation.test','invocation',$4,'accepted',$5,$6,'{"secret":"private-native-payload"}',clock_timestamp())`, identity.New("aud"), scope, actor, invocationID, identity.New("cor"), f.target.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(ctx, `INSERT INTO invocation_authorization_decisions (isolation_domain_id,operation_id,invocation_id,service_id,revision_id,actor_id,action,outcome,policy_set_id,policy_digest,correlation_id)
            VALUES ($1,$2,$3,$4,$5,$6,'run','allowed','reviewed-policy',$7,$8)`, scope, f.target.OperationID, invocationID, f.target.ServiceID, revisionID, actor, "sha256:"+strings.Repeat("a", 64), identity.New("cor"))
		if err != nil {
			t.Fatal(err)
		}
	}
	// Allocate lower sequence values without committing. A high-water sequence
	// alone would expose these rows on continuation after the later commit.
	late, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer late.Rollback(ctx)
	if _, err := late.Exec(ctx, `SAVEPOINT audit_subtransaction`); err != nil {
		t.Fatal(err)
	}
	insert(late, "late-commit")
	if _, err := late.Exec(ctx, `RELEASE SAVEPOINT audit_subtransaction`); err != nil {
		t.Fatal(err)
	}
	insert(f.pool, "visible")
	aborted, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insert(aborted, "aborted")
	if err := aborted.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// A colliding resource identifier in another domain must never enter the page.
	if _, err := f.pool.Exec(ctx, `INSERT INTO audit_records (id,isolation_domain_id,actor_id,action,resource_type,resource_id,outcome,correlation_id,occurred_at)
        VALUES ($1,$2,'foreign-domain','invocation.test','invocation',$3,'accepted',$4,clock_timestamp())`, identity.New("aud"), identity.New("iso"), invocationID, identity.New("cor")); err != nil {
		t.Fatal(err)
	}
	expected := read("invocation", invocationID, "", 100)
	first := read("invocation", invocationID, "", 1)
	if first.NextCursor == "" {
		t.Fatal("missing continuation")
	}
	if err := late.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	insert(f.pool, "new-commit")
	repo = persistence.NewRepository(f.pool)
	items := append([]domain.ResourceAuditRecord{}, first.Items...)
	cursor := first.NextCursor
	var terminal domain.ResourceAuditPage
	for i := 0; cursor != ""; i++ {
		if i > 100 {
			t.Fatal("pagination did not terminate")
		}
		page := read("invocation", invocationID, cursor, 1)
		replay := read("invocation", invocationID, cursor, 1)
		if !reflect.DeepEqual(page.Items, replay.Items) {
			t.Fatal("cursor retry changed content")
		}
		items = append(items, page.Items...)
		cursor = page.NextCursor
		terminal = page
	}
	if !reflect.DeepEqual(items, expected.Items) {
		t.Fatalf("snapshot changed after commits: got %#v want %#v", items, expected.Items)
	}
	refreshed := read("invocation", invocationID, "", 100)
	if len(refreshed.Items) != len(expected.Items)+4 {
		t.Fatal("refresh omitted later commits")
	}
	for _, item := range items {
		if item.ActorID == "late-commit" || item.ActorID == "new-commit" || item.ActorID == "aborted" || item.ActorID == "foreign-domain" {
			t.Fatal("snapshot leaked later/aborted transaction")
		}
	}
	for _, p := range []authn.Principal{principal("other-auditor", authn.PrincipalHuman), principal("auditor", authn.PrincipalService)} {
		if page, err := repo.ReadResourceAudit(ctx, p, scope, "invocation", invocationID, identity.New("cor"), first.NextCursor, 1); !errors.Is(err, persistence.ErrResourceAuditInvalid) || len(page.Items) != 0 {
			t.Fatal("cursor crossed principal boundary", err)
		}
	}
	for _, test := range []struct{ kind, id, cursor string }{{"service-revision", revisionID, first.NextCursor}, {"invocation", invocationID, identity.New("arr")}, {"invocation", invocationID, terminal.ReceiptID}} {
		if _, err := repo.ReadResourceAudit(ctx, reader, scope, test.kind, test.id, identity.New("cor"), test.cursor, 1); !errors.Is(err, persistence.ErrResourceAuditInvalid) {
			t.Fatal("invalid continuation accepted", err)
		}
	}
	// A revision includes its publication operation, but no invocation records.
	revision := read("service-revision", revisionID, "", 100)
	foundPublication := false
	for _, item := range revision.Items {
		if strings.HasPrefix(item.Action, "invocation") {
			t.Fatal("revision read included invocation")
		}
		foundPublication = foundPublication || item.Action == "service-publication.published"
	}
	if !foundPublication {
		t.Fatal("revision publication lifecycle omitted")
	}
	var publicationID string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM service_publication_operations WHERE isolation_domain_id=$1 AND revision_id=$2`, scope, revisionID).Scan(&publicationID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO publication_authorization_decisions (contract,isolation_domain_id,service_id,revision_id,operation_id,actor_id,correlation_id,phase,expected_version,fencing_token,plan_digest,verification_digest,policy_contract,policy_set_id,policy_digest,outcome)
        VALUES ('dataground.publication-authorization-decision/v1',$1,$2,$3,$4,'publisher',$5,'entry',1,0,$6,$6,'dataground.invocation-authorization-policy/v5','publication-policy',$6,'allowed')`, scope, f.target.ServiceID, revisionID, publicationID, identity.New("cor"), "sha256:"+strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	revision = read("service-revision", revisionID, "", 100)
	last := revision.Items[len(revision.Items)-1]
	if last.Source != "publication-authorization" || last.Phase != "entry" || last.PolicySetID != "publication-policy" {
		t.Fatal("publication policy provenance omitted")
	}

	const token = "resource-audit-token-with-thirty-two-bytes"
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: "auditor", IsolationDomainID: scope})
	if err != nil {
		t.Fatal(err)
	}
	handlerFor := func(policy string) http.Handler {
		t.Helper()
		authorizer, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{PolicySetID: "audit-read-test", Schema: authz.CanonicalAPICedarSchema(), Policies: []byte(policy)})
		if err != nil {
			t.Fatal(err)
		}
		audited, err := authz.NewAuditedAuthorizer(authorizer, repo)
		if err != nil {
			t.Fatal(err)
		}
		handler, err := api.NewDurableHandler(repo, authenticator, audited)
		if err != nil {
			t.Fatal(err)
		}
		return handler
	}
	handler := handlerFor(`permit(principal,action,resource) when { action == DataGround::Action::"readInvocationAudit" || action == DataGround::Action::"readServiceRevisionAudit" };`)
	path := "/v1/isolation-domains/" + scope + "/invocations/" + invocationID + "/audit"
	request := func(path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("audit HTTP %d want %d: %s", w.Code, want, w.Body.String())
		}
		return w
	}
	response := request(path+"?limit=1", 200)
	var httpPage domain.ResourceAuditPage
	if err := json.Unmarshal(response.Body.Bytes(), &httpPage); err != nil || httpPage.NextCursor == "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invalid HTTP audit page", err)
	}
	response = request(path, 200)
	for _, secret := range []string{"safeMetadata", "private-native-payload", "snapshot", "sequence", "recorded_transaction_id", "input"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatal("private data leaked", secret)
		}
	}
	request(path+"?cursor="+httpPage.NextCursor, 200)
	request(strings.TrimSuffix(path, "/audit"), 403)
	request("/v1/isolation-domains/"+scope+"/service-revisions/"+revisionID+"/audit", 200)
	request("/v1/isolation-domains/"+scope+"/service-revisions/"+revisionID+"/audit?cursor="+httpPage.NextCursor, 400)
	request(strings.Replace(path, invocationID, identity.New("inv"), 1), 404)
	request(strings.Replace(path, scope, identity.New("iso"), 1), 403)
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=01", "?limit=1&limit=2", "?cursor=", "?unknown=value"} {
		request(path+query, 400)
	}
	handler = handlerFor(`permit(principal,action==DataGround::Action::"readInvocation",resource);`)
	request(path+"?cursor="+httpPage.NextCursor, 403)
	handler = handlerFor(`permit(principal,action==DataGround::Action::"readInvocationAudit",resource);`)
	// Receipt failure must disclose nothing, even after successful authorization.
	if _, err := f.pool.Exec(ctx, `ALTER TABLE resource_audit_read_receipts ADD CONSTRAINT test_reject_resource_audit CHECK (false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	response = request(path, 503)
	if strings.Contains(response.Body.String(), "items") || strings.Contains(response.Body.String(), "visible") {
		t.Fatal("receipt failure returned evidence")
	}
	if _, err := f.pool.Exec(ctx, `ALTER TABLE resource_audit_read_receipts DROP CONSTRAINT test_reject_resource_audit`); err != nil {
		t.Fatal(err)
	}
	var allowed, denied int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE outcome='allowed'),count(*) FILTER (WHERE outcome='denied') FROM api_authorization_decisions WHERE isolation_domain_id=$1 AND action IN ('readInvocationAudit','readServiceRevisionAudit')`, scope).Scan(&allowed, &denied); err != nil || allowed < 3 || denied < 1 {
		t.Fatal("audit read authorization was not recorded", allowed, denied, err)
	}
	for _, statement := range []string{`UPDATE resource_audit_read_receipts SET record_count=0 WHERE id=$1`, `DELETE FROM resource_audit_read_receipts WHERE id=$1`} {
		if _, err := f.pool.Exec(ctx, statement, first.ReceiptID); err == nil {
			t.Fatal("receipt mutation accepted")
		}
	}
	cancelled, cancelRead := context.WithCancel(ctx)
	cancelRead()
	if page, err := repo.ReadResourceAudit(cancelled, reader, scope, "invocation", invocationID, identity.New("cor"), "", 1); err == nil || len(page.Items) != 0 {
		t.Fatal("cancelled read disclosed records")
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO audit_records (id,isolation_domain_id,actor_id,action,resource_type,resource_id,outcome,correlation_id,occurred_at)
        VALUES ($1,$2,'corrupt','private/native/path','invocation',$3,'accepted',$4,clock_timestamp())`, identity.New("aud"), scope, invocationID, identity.New("cor")); err != nil {
		t.Fatal(err)
	}
	response = request(path, 503)
	if strings.Contains(response.Body.String(), "private/native/path") || strings.Contains(response.Body.String(), "items") {
		t.Fatal("corrupt evidence was disclosed")
	}
	migration, err := os.ReadFile("migrations/00060_public_resource_audit.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, down, _ := strings.Cut(string(migration), "-- dataground:down")
	tx, err := f.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "cannot remove resource audit read evidence") {
		t.Fatal("downgrade discarded receipt", err)
	}
}

func TestResourceAuditMigrationPreservesLegacyEvidence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	t.Cleanup(pool.Close)
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE audit_records,invocation_authorization_decisions,publication_authorization_decisions`); err != nil {
			t.Error(err)
		}
	})
	migration, err := os.ReadFile("migrations/00060_public_resource_audit.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, found := strings.Cut(string(migration), "-- dataground:down")
	if !found {
		t.Fatal("missing downgrade")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatal(err)
	}
	scope, revisionID, invocationID, operationID, serviceID := identity.New("iso"), identity.New("rev"), identity.New("inv"), identity.New("op"), identity.New("svc")
	if _, err := tx.Exec(ctx, `INSERT INTO audit_records (id,isolation_domain_id,actor_id,action,resource_type,resource_id,outcome,correlation_id,safe_metadata,occurred_at)
        VALUES ($1,$2,'legacy-actor','service-revision.created','service-revision',$3,'accepted',$4,'{"sensitive":true}','2026-08-01T12:00:00Z')`, identity.New("aud"), scope, revisionID, identity.New("cor")); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO invocation_authorization_decisions (isolation_domain_id,operation_id,invocation_id,service_id,revision_id,actor_id,action,outcome,policy_set_id,policy_digest,correlation_id)
        VALUES ($1,$2,$3,$4,$5,'legacy-actor','run','denied','legacy-policy',$6,$7)`, scope, operationID, invocationID, serviceID, revisionID, "sha256:"+strings.Repeat("a", 64), identity.New("cor")); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO publication_authorization_decisions (contract,isolation_domain_id,service_id,revision_id,operation_id,actor_id,correlation_id,phase,expected_version,fencing_token,plan_digest,verification_digest,policy_contract,policy_set_id,policy_digest,outcome)
        VALUES ('dataground.publication-authorization-decision/v1',$1,$2,$3,$4,'legacy-actor',$5,'entry',1,0,$6,$6,'dataground.invocation-authorization-policy/v5','legacy-policy',$6,'allowed')`, scope, serviceID, revisionID, operationID, identity.New("cor"), "sha256:"+strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	tables := []string{"audit_records", "invocation_authorization_decisions", "publication_authorization_decisions"}
	before := make([][]byte, len(tables))
	for i, table := range tables {
		if err := tx.QueryRow(ctx, `SELECT to_jsonb(t) FROM `+table+` t WHERE isolation_domain_id=$1`, scope).Scan(&before[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	for i, table := range tables {
		var after []byte
		if err := tx.QueryRow(ctx, `SELECT to_jsonb(t)-'recorded_transaction_id'-'public_audit_id' FROM `+table+` t WHERE isolation_domain_id=$1`, scope).Scan(&after); err != nil || !bytes.Equal(before[i], after) {
			t.Fatal("legacy evidence changed", table, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		var visible bool
		if err := pool.QueryRow(ctx, `SELECT pg_visible_in_snapshot(recorded_transaction_id,pg_current_snapshot()) FROM `+table+` WHERE isolation_domain_id=$1`, scope).Scan(&visible); err != nil || !visible {
			t.Fatal("legacy evidence not visible after upgrade", table, err)
		}
	}
}
