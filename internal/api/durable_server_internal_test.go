package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asabla/dataground/internal/persistence"
)

func TestInvocationCommandIdempotencyBindsGovernedDispatchTarget(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodPost, "/v1/isolation-domains/iso/services/svc:invoke", nil)
	request.Header.Set("Idempotency-Key", "stable-key")
	body := []byte(`{"alias":"current","input":{"prompt":"run"}}`)
	baseline := commandIdempotency(request, "iso_00000000000000000001", "actor", body)
	if got := invocationCommandIdempotency(
		request, baseline.IsolationDomainID, "actor", body, nil,
	); got != baseline {
		t.Fatal("unconfigured invocation idempotency changed")
	}

	target := persistence.InvocationDispatchTarget{
		IsolationDomainID: "iso_00000000000000000001",
		ServiceID:         "svc_00000000000000000001",
		RevisionID:        "rev_00000000000000000001",
		RuntimeProfile:    persistence.GovernedInvocationRuntimeProfile,
	}
	bound := invocationCommandIdempotency(
		request, baseline.IsolationDomainID, "actor", body, &target,
	)
	if bound.RequestDigest == baseline.RequestDigest {
		t.Fatal("governed dispatch target was not bound to idempotency")
	}
	if replay := invocationCommandIdempotency(
		request, baseline.IsolationDomainID, "actor", body, &target,
	); replay.RequestDigest != bound.RequestDigest {
		t.Fatal("identical governed dispatch target changed idempotency")
	}

	changed := target
	changed.RevisionID = "rev_00000000000000000002"
	if got := invocationCommandIdempotency(
		request, baseline.IsolationDomainID, "actor", body, &changed,
	); got.RequestDigest == bound.RequestDigest {
		t.Fatal("changed governed dispatch target reused idempotency")
	}
}
