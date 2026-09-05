package persistence

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"testing"

	cedar "github.com/cedar-policy/cedar-go"
)

func TestInvocationAuthorizationEntityRefreshContracts(t *testing.T) {
	t.Parallel()
	var entityMap cedar.EntityMap
	if err := json.Unmarshal([]byte(`[
		{"uid":{"type":"DataGround::Role","id":"invoker"},"attrs":{},"parents":[]}
	]`), &entityMap); err != nil {
		t.Fatal(err)
	}
	entities, err := json.Marshal(entityMap)
	if err != nil {
		t.Fatal(err)
	}
	entityDigest := sha256.Sum256(entities)
	generation := InvocationAuthorizationEntityGeneration{
		Contract:          InvocationAuthorizationEntityGenerationContract,
		IsolationDomainID: "iso_00000000000000000001",
		ServiceID:         "svc_00000000000000000001",
		RevisionID:        "rev_00000000000000000001",
		Generation:        1,
		EntityDigest:      entityDigest[:],
		Entities:          entities,
		PublishedBy:       "operator",
		CorrelationID:     "cor_00000000000000000001",
		ReasonDigest:      bytes.Repeat([]byte{0x22}, sha256.Size),
	}
	if !generation.Valid() {
		t.Fatal("valid entity generation was rejected")
	}
	changed := cloneInvocationAuthorizationEntityGeneration(generation)
	changed.Entities = []byte("[ ]")
	if changed.Valid() {
		t.Fatal("noncanonical entity generation was accepted")
	}
	changed = cloneInvocationAuthorizationEntityGeneration(generation)
	changed.EntityDigest[0] ^= 0xff
	if changed.Valid() {
		t.Fatal("entity generation digest drift was accepted")
	}
	if generation.EntityDigest[0] == changed.EntityDigest[0] {
		t.Fatal("entity generation clone shared digest storage")
	}

	activation := InvocationAuthorizationEntityActivation{
		Contract:              InvocationAuthorizationEntityActivationContract,
		IsolationDomainID:     generation.IsolationDomainID,
		ServiceID:             generation.ServiceID,
		RevisionID:            generation.RevisionID,
		Generation:            generation.Generation,
		InstalledPolicyDigest: bytes.Repeat([]byte{0x33}, sha256.Size),
		ActivatedBy:           "operator",
		CorrelationID:         "cor_00000000000000000002",
		ReasonDigest:          bytes.Repeat([]byte{0x44}, sha256.Size),
	}
	if !activation.Valid() {
		t.Fatal("valid entity activation was rejected")
	}
	activation.EffectivePolicyDigest = []byte{0x01}
	if activation.Valid() {
		t.Fatal("invalid optional effective digest was accepted")
	}
}

func TestInvocationAuthorizationEntityRefreshRejectsNonEntityPolicyContracts(t *testing.T) {
	t.Parallel()
	for _, contract := range []string{"", "dataground.invocation-authorization-policy/v1", "dataground.invocation-authorization-policy/v5"} {
		record := InvocationAuthorizationPolicyRecord{Contract: contract, Schema: []byte("schema"), Policies: []byte("policy")}
		if _, supported := record.entityPolicyDigest([]byte("[]")); supported {
			t.Fatalf("unsupported entity refresh contract accepted: %q", contract)
		}
	}
}
