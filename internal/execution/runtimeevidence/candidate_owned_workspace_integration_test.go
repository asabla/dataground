package runtimeevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/openshell"
	"github.com/asabla/dataground/internal/workspace"
)

func verifyOwnedWorkspaceQuiescence(t *testing.T, ctx context.Context, rpc *candidatePolicyRPC, provider *openshell.Provider, ref execution.ExecutionRef, owner interface{ Quiesce(context.Context) error }) {
	t.Helper()
	command := func(script string) {
		t.Helper()
		response := rpc.call("command/exec", map[string]any{
			"command": []string{"python3", "-c", script}, "cwd": "/sandbox/workspace",
			"sandboxPolicy": map[string]any{"type": "workspaceWrite", "writableRoots": []string{"/sandbox/workspace"}, "networkAccess": false, "excludeTmpdirEnvVar": true, "excludeSlashTmp": true},
			"timeoutMs":     10000, "outputBytesCap": 4096,
		})
		var result struct {
			ExitCode *int   `json:"exitCode"`
			Stdout   string `json:"stdout"`
			Stderr   string `json:"stderr"`
		}
		if response.Error != nil || json.Unmarshal(response.Result, &result) != nil || result.ExitCode == nil || *result.ExitCode != 0 || result.Stdout != "" || result.Stderr != "" {
			t.Fatal("detached workspace writer fixture failed")
		}
	}
	command(`import os,time
os.umask(0o077)
child=os.fork()
if child==0:
 os.setsid()
 null=os.open('/dev/null',os.O_RDWR)
 for target in (0,1,2): os.dup2(null,target)
 if null>2: os.close(null)
 fd=os.open('writer.bin',os.O_CREAT|os.O_EXCL|os.O_RDWR,0o600)
 end=time.monotonic()+30
 counter=0
 while time.monotonic()<end:
  os.pwrite(fd,counter.to_bytes(8,'big'),0)
  counter+=1
  time.sleep(0.005)
 os.close(fd)
 os._exit(0)
end=time.monotonic()+2
while not os.path.exists('writer.bin') and time.monotonic()<end: time.sleep(0.005)
assert os.path.exists('writer.bin')`)
	// This second command starts after the original command and its output ended.
	// Its observations prove that a detached descendant is still writing.
	command(`import time
with open('writer.bin','rb') as f: before=f.read()
end=time.monotonic()+2
changed=False
while time.monotonic()<end:
 time.sleep(0.01)
 with open('writer.bin','rb') as f: after=f.read()
 if len(after)==8 and after!=before: changed=True;break
assert changed`)
	if err := owner.Quiesce(ctx); err != nil {
		t.Fatal("native descendant cleanup was not confirmed")
	}
	if err := owner.Quiesce(ctx); err != nil {
		t.Fatal("terminal owner replay failed")
	}
	base, err := workspaceDiagnosticSource(false)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := provider.ExportWorkspaceChanges(ctx, ref, base)
	if err != nil || changes == nil || changes.Receipt().ChangeCount != 5 {
		t.Fatal("terminal workspace export failed", err)
	}
	var first bytes.Buffer
	if err := changes.Write(ctx, &first); err != nil {
		t.Fatal(err)
	}
	defer clear(first.Bytes())
	restored, err := workspace.ApplyChanges(base, first.Bytes(), changes.Receipt().Digest)
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	if _, err := restored.Materialize(ctx, parent); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(filepath.Join(parent, "workspace", "writer.bin"))
	if err != nil || len(value) != 8 {
		t.Fatal("detached writer bytes were not retained")
	}
	time.Sleep(30 * time.Millisecond)
	repeated, err := provider.ExportWorkspaceChanges(ctx, ref, base)
	if err != nil || repeated == nil || repeated.Receipt() != changes.Receipt() {
		t.Fatal("workspace changed after terminal acknowledgement")
	}
	var second bytes.Buffer
	if err := repeated.Write(ctx, &second); err != nil {
		t.Fatal(err)
	}
	defer clear(second.Bytes())
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("terminal artifact changed")
	}
	t.Log("verified a live detached workspace writer, terminal owner acknowledgement, host exit, and exact repeated export after cleanup")
}
