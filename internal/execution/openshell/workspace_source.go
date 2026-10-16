package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/workspace"
)

var ErrWorkspaceSourceTransfer = errors.New("workspace source transfer could not be confirmed")

var workspaceArchiveDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

const workspaceMaterializationCommand = "/usr/local/bin/dataground-workspace-materialize --destination /sandbox/workspace --archive-digest "

// MaterializeWorkspace is an internal transport, not a source-use authorization
// or provisioning lease. The caller must keep the exact sandbox inactive, hold
// current effect authority, and select an accepted image containing the helper.
// A failed or lost acknowledgement is ambiguous; retry only under that authority
// while the workspace remains inactive. The helper observes an existing tree.
func (provider *Provider) MaterializeWorkspace(ctx context.Context, ref execution.ExecutionRef, source *workspace.Snapshot) (workspace.Receipt, error) {
	if provider == nil || ctx == nil || source == nil || !workspaceArchiveDigestPattern.MatchString(source.Receipt().ArchiveDigest) {
		return workspace.Receipt{}, ErrWorkspaceSourceTransfer
	}
	transferCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	// The digest is produced by Inspect and checked again here. No source path,
	// content, credentials, or caller-selected command enters SSH's command string.
	session, err := provider.startSandboxCommand(transferCtx, ref, workspaceMaterializationCommand+source.Receipt().ArchiveDigest)
	if err != nil {
		return workspace.Receipt{}, ErrWorkspaceSourceTransfer
	}
	return transferWorkspaceSource(transferCtx, session, source)
}

func transferWorkspaceSource(ctx context.Context, session execution.RuntimeSession, source *workspace.Snapshot) (workspace.Receipt, error) {
	stdout, err := exchangeWorkspaceSource(ctx, session, source, 4096)
	defer clear(stdout)
	expected := source.Receipt()
	canonical, _ := json.Marshal(expected)
	if err != nil || !bytes.Equal(bytes.TrimSpace(stdout), canonical) {
		return workspace.Receipt{}, ErrWorkspaceSourceTransfer
	}
	return expected, nil
}

// Both workspace helpers consume the exact base archive and require complete
// input, bounded output, an empty diagnostic stream and confirmed shutdown.
// On success the caller owns the sensitive output and must clear it after use.
func exchangeWorkspaceSource(ctx context.Context, session execution.RuntimeSession, source *workspace.Snapshot, maximumOutput int64) ([]byte, error) {
	defer session.Close()
	written := make(chan error, 1)
	go func() {
		err := source.WriteArchive(ctx, session.Input())
		closeErr := session.Input().Close()
		if err == nil {
			err = closeErr
		}
		written <- err
		if err != nil {
			_ = session.Close()
		}
	}()
	type readResult struct {
		data []byte
		err  error
	}
	read := func(reader io.Reader, limit int64) <-chan readResult {
		result := make(chan readResult, 1)
		go func() {
			data, err := io.ReadAll(io.LimitReader(reader, limit+1))
			if int64(len(data)) > limit {
				err = ErrWorkspaceSourceTransfer
			}
			result <- readResult{data, err}
			if err != nil {
				_ = session.Close()
			}
		}()
		return result
	}
	output := read(session.Output(), maximumOutput)
	diagnostic := read(session.Errors(), maxNativeDiagnosticBytes)
	waitErr := session.Wait()
	writeErr := <-written
	stdout, stderr := <-output, <-diagnostic
	retained := false
	defer func() {
		if !retained {
			clear(stdout.data)
		}
	}()
	defer clear(stderr.data)
	if ctx.Err() != nil || waitErr != nil || writeErr != nil || stdout.err != nil || stderr.err != nil || len(stderr.data) != 0 {
		return nil, ErrWorkspaceSourceTransfer
	}
	if err := session.Close(); err != nil {
		return nil, ErrWorkspaceSourceTransfer
	}
	retained = true
	return stdout.data, nil
}
