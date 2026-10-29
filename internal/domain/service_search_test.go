package domain

import (
	"strings"
	"testing"
)

func TestServiceSearchLiteralAndLocaleIndependent(t *testing.T) {
	s := AgentService{Name: "Ångström 100%_\\", Description: "Research NOTES", Metadata: ResourceMetadata{ID: "svc_00000000000000000001"}}
	for _, q := range []string{"", "100%_\\", "notes", "Ångström", s.Metadata.ID} {
		if !MatchesServiceSearch(s, q) {
			t.Fatalf("literal match missing: %q", q)
		}
	}
	for _, q := range []string{"åNGSTRÖM", "missing", ".*", "100anything"} {
		if MatchesServiceSearch(s, q) {
			t.Fatalf("search widened: %q", q)
		}
	}
	for _, q := range []string{"", strings.Repeat("界", 128), "%_\\"} {
		if !ValidDiscoverySearch(q) {
			t.Fatalf("valid search rejected: %q", q)
		}
	}
	for _, q := range []string{strings.Repeat("界", 129), "\n", "\u0085", string([]byte{0xff})} {
		if ValidDiscoverySearch(q) {
			t.Fatal("invalid search accepted")
		}
	}
}
