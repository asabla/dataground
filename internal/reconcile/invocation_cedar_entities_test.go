package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	cedar "github.com/cedar-policy/cedar-go"
)

func TestCedarInvocationAuthorizationEvaluatorUsesRevisionEntitySnapshot(t *testing.T) {
	t.Parallel()

	policy, err := NewInvocationAuthorizationPolicyWithEntities(
		InvocationAuthorizationPolicyScope{
			IsolationDomainID: "iso_1",
			ServiceID:         "svc_1",
			RevisionID:        "rev_1",
		},
		"policy.entities.v1",
		CanonicalInvocationCedarEntitySchema(),
		[]byte(`permit(
			principal in DataGround::Role::"invoker",
			action == DataGround::Action::"admit",
			resource
		);`),
		canonicalInvocationEntityFixture(t),
	)
	if err != nil {
		t.Fatalf("construct entity policy: %v", err)
	}

	evaluator := NewCedarInvocationAuthorizationEvaluator()
	input := testInvocationCedarInput(t, testInvocationAuthorizationRequest())
	if err := evaluator.EvaluateInvocationAuthorization(
		context.Background(),
		policy,
		input,
	); err != nil {
		t.Fatalf("evaluate entity-backed permit: %v", err)
	}

	input.Principal.ID = "actor_2"
	if err := evaluator.EvaluateInvocationAuthorization(
		context.Background(),
		policy,
		input,
	); !errors.Is(err, ErrInvocationAuthorizationDenied) {
		t.Fatalf("unlisted actor error = %v, want denial", err)
	}
}

func TestEntityPolicyConstructionPreservesExactReviewedBytes(t *testing.T) {
	t.Parallel()

	entities := canonicalInvocationEntityFixture(t)
	policy, err := NewInvocationAuthorizationPolicyWithEntities(
		InvocationAuthorizationPolicyScope{
			IsolationDomainID: "iso_1",
			ServiceID:         "svc_1",
			RevisionID:        "rev_1",
		},
		"policy.entities.v1",
		CanonicalInvocationCedarEntitySchema(),
		[]byte("permit(principal, action, resource);"),
		entities,
	)
	if err != nil {
		t.Fatalf("construct entity policy: %v", err)
	}
	if policy.Contract != InvocationAuthorizationPolicyEntityContract ||
		string(policy.Entities) != string(entities) {
		t.Fatalf("entity policy = %#v", policy)
	}
	entities[0] = 'X'
	if policy.Entities[0] == 'X' {
		t.Fatal("constructed policy shared caller-owned entity bytes")
	}

	changed := cloneInvocationAuthorizationPolicy(policy)
	changed.Entities[0] ^= 1
	if validInvocationAuthorizationPolicy(changed, InvocationAuthorizationPolicyScope{
		IsolationDomainID: "iso_1",
		ServiceID:         "svc_1",
		RevisionID:        "rev_1",
	}) {
		t.Fatal("entity snapshot drift remained valid")
	}
}

func TestEntityPolicyConstructionRejectsUnreviewableSnapshots(t *testing.T) {
	t.Parallel()

	scope := InvocationAuthorizationPolicyScope{
		IsolationDomainID: "iso_1",
		ServiceID:         "svc_1",
		RevisionID:        "rev_1",
	}
	canonical := canonicalInvocationEntityFixture(t)
	for name, entities := range map[string][]byte{
		"empty":        nil,
		"empty set":    []byte("[]"),
		"malformed":    []byte("["),
		"noncanonical": append(append([]byte(nil), canonical...), '\n'),
		"trailing":     append(append([]byte(nil), canonical...), []byte("{}")...),
		"oversized":    make([]byte, maxInvocationAuthorizationEntityBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewInvocationAuthorizationPolicyWithEntities(
				scope,
				"policy.entities.v1",
				CanonicalInvocationCedarEntitySchema(),
				[]byte("permit(principal, action, resource);"),
				entities,
			); !errors.Is(err, ErrInvocationAuthorizationPolicyInvalid) {
				t.Fatalf("construction error = %v", err)
			}
		})
	}
}

func canonicalInvocationEntityFixture(t *testing.T) []byte {
	t.Helper()
	raw := []byte(`[
		{"uid":{"type":"DataGround::Actor","id":"actor_1"},"attrs":{},"parents":[{"type":"DataGround::Role","id":"invoker"}]},
		{"uid":{"type":"DataGround::Role","id":"invoker"},"attrs":{},"parents":[]}
	]`)
	var entities cedar.EntityMap
	if err := json.Unmarshal(raw, &entities); err != nil {
		t.Fatalf("parse entity fixture: %v", err)
	}
	canonical, err := json.Marshal(entities)
	if err != nil {
		t.Fatalf("canonicalize entity fixture: %v", err)
	}
	return canonical
}
