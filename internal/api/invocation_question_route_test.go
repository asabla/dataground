package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
)

func TestQuestionRoutesAuthorizeExactQuestionBeforeReadingBody(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		method, suffix string
		action         authz.Action
	}{
		{http.MethodGet, "", authz.ReadInvocationQuestion},
		{http.MethodPost, "/answers", authz.AnswerInvocationQuestion},
	} {
		t.Run(test.method, func(t *testing.T) {
			authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			handler, err := api.NewHandler(authenticator, authorizerFunc(func(_ context.Context, request authz.Request) error {
				called = true
				if request.Action != test.action || request.ResourceType != authz.InvocationQuestion || request.ResourceID != "qst_00000000000000000001" || request.IsolationDomainID != testDomain || request.Principal.ID() != testActor {
					t.Fatal("question action lost validated principal or exact path")
				}
				return authz.ErrDenied
			}))
			if err != nil {
				t.Fatal(err)
			}
			reader := &countingReader{}
			request := httptest.NewRequest(test.method, "/v1/isolation-domains/"+testDomain+"/invocations/inv_00000000000000000001/questions/qst_00000000000000000001"+test.suffix, reader)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || called || reader.reads != 0 {
				t.Fatalf("unauthenticated question route: %d", response.Code)
			}
			request.Header.Set("Authorization", "Bearer "+testToken)
			response = httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || !called || reader.reads != 0 {
				t.Fatalf("unauthorized question route: %d", response.Code)
			}
		})
	}
}
