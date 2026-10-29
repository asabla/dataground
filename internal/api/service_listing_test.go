package api

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
)

func TestServiceSearchCursorCompatibilityAndScope(t *testing.T) {
	const scope = "iso_00000000000000000001"
	service := domain.AgentService{Metadata: domain.ResourceMetadata{ID: "svc_00000000000000000001", IsolationDomainID: scope, CreatedAt: time.Now().UTC()}}
	encoded, err := encodeServiceListCursor(service, "Report")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseServiceListQuery("q=REPORT&cursor="+encoded, scope); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseServiceListQuery("q=Report&cursor="+encoded, "iso_00000000000000000002"); err == nil {
		t.Fatal("cross-scope cursor accepted")
	}
	legacy, _ := json.Marshal(serviceListCursor{ID: service.Metadata.ID, CreatedAt: service.Metadata.CreatedAt})
	query := "cursor=" + base64.RawURLEncoding.EncodeToString(legacy)
	if _, _, err := parseServiceListQuery(query, scope); err != nil {
		t.Fatal("legacy unfiltered continuation rejected", err)
	}
	if _, _, err := parseServiceListQuery(query+"&q=Report", scope); err == nil {
		t.Fatal("legacy cursor used with search")
	}
	// All maximum-length resource identifiers and nanosecond timestamps must fit the public cursor bound.
	workspace := workspaceServiceCursor{Kind: "workspace-services", Version: 1, IsolationDomainID: "iso_" + strings.Repeat("a", 32), WorkspaceID: "wsp_" + strings.Repeat("b", 32), ID: "svc_" + strings.Repeat("c", 32), CreatedAt: time.Now().UTC(), Search: serviceSearchDigest(strings.Repeat("界", 128))}
	raw, _ := json.Marshal(workspace)
	cursor := base64.RawURLEncoding.EncodeToString(raw)
	if len(cursor) > maximumServiceCursorLen {
		t.Fatal("workspace search cursor exceeds contract", len(cursor))
	}
	if _, _, err := parseWorkspaceServiceQuery("cursor="+cursor+"&q="+url.QueryEscape(strings.Repeat("界", 128)), workspace.IsolationDomainID, workspace.WorkspaceID); err != nil {
		t.Fatal(err)
	}
}
