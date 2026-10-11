package api

import (
	"github.com/asabla/dataground/internal/domain"
	"net/url"
	"testing"
	"time"
)

func TestWorkspaceCursorBindsScopeAndTieBreakIdentity(t *testing.T) {
	scope := "iso_00000000000000000001"
	cursor, err := encodeWorkspaceListCursor(domain.Workspace{Metadata: domain.ResourceMetadata{ID: "wsp_00000000000000000001", IsolationDomainID: scope, CreatedAt: time.Unix(1, 0).UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	q := url.Values{"cursor": {cursor}}
	if _, v, err := parseWorkspaceListQuery(q, scope); err != nil || v.ID != "wsp_00000000000000000001" {
		t.Fatal("cursor changed", err)
	}
	if _, _, err := parseWorkspaceListQuery(q, "iso_00000000000000000002"); err == nil {
		t.Fatal("foreign cursor accepted")
	}
	q.Set("cursor", cursor+"=")
	if _, _, err := parseWorkspaceListQuery(q, scope); err == nil {
		t.Fatal("noncanonical cursor accepted")
	}
}
