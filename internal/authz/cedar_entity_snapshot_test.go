package authz

import (
	"encoding/json"
	"testing"

	cedar "github.com/cedar-policy/cedar-go"
)

func TestParseInvocationCedarEntitySnapshotAcceptsClosedMembership(t *testing.T) {
	t.Parallel()

	encoded := canonicalInvocationEntitySnapshot(t, `[
		{"uid":{"type":"DataGround::Actor","id":"actor_1"},"attrs":{},"parents":[{"type":"DataGround::Role","id":"invoker"}]},
		{"uid":{"type":"DataGround::Role","id":"invoker"},"attrs":{},"parents":[]}
	]`)
	entities, err := ParseInvocationCedarEntitySnapshot(encoded)
	if err != nil {
		t.Fatalf("parse entity snapshot: %v", err)
	}
	if len(entities) != 2 {
		t.Fatalf("entity count = %d", len(entities))
	}
}

func TestParseInvocationCedarEntitySnapshotRejectsWidenedModels(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"unknown type": `[
			{"uid":{"type":"DataGround::Group","id":"group_1"},"attrs":{},"parents":[]}
		]`,
		"attributes": `[
			{"uid":{"type":"DataGround::Actor","id":"actor_1"},"attrs":{"domain":"other"},"parents":[],"tags":{}}
		]`,
		"tags": `[
			{"uid":{"type":"DataGround::Actor","id":"actor_1"},"attrs":{},"parents":[],"tags":{"source":"external"}}
		]`,
		"role parent": `[
			{"uid":{"type":"DataGround::Role","id":"child"},"attrs":{},"parents":[{"type":"DataGround::Role","id":"parent"}]},
			{"uid":{"type":"DataGround::Role","id":"parent"},"attrs":{},"parents":[]}
		]`,
		"missing parent": `[
			{"uid":{"type":"DataGround::Actor","id":"actor_1"},"attrs":{},"parents":[{"type":"DataGround::Role","id":"missing"}]}
		]`,
		"duplicate identity": `[
			{"uid":{"type":"DataGround::Role","id":"invoker"},"attrs":{},"parents":[]},
			{"uid":{"type":"DataGround::Role","id":"invoker"},"attrs":{},"parents":[]}
		]`,
		"unknown field": `[
			{"uid":{"type":"DataGround::Role","id":"invoker"},"attrs":{},"parents":[],"other":true}
		]`,
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseInvocationCedarEntitySnapshot([]byte(document)); err == nil {
				t.Fatal("widened entity model was accepted")
			}
		})
	}
}

func TestParseInvocationCedarEntitySnapshotRequiresCanonicalBytes(t *testing.T) {
	t.Parallel()

	canonical := canonicalInvocationEntitySnapshot(t, `[
		{"uid":{"type":"DataGround::Role","id":"invoker"},"attrs":{},"parents":[]}
	]`)
	if _, err := ParseInvocationCedarEntitySnapshot(append(canonical, '\n')); err == nil {
		t.Fatal("noncanonical entity bytes were accepted")
	}
}

func canonicalInvocationEntitySnapshot(t *testing.T, document string) []byte {
	t.Helper()
	var entities cedar.EntityMap
	if err := json.Unmarshal([]byte(document), &entities); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(entities)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
