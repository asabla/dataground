package runtimeevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/openshell"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/workspace"
)

// The fixture owns every workspace writer and waits for each bounded command.
// This proves the real transport under strict policy, not workload quiescence
// for arbitrary harness processes or permission to destroy user scratch data.
func verifyCandidateWorkspaceChanges(t *testing.T, ctx context.Context, rpc *candidatePolicyRPC, provider *openshell.Provider, ref execution.ExecutionRef) {
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
			t.Fatal("controlled workspace writer failed")
		}
	}
	command(`import os
os.umask(0o077)
with open('src/data.bin','wb') as f: f.write(bytes([255,0,42,10]))
with open('new.bin','xb') as f: f.write(bytes([0,128,255]))
os.mkdir('empty',0o700)
os.unlink('run.sh')`)

	base, err := workspaceDiagnosticSource(false)
	if err != nil {
		t.Fatal("base fixture failed")
	}
	parent := t.TempDir()
	if _, err := base.Materialize(ctx, parent); err != nil {
		t.Fatal("local expected source failed")
	}
	for path, data := range map[string][]byte{"src/data.bin": {255, 0, 42, 10}, "new.bin": {0, 128, 255}} {
		if err := os.WriteFile(filepath.Join(parent, "workspace", path), data, 0o600); err != nil {
			t.Fatal("local expected binary failed")
		}
	}
	if err := os.Mkdir(filepath.Join(parent, "workspace", "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(parent, "workspace", "run.sh")); err != nil {
		t.Fatal(err)
	}
	final, err := workspace.Capture(ctx, parent)
	if err != nil {
		t.Fatal("expected final capture failed")
	}
	expected, err := workspace.Compare(base, final)
	if err != nil || expected.Receipt().ChangeCount != 4 {
		t.Fatal("expected final difference failed")
	}
	var expectedBytes bytes.Buffer
	if err := expected.Write(ctx, &expectedBytes); err != nil {
		t.Fatal(err)
	}
	defer clear(expectedBytes.Bytes())

	wrong := ref
	wrong.IsolationDomainID = identity.New("iso")
	if _, err := provider.ExportWorkspaceChanges(ctx, wrong, base); err == nil {
		t.Fatal("cross-domain export accepted")
	}
	verify := func() {
		t.Helper()
		changes, err := provider.ExportWorkspaceChanges(ctx, ref, base)
		if err != nil || changes == nil || changes.Receipt() != expected.Receipt() {
			t.Fatal("strict gateway change export differs from native writes")
		}
		var actual bytes.Buffer
		if err := changes.Write(ctx, &actual); err != nil {
			t.Fatal(err)
		}
		defer clear(actual.Bytes())
		if !bytes.Equal(actual.Bytes(), expectedBytes.Bytes()) {
			t.Fatal("strict gateway changed canonical artifact bytes")
		}
		if restored, err := workspace.ApplyChanges(base, actual.Bytes(), changes.Receipt().Digest); err != nil || restored.Receipt() != final.Receipt() {
			t.Fatal("exported binary changes did not restore the exact final tree")
		}
	}
	verify()
	verify()
	command(`import os; os.symlink('/etc/passwd','outside-link')`)
	if _, err := provider.ExportWorkspaceChanges(ctx, ref, base); err == nil {
		t.Fatal("escaping link exported through strict gateway")
	}
	command(`import os; os.unlink('outside-link')`)
	verify()
	t.Log("verified exact binary change export, repetition, scope denial, and escaping-link denial through the strict gateway")
}
