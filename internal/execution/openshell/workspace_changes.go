package openshell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/workspace"
)

var ErrWorkspaceChangeExport = errors.New("workspace change export could not be confirmed")

const workspaceChangeExportCommand = "/usr/local/bin/dataground-workspace-export --destination /sandbox/workspace --archive-digest "

// ExportWorkspaceChanges transports one final difference from the exact sandbox.
// The caller must own current export authority, hold a workload quiescence fence
// for the entire exchange, and select an accepted image containing the helper.
// This transport grants no authority, records no durable export, and permits no
// teardown. An unconfirmed exchange must retain the workspace; retry requires
// the same base, current authority, and continued quiescence.
func (provider *Provider) ExportWorkspaceChanges(ctx context.Context, ref execution.ExecutionRef, base *workspace.Snapshot) (*workspace.ChangeSet, error) {
	if provider == nil || ctx == nil || base == nil || !workspaceArchiveDigestPattern.MatchString(base.Receipt().ArchiveDigest) {
		return nil, ErrWorkspaceChangeExport
	}
	transferCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	session, err := provider.startSandboxCommand(transferCtx, ref, workspaceChangeExportCommand+base.Receipt().ArchiveDigest)
	if err != nil {
		return nil, ErrWorkspaceChangeExport
	}
	encoded, err := exchangeWorkspaceSource(transferCtx, session, base, workspace.MaximumChangeSetBytes)
	defer clear(encoded)
	if err != nil {
		return nil, ErrWorkspaceChangeExport
	}
	sum := sha256.Sum256(encoded)
	changes, err := workspace.InspectChanges(base, encoded, "sha256:"+hex.EncodeToString(sum[:]))
	if err != nil || transferCtx.Err() != nil {
		return nil, ErrWorkspaceChangeExport
	}
	return changes, nil
}
