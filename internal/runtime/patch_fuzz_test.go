package runtime

import (
	"encoding/json"
	"reflect"
	"testing"
)

func FuzzCompletedPatchRoundTrip(f *testing.F) {
	f.Add([]byte(`{"schemaVersion":"dataground.runtime-patch/v1","status":"completed","changes":[]}`))
	f.Add([]byte(`{"schemaVersion":"dataground.runtime-patch/v1","status":"completed","changes":[{"path":"notes/draft.txt","kind":"update","diff":"+text","movePath":"notes/final.txt"}]}`))
	f.Add([]byte(`{"schemaVersion":"dataground.runtime-patch/v1","status":"completed","changes":[{"path":"notes.txt","kind":"add","diff":"","movePath":null}]}`))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > MaximumPatchBytes {
			return
		}
		var payload map[string]any
		if json.Unmarshal(input, &payload) != nil {
			return
		}
		patch, err := ParseCompletedPatch(payload)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(patch)
		if err != nil || len(encoded) > MaximumPatchBytes {
			t.Fatalf("accepted patch exceeds its encoded bounds: %d bytes, %v", len(encoded), err)
		}
		if len(patch.Changes) > MaximumPatchChanges {
			t.Fatalf("accepted patch exceeds its change limit: %d", len(patch.Changes))
		}
		roundTrip, err := ParseCompletedPatch(patch.Payload())
		if err != nil || !reflect.DeepEqual(roundTrip, patch) {
			t.Fatalf("accepted patch changed on round trip: %v", err)
		}
	})
}
