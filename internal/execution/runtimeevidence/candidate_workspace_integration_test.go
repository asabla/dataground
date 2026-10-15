package runtimeevidence

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/openshell"
	"github.com/asabla/dataground/internal/workspace"
)

func candidateWorkspaceSource(t *testing.T, changed bool) *workspace.Snapshot {
	t.Helper()
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
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive.Bytes())
	snapshot, err := workspace.Inspect(archive.Bytes(), "sha256:"+hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// Use the real gateway proxy and provider-bound sandbox before any harness is
// started. These synthetic fixtures establish transport and filesystem behavior,
// not source-grant authority or a runtime acceptance statement.
func verifyCandidateWorkspaceTransfer(t *testing.T, ctx context.Context, provider *openshell.Provider, ref execution.ExecutionRef) {
	t.Helper()
	source := candidateWorkspaceSource(t, false)
	wrongScope := ref
	wrongScope.IsolationDomainID = runtimeIsolationDomain(ref.IsolationDomainID + ":other")
	if _, err := provider.MaterializeWorkspace(ctx, wrongScope, source); err == nil {
		t.Fatal("cross-domain source transfer accepted")
	}
	for range 2 {
		if receipt, err := provider.MaterializeWorkspace(ctx, ref, source); err != nil || receipt != source.Receipt() {
			t.Fatal("provider-bound workspace installation or read-back failed")
		}
	}
	if _, err := provider.MaterializeWorkspace(ctx, ref, candidateWorkspaceSource(t, true)); err == nil {
		t.Fatal("different source replaced the installed tree")
	}
	if receipt, err := provider.MaterializeWorkspace(ctx, ref, source); err != nil || receipt != source.Receipt() {
		t.Fatal("rejected source changed the installed tree")
	}
	t.Log("verified source transfer and exact read-back through the provider-bound gateway")
}

func verifyCandidateWorkspaceRuntime(t *testing.T, rpc *candidatePolicyRPC) {
	t.Helper()
	probe := `import json,os,stat
root='/sandbox/workspace'
print(json.dumps({'content':open(root+'/src/data.bin','rb').read().hex(),'fileMode':stat.S_IMODE(os.stat(root+'/src/data.bin').st_mode),'executableMode':stat.S_IMODE(os.stat(root+'/run.sh').st_mode),'link':os.readlink(root+'/data-link'),'linkedContent':open(root+'/data-link','rb').read().hex()}))`
	call := func(command []string) string {
		t.Helper()
		response := rpc.call("command/exec", map[string]any{
			"command": command, "cwd": "/sandbox/workspace",
			"sandboxPolicy": map[string]any{"type": "readOnly", "networkAccess": false},
			"timeoutMs":     10000, "outputBytesCap": 4096,
		})
		var result struct {
			ExitCode int    `json:"exitCode"`
			Stdout   string `json:"stdout"`
		}
		if response.Error != nil || json.Unmarshal(response.Result, &result) != nil || result.ExitCode != 0 {
			t.Fatal("runtime could not use the prepared source under native restrictions")
		}
		return result.Stdout
	}
	var observed struct {
		Content        string `json:"content"`
		FileMode       int    `json:"fileMode"`
		ExecutableMode int    `json:"executableMode"`
		Link           string `json:"link"`
		LinkedContent  string `json:"linkedContent"`
	}
	if json.Unmarshal([]byte(call([]string{"python3", "-c", probe})), &observed) != nil || observed.Content != "00ff0a2a" || observed.LinkedContent != observed.Content || observed.FileMode != 0600 || observed.ExecutableMode != 0700 || observed.Link != "src/data.bin" {
		t.Fatal("runtime source content, permissions, or link differs from preparation")
	}
	if call([]string{"/sandbox/workspace/run.sh"}) != "DATAGROUND_SOURCE_EXECUTABLE_OK\n" {
		t.Fatal("prepared executable did not retain its behavior")
	}
	t.Log("verified binary source, private modes, contained link, and executable through the native runtime")
}
