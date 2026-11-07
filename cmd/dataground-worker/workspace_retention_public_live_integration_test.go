package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
)

func (live *liveWorkspaceAPI) recoverRetention(failed domain.Invocation) {
	live.t.Helper()
	metadataPath := live.root() + "/invocations/" + failed.Metadata.ID + "/workspace-retention"
	live.call(http.MethodGet, metadataPath, nil, "", false, http.StatusUnauthorized)
	live.call(http.MethodGet, strings.Replace(metadataPath, failed.Metadata.IsolationDomainID, identity.New("iso"), 1), nil, "", true, http.StatusForbidden)
	var metadata domain.WorkspaceRetention
	if json.Unmarshal(live.call(http.MethodGet, metadataPath, nil, "", true, http.StatusOK), &metadata) != nil || metadata.State != "export_pending" || metadata.Recovery != "review_required" || metadata.RecoveryAccess != "allowed" {
		live.t.Fatal("public retention metadata did not expose the pending recovery")
	}
	path := metadataPath + "/recoveries"
	body := map[string]any{"expectedVersion": failed.Metadata.Version}
	live.call(http.MethodPost, path, body, "public-retention-no-auth", false, http.StatusUnauthorized)
	live.call(http.MethodPost, strings.Replace(path, failed.Metadata.IsolationDomainID, identity.New("iso"), 1), body, "public-retention-cross-scope", true, http.StatusForbidden)
	accepted := live.call(http.MethodPost, path, body, "live-public-retention-recovery", true, http.StatusAccepted)
	replay := live.call(http.MethodPost, path, body, "live-public-retention-recovery", true, http.StatusAccepted)
	var receipt domain.WorkspaceRetentionRecoveryReceipt
	if !bytes.Equal(accepted, replay) || json.Unmarshal(accepted, &receipt) != nil || receipt.State != "requested" || receipt.InvocationID != failed.Metadata.ID || receipt.IsolationDomainID != failed.Metadata.IsolationDomainID || receipt.InvocationVersion != int64(failed.Metadata.Version)+1 {
		live.t.Fatal("public recovery receipt or exact replay changed")
	}
	live.call(http.MethodPost, path, map[string]any{"expectedVersion": failed.Metadata.Version + 1}, "live-public-retention-recovery", true, http.StatusConflict)
	live.retentionReplay = func() {
		var retained domain.WorkspaceRetention
		if json.Unmarshal(live.call(http.MethodGet, metadataPath, nil, "", true, http.StatusOK), &retained) != nil || retained.State != "recorded" || retained.Recovery != "none" || retained.ArtifactID == "" || retained.RecordedAt == nil {
			live.t.Fatal("public retention metadata did not record the final changes")
		}
		if !bytes.Equal(accepted, live.call(http.MethodPost, path, body, "live-public-retention-recovery", true, http.StatusAccepted)) {
			live.t.Fatal("terminal recovery replay changed its receipt")
		}
		if live.invocation(failed.Metadata.ID).State != "cancelled" {
			live.t.Fatal("terminal recovery replay reopened cancellation")
		}
	}
}
