package api

import (
	"net/http/httptest"
	"testing"
)

func TestResourceAuditQueryIsStrictAndReferenceHasNoDurableAudit(t *testing.T) {
	server := NewServer()
	const scope = "iso_00000000000000000001"
	for _, kind := range []string{"invocation", "revision"} {
		id := "inv_00000000000000000001"
		field := "invocationId"
		if kind == "revision" {
			id = "rev_00000000000000000001"
			field = "revisionId"
			server.revisions[resourceKey(scope, id)] = ServiceRevision{}
		} else {
			server.invocations[resourceKey(scope, id)] = Invocation{}
		}
		for _, test := range []struct {
			query  string
			status int
		}{
			{"", 503}, {"?limit=100", 503}, {"?limit=0", 400}, {"?limit=101", 400}, {"?limit=01", 400}, {"?limit=1&limit=2", 400}, {"?cursor=", 400}, {"?cursor=x", 400}, {"?cursor=x&cursor=x", 400}, {"?other=x", 400}, {"?limit=%ZZ", 400}, {"?limit=1;other=2", 400},
		} {
			r := httptest.NewRequest("GET", "/"+test.query, nil)
			r.SetPathValue("isolationDomainId", scope)
			r.SetPathValue(field, id)
			w := httptest.NewRecorder()
			server.readResourceAudit(w, r)
			if w.Code != test.status || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s %s: %d %s", kind, test.query, w.Code, w.Body.String())
			}
		}
	}
}
