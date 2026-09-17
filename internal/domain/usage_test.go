package domain

import "testing"

func TestUsageSnapshotRequiresExactBoundedCounts(t *testing.T) {
	for _, encoded := range []string{
		`{}`, `null`, `{"inputTokens":1,"outputTokens":2}`, `{"inputTokens":null,"outputTokens":2,"totalTokens":3}`,
		`{"inputTokens":-1,"outputTokens":2,"totalTokens":1}`, `{"inputTokens":1.5,"outputTokens":2,"totalTokens":3}`,
		`{"inputTokens":"1","outputTokens":2,"totalTokens":3}`, `{"inputTokens":1,"outputTokens":2,"totalTokens":9007199254740992}`,
		`{"inputTokens":1,"outputTokens":2,"totalTokens":3,"nativeThread":"private"}`,
	} {
		if _, err := ParseUsageSnapshot([]byte(encoded)); err != ErrUsageInvalid {
			t.Fatalf("invalid snapshot accepted: %s", encoded)
		}
	}
	for _, encoded := range []string{
		`{"inputTokens":0,"outputTokens":0,"totalTokens":0}`,
		`{"inputTokens":0,"outputTokens":0,"totalTokens":100}`, // Native context-window correction.
		`{"inputTokens":9007199254740991,"outputTokens":0,"totalTokens":9007199254740991}`,
	} {
		if _, err := ParseUsageSnapshot([]byte(encoded)); err != nil {
			t.Fatal(err)
		}
	}
}
