package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceServiceCursorBindsWorkspaceScopeAndPurpose(t *testing.T) {
	const scope = "iso_00000000000000000001"
	const workspace = "wsp_00000000000000000001"
	cursor := workspaceServiceCursor{Kind: "workspace-services", Version: 1, IsolationDomainID: scope, WorkspaceID: workspace, CreatedAt: time.Now().UTC(), ID: "svc_00000000000000000001"}
	encode := func(value workspaceServiceCursor) string {
		data, _ := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(data)
	}
	limit, decoded, err := parseWorkspaceServiceQuery("limit=2&cursor="+encode(cursor), scope, workspace)
	if err != nil || limit != 2 || *decoded != cursor {
		t.Fatalf("round trip: %d %#v %v", limit, decoded, err)
	}
	for _, query := range []string{"state=", "state=future", "state=pending", "limit=0", "limit=101", "limit=01", "limit=1&limit=2", "cursor=", "other=1", "limit=%ZZ", "cursor=" + strings.Repeat("a", 513)} {
		if _, _, err := parseWorkspaceServiceQuery(query, scope, workspace); err == nil {
			t.Fatalf("invalid query accepted: %s", query)
		}
	}
	for _, mutate := range []func(*workspaceServiceCursor){func(c *workspaceServiceCursor) { c.Kind = "invocation-artifacts" }, func(c *workspaceServiceCursor) { c.Version++ }, func(c *workspaceServiceCursor) { c.WorkspaceID = "wsp_00000000000000000002" }, func(c *workspaceServiceCursor) { c.IsolationDomainID = "iso_00000000000000000002" }, func(c *workspaceServiceCursor) { c.ID = "bad" }, func(c *workspaceServiceCursor) { c.CreatedAt = time.Time{} }} {
		changed := cursor
		mutate(&changed)
		if _, _, err := parseWorkspaceServiceQuery("cursor="+encode(changed), scope, workspace); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
	canonical, _ := json.Marshal(cursor)
	for _, raw := range []string{" " + string(canonical), string(canonical) + "{}", strings.Replace(string(canonical), "{", `{"version":1,`, 1)} {
		if _, _, err := parseWorkspaceServiceQuery("cursor="+base64.RawURLEncoding.EncodeToString([]byte(raw)), scope, workspace); err == nil {
			t.Fatal("noncanonical cursor accepted")
		}
	}
}

func TestDurableWorkspaceServiceFailureIsSafe(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.SetPathValue("isolationDomainId", "iso_00000000000000000001")
	request.SetPathValue("workspaceId", "wsp_00000000000000000001")
	response := httptest.NewRecorder()
	(&DurableServer{}).listWorkspaceAgentServices(response, request)
	if response.Code != 503 || !strings.Contains(response.Body.String(), "WORKSPACE_SERVICE_LIST_UNAVAILABLE") {
		t.Fatal(response.Code, response.Body.String())
	}
}
