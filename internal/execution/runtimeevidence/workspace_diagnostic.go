package runtimeevidence

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/workspace"
)

const workspaceDiagnosticSchemaVersion = "dataground.dev.openshell-runtime-diagnostic/v6"
const workspaceDiagnosticProfile = "openshell-workspace-source-diagnostic/v1"

// This evidence describes a synthetic fixture, never a user's source tree. The
// owning launcher binds it to the same sandbox, images and cleanup as the model
// cases. It confers neither source authorization nor runtime acceptance.
type diagnosticWorkspaceSource struct {
	Profile       string    `json:"profile"`
	RunID         string    `json:"runId"`
	StartedAt     string    `json:"startedAt"`
	FinishedAt    string    `json:"finishedAt"`
	ArchiveDigest string    `json:"archiveDigest"`
	Checks        [6]string `json:"checks"`
}

var workspaceDiagnosticChecks = [6]string{"cross-domain-denial", "source-installation", "exact-read-back", "substitution-denial", "native-source-content", "native-source-executable"}

func validWorkspaceDiagnostic(value *diagnosticWorkspaceSource, runID string) bool {
	if value == nil || value.Profile != workspaceDiagnosticProfile || value.RunID != runID || !runIDPattern.MatchString(runID) || value.Checks != workspaceDiagnosticChecks {
		return false
	}
	started, startErr := time.Parse(time.RFC3339Nano, value.StartedAt)
	finished, finishErr := time.Parse(time.RFC3339Nano, value.FinishedAt)
	source, err := workspaceDiagnosticSource(false)
	return startErr == nil && finishErr == nil && finished.After(started) && finished.Sub(started) <= time.Minute && err == nil && value.ArchiveDigest == source.Receipt().ArchiveDigest
}

func workspaceDiagnosticSource(changed bool) (*workspace.Snapshot, error) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	content := []byte{0, 255, 10, 42}
	if changed {
		content[3]++
	}
	for _, entry := range []struct {
		header tar.Header
		data   []byte
	}{
		{tar.Header{Name: "src/data.bin", Mode: 0644}, content},
		{tar.Header{Name: "run.sh", Mode: 0755}, []byte("#!/bin/sh\nprintf 'DATAGROUND_SOURCE_EXECUTABLE_OK\\n'\n")},
		{tar.Header{Name: "data-link", Typeflag: tar.TypeSymlink, Linkname: "src/data.bin", Mode: 0777}, nil},
	} {
		entry.header.Size = int64(len(entry.data))
		if err := writer.WriteHeader(&entry.header); err != nil {
			return nil, ErrCase
		}
		if _, err := writer.Write(entry.data); err != nil {
			return nil, ErrCase
		}
	}
	if err := writer.Close(); err != nil {
		return nil, ErrCase
	}
	digest := sha256.Sum256(archive.Bytes())
	return workspace.Inspect(archive.Bytes(), "sha256:"+hex.EncodeToString(digest[:]))
}

type workspaceDiagnosticProvider interface {
	MaterializeWorkspace(context.Context, execution.ExecutionRef, *workspace.Snapshot) (workspace.Receipt, error)
	StartRuntime(context.Context, execution.ExecutionRef) (execution.RuntimeSession, error)
}

func checkLauncherWorkspace(ctx context.Context, runID string, value execution.Execution, ports launcherPorts) (*diagnosticWorkspaceSource, error) {
	bound, ok := ports.(*runtimeLauncherPorts)
	if !ok || bound.provider == nil || value.IsolationDomainID != runtimeIsolationDomain(runID) || value.ID != runtimeExecutionID(runID) {
		return nil, ErrCase
	}
	return checkWorkspaceDiagnostic(ctx, runID, bound.provider, execution.ExecutionRef{IsolationDomainID: value.IsolationDomainID, ID: value.ID})
}

func checkWorkspaceDiagnostic(ctx context.Context, runID string, provider workspaceDiagnosticProvider, ref execution.ExecutionRef) (*diagnosticWorkspaceSource, error) {
	if ctx == nil || provider == nil || !runIDPattern.MatchString(runID) || ref.IsolationDomainID != runtimeIsolationDomain(runID) || ref.ID != runtimeExecutionID(runID) {
		return nil, ErrCase
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	started := time.Now().UTC()
	source, err := workspaceDiagnosticSource(false)
	if err != nil {
		return nil, ErrCase
	}
	wrong := ref
	wrong.IsolationDomainID = runtimeIsolationDomain(runID + ":other")
	if _, err := provider.MaterializeWorkspace(ctx, wrong, source); err == nil {
		return nil, ErrCase
	}
	for range 2 {
		if receipt, err := provider.MaterializeWorkspace(ctx, ref, source); err != nil || receipt != source.Receipt() {
			return nil, ErrCase
		}
	}
	changed, err := workspaceDiagnosticSource(true)
	if err != nil {
		return nil, ErrCase
	}
	if _, err := provider.MaterializeWorkspace(ctx, ref, changed); err == nil {
		return nil, ErrCase
	}
	if receipt, err := provider.MaterializeWorkspace(ctx, ref, source); err != nil || receipt != source.Receipt() {
		return nil, ErrCase
	}
	if err := checkWorkspaceNative(ctx, provider, ref); err != nil || ctx.Err() != nil {
		return nil, ErrCase
	}
	value := &diagnosticWorkspaceSource{Profile: workspaceDiagnosticProfile, RunID: runID, StartedAt: started.Format(time.RFC3339Nano), FinishedAt: time.Now().UTC().Format(time.RFC3339Nano), ArchiveDigest: source.Receipt().ArchiveDigest, Checks: workspaceDiagnosticChecks}
	if !validWorkspaceDiagnostic(value, runID) {
		return nil, ErrCase
	}
	return value, nil
}

const workspaceNativeProbe = `import json,os,stat
root='/sandbox/workspace'
print(json.dumps({'content':open(root+'/src/data.bin','rb').read().hex(),'fileMode':stat.S_IMODE(os.stat(root+'/src/data.bin').st_mode),'executableMode':stat.S_IMODE(os.stat(root+'/run.sh').st_mode),'link':os.readlink(root+'/data-link'),'linkedContent':open(root+'/data-link','rb').read().hex()}))`

func checkWorkspaceNative(ctx context.Context, provider workspaceDiagnosticProvider, ref execution.ExecutionRef) (outcome error) {
	session, err := provider.StartRuntime(ctx, ref)
	if err != nil || session == nil {
		return ErrCase
	}
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	diagnostics := make(chan int64, 1)
	go func() {
		n, _ := io.CopyN(io.Discard, session.Errors(), (64<<10)+1)
		if n > 64<<10 {
			_ = session.Close()
		}
		diagnostics <- n
	}()
	defer func() {
		if err := session.Close(); err != nil {
			outcome = ErrCase
		}
		if n := <-diagnostics; n > 64<<10 || ctx.Err() != nil {
			outcome = ErrCase
		}
	}()
	scanner := bufio.NewScanner(io.LimitReader(session.Output(), 128<<10))
	scanner.Buffer(make([]byte, 4096), 16<<10)
	id := 0
	call := func(method string, params any) (json.RawMessage, error) {
		id++
		message, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
		if err != nil {
			return nil, ErrCase
		}
		message = append(message, '\n')
		if n, err := session.Input().Write(message); err != nil || n != len(message) {
			return nil, ErrCase
		}
		for range 32 {
			if !scanner.Scan() {
				return nil, ErrCase
			}
			var response struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(scanner.Bytes(), &response) != nil {
				return nil, ErrCase
			}
			if response.ID == nil && response.Method != "" {
				continue
			}
			if response.ID == nil || *response.ID != id || response.Method != "" || len(response.Error) != 0 || len(response.Result) == 0 || bytes.Equal(response.Result, []byte("null")) {
				return nil, ErrCase
			}
			return response.Result, nil
		}
		return nil, ErrCase
	}
	if _, err := call("initialize", map[string]any{"clientInfo": map[string]any{"name": "dataground-workspace-diagnostic", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}}); err != nil {
		return ErrCase
	}
	command := func(args []string) (string, error) {
		response, err := call("command/exec", map[string]any{"command": args, "cwd": "/sandbox/workspace", "sandboxPolicy": map[string]any{"type": "readOnly", "networkAccess": false}, "timeoutMs": 10000, "outputBytesCap": 4096})
		var result struct {
			ExitCode *int   `json:"exitCode"`
			Stdout   string `json:"stdout"`
			Stderr   string `json:"stderr"`
		}
		if err != nil || json.Unmarshal(response, &result) != nil || result.ExitCode == nil || *result.ExitCode != 0 || result.Stderr != "" {
			return "", ErrCase
		}
		return result.Stdout, nil
	}
	output, err := command([]string{"python3", "-c", workspaceNativeProbe})
	var observed struct {
		Content        string `json:"content"`
		FileMode       int    `json:"fileMode"`
		ExecutableMode int    `json:"executableMode"`
		Link           string `json:"link"`
		LinkedContent  string `json:"linkedContent"`
	}
	if err != nil || json.Unmarshal([]byte(output), &observed) != nil || observed.Content != "00ff0a2a" || observed.LinkedContent != observed.Content || observed.FileMode != 0600 || observed.ExecutableMode != 0700 || observed.Link != "src/data.bin" {
		return ErrCase
	}
	if output, err := command([]string{"/sandbox/workspace/run.sh"}); err != nil || output != "DATAGROUND_SOURCE_EXECUTABLE_OK\n" {
		return ErrCase
	}
	return nil
}
