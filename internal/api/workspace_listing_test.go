package api

import (
	"github.com/asabla/dataground/internal/domain"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceCursorBindsScopeAndTieBreakIdentity(t *testing.T) {
	scope := "iso_00000000000000000001"
	cursor, err := encodeWorkspaceListCursor(domain.Workspace{Metadata: domain.ResourceMetadata{ID: "wsp_00000000000000000001", IsolationDomainID: scope, CreatedAt: time.Unix(1, 0).UTC()}}, "")
	if err != nil {
		t.Fatal(err)
	}
	q := url.Values{"cursor": {cursor}}
	if _, v, err := parseWorkspaceListQuery(q.Encode(), scope); err != nil || v.ID != "wsp_00000000000000000001" {
		t.Fatal("cursor changed", err)
	}
	if _, _, err := parseWorkspaceListQuery(q.Encode(), "iso_00000000000000000002"); err == nil {
		t.Fatal("foreign cursor accepted")
	}
	q.Set("cursor", cursor+"=")
	if _, _, err := parseWorkspaceListQuery(q.Encode(), scope); err == nil {
		t.Fatal("noncanonical cursor accepted")
	}
}

func TestWorkspaceSearchCursorBindsSearchWithinMaximumSize(t *testing.T) {
	w := domain.Workspace{Metadata: domain.ResourceMetadata{ID: "wsp_" + strings.Repeat("a", 32), IsolationDomainID: "iso_" + strings.Repeat("b", 32), CreatedAt: time.Now().UTC()}}
	cursor, err := encodeWorkspaceListCursor(w, "Report")
	if err != nil || len(cursor) > maximumWorkspaceCursorLen {
		t.Fatal("cursor exceeds contract", err)
	}
	for _, q := range []string{"REPORT", "Report"} {
		if _, _, err := parseWorkspaceListQuery("cursor="+cursor+"&q="+q, w.Metadata.IsolationDomainID); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{"", "Other"} {
		if _, _, err := parseWorkspaceListQuery("cursor="+cursor+"&q="+q, w.Metadata.IsolationDomainID); err == nil {
			t.Fatal("cursor search substitution accepted")
		}
	}
}
