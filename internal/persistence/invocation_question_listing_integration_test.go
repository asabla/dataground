package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestInvocationQuestionDiscoveryIsScopedDurableBoundedAndAudited(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	t.Cleanup(func() {
		if _, err := fixture.pool.Exec(context.Background(), `TRUNCATE api_authorization_decisions`); err != nil {
			t.Error(err)
		}
	})
	domainID, invocationID := fixture.target.IsolationDomainID, fixture.target.InvocationID
	empty, err := fixture.repository.ListInvocationQuestions(ctx, domainID, invocationID, nil, "", 1)
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.HasMore {
		t.Fatalf("empty discovery: %#v, %v", empty, err)
	}
	first := fixture.request(t, ctx, 20*time.Second)
	second := fixture.request(t, ctx, 20*time.Second)
	// Equal creation times require the identifier as a stable ordering boundary.
	tiedIDs := []string{"qst_00000000000000000002", "qst_00000000000000000001"}
	tiedTime := first.CreatedAt.Add(-time.Minute)
	for i, id := range tiedIDs {
		if _, err := fixture.pool.Exec(ctx, `INSERT INTO invocation_runtime_questions
            (contract,isolation_domain_id,id,operation_id,invocation_id,service_id,revision_id,effect_id,source_sequence,correlation_id,requested_by,prompts,expires_at,state,version,created_at,updated_at)
            SELECT contract,isolation_domain_id,$3,operation_id,invocation_id,service_id,revision_id,effect_id,$4,correlation_id,requested_by,prompts,expires_at,'pending',1,$5,$5
            FROM invocation_runtime_questions WHERE isolation_domain_id=$1 AND id=$2`, domainID, first.ID, id, 100+i, tiedTime); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, questionAnswer(second), allowQuestion); err != nil {
		t.Fatal(err)
	}
	page, err := fixture.repository.ListInvocationQuestions(ctx, domainID, invocationID, nil, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != second.ID || !page.HasMore {
		t.Fatalf("first page: %#v, %v", page, err)
	}
	if _, err := fixture.repository.CloseInvocationRuntimeQuestion(ctx, fixture.claim, fixture.effect, first.ID, "runtime-ended"); err != nil {
		t.Fatal(err)
	}
	newest := fixture.request(t, ctx, 20*time.Second)
	restarted := persistence.NewRepository(fixture.pool)
	continuation, err := restarted.ListInvocationQuestions(ctx, domainID, invocationID, &page.Items[0].CreatedAt, page.Items[0].ID, 2)
	if err != nil || len(continuation.Items) != 2 || !continuation.HasMore || continuation.Items[0].ID != first.ID || continuation.Items[0].State != "closed" || continuation.Items[1].ID != tiedIDs[0] {
		t.Fatalf("continuation: %#v, %v", continuation, err)
	}
	last := continuation.Items[1]
	tail, err := restarted.ListInvocationQuestions(ctx, domainID, invocationID, &last.CreatedAt, last.ID, 2)
	if err != nil || len(tail.Items) != 1 || tail.HasMore || tail.Items[0].ID != tiedIDs[1] || tail.Items[0].SchemaVersion != domain.InvocationQuestionSummarySchemaV1 {
		t.Fatalf("timestamp tie: %#v, %v", tail, err)
	}
	refreshed, err := restarted.ListInvocationQuestions(ctx, domainID, invocationID, nil, "", 100)
	if err != nil || len(refreshed.Items) != 5 || refreshed.Items[0].ID != newest.ID || refreshed.HasMore {
		t.Fatalf("refresh: %#v, %v", refreshed, err)
	}
	for _, item := range refreshed.Items {
		read, err := restarted.GetInvocationQuestion(ctx, domainID, invocationID, item.ID)
		if err != nil || read.State != item.State || read.Version != item.Version || !read.CreatedAt.Equal(item.CreatedAt) || !read.UpdatedAt.Equal(item.UpdatedAt) || !read.ExpiresAt.Equal(item.ExpiresAt) {
			t.Fatal("collection metadata differs from authoritative read")
		}
	}
	summaries, err := json.Marshal(refreshed.Items)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private prompt sentinel", "private answer sentinel", "questions", "answers", "answeredBy", "requestedBy"} {
		if strings.Contains(string(summaries), forbidden) {
			t.Fatalf("discovery exposed %s", forbidden)
		}
	}
	for _, scope := range [][2]string{{identity.New("iso"), invocationID}, {domainID, identity.New("inv")}} {
		_, err := restarted.ListInvocationQuestions(ctx, scope[0], scope[1], nil, "", 1)
		var missing *persistence.DomainError
		if !errors.As(err, &missing) || missing.Code != "RESOURCE_NOT_FOUND" {
			t.Fatalf("cross-scope discovery: %v", err)
		}
	}
	for _, limit := range []int{0, 101} {
		if _, err := restarted.ListInvocationQuestions(ctx, domainID, invocationID, nil, "", limit); !errors.Is(err, persistence.ErrInvocationRuntimeQuestionInvalid) {
			t.Fatal("unbounded list accepted")
		}
	}
	if _, err := restarted.ListInvocationQuestions(ctx, domainID, invocationID, &tiedTime, "", 1); !errors.Is(err, persistence.ErrInvocationRuntimeQuestionInvalid) {
		t.Fatal("partial cursor accepted")
	}

	actor := identity.New("usr")
	const token = "question-discovery-test-token-thirty-two-bytes"
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: actor, IsolationDomainID: domainID})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{PolicySetID: "question-discovery-test", Schema: authz.CanonicalAPICedarSchema(), Policies: []byte(`permit(principal, action == DataGround::Action::"listInvocationQuestions", resource);`)})
	if err != nil {
		t.Fatal(err)
	}
	audited, err := authz.NewAuditedAuthorizer(policy, fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewDurableHandler(restarted, authenticator, audited)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/isolation-domains/" + domainID + "/invocations/" + invocationID + "/questions"
	request := func(path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("discovery HTTP: %d %s", response.Code, response.Body.String())
		}
		return response
	}
	response := request(path+"?limit=1", http.StatusOK)
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("question discovery can be cached")
	}
	var httpPage struct {
		Items      []domain.InvocationQuestionSummary `json:"items"`
		NextCursor string                             `json:"nextCursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &httpPage); err != nil || len(httpPage.Items) != 1 || httpPage.NextCursor == "" {
		t.Fatal("HTTP page lost bound or continuation")
	}
	for _, forbidden := range []string{"operationId", "effectId", "serviceId", "revisionId", "sourceSequence", "effectiveDecision"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("public collection exposed %s", forbidden)
		}
	}
	request(path+"?limit=1&cursor="+httpPage.NextCursor, http.StatusOK)
	request(path+"/"+newest.ID, http.StatusForbidden)
	answerRequest := httptest.NewRequest(http.MethodPost, path+"/"+newest.ID+"/answers", strings.NewReader(`{}`)).WithContext(ctx)
	answerRequest.Header.Set("Authorization", "Bearer "+token)
	answerResponse := httptest.NewRecorder()
	handler.ServeHTTP(answerResponse, answerRequest)
	if answerResponse.Code != http.StatusForbidden {
		t.Fatalf("discovery grant permits answer: %d", answerResponse.Code)
	}

	request(strings.Replace(path, invocationID, identity.New("inv"), 1)+"?cursor="+httpPage.NextCursor, http.StatusBadRequest)
	var count int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM api_authorization_decisions WHERE isolation_domain_id=$1 AND principal_id=$2 AND action='listInvocationQuestions' AND resource_type='DataGround::Invocation' AND resource_id=$3 AND outcome='allowed'`, domainID, actor, invocationID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("discovery audit: %d %v", count, err)
	}
	// A policy change denies the same cursor before reading another page.
	deny, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{PolicySetID: "question-discovery-deny", Schema: authz.CanonicalAPICedarSchema(), Policies: []byte(`forbid(principal,action,resource);`)})
	if err != nil {
		t.Fatal(err)
	}
	deniedAudit, err := authz.NewAuditedAuthorizer(deny, fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err = api.NewDurableHandler(restarted, authenticator, deniedAudit)
	if err != nil {
		t.Fatal(err)
	}
	request(path+"?cursor="+httpPage.NextCursor, http.StatusForbidden)
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM api_authorization_decisions WHERE isolation_domain_id=$1 AND action='listInvocationQuestions' AND outcome='denied'`, domainID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("discovery denial audit: %d %v", count, err)
	}
	migration, err := os.ReadFile("migrations/00056_invocation_question_discovery.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, down, found := strings.Cut(string(migration), "-- dataground:down")
	if !found {
		t.Fatal("missing downgrade")
	}
	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "cannot remove invocation question discovery authorization evidence") {
		t.Fatalf("downgrade discarded audit: %v", err)
	}
}
