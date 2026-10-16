package runtimeevidence

import (
	"encoding/json"
	"strings"
	"testing"
)

func verifyCandidateProcessOwner(t *testing.T, rpc *candidatePolicyRPC) {
	t.Helper()
	response := rpc.call("command/exec", map[string]any{
		"command": []string{"/usr/local/bin/dataground-process-owner-test", "-test.run=^TestNativeProcessOwnerKernelBoundary$", "-test.v", "-test.count=1", "-test.timeout=40s"}, "cwd": "/sandbox/workspace",
		"sandboxPolicy": map[string]any{"type": "workspaceWrite", "writableRoots": []string{"/sandbox/workspace"}, "networkAccess": false, "excludeTmpdirEnvVar": true, "excludeSlashTmp": true},
		"timeoutMs":     45000, "outputBytesCap": 8192,
	})
	var result struct {
		ExitCode *int   `json:"exitCode"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	if response.Error != nil || json.Unmarshal(response.Result, &result) != nil || result.ExitCode == nil || *result.ExitCode != 0 || result.Stderr != "" || !strings.Contains(result.Stdout, "--- PASS: TestNativeProcessOwnerKernelBoundary") || strings.Contains(result.Stdout, "SKIP") {
		t.Fatal("native process ownership failed under strict sandbox policy", result.Stdout, result.Stderr)
	}
	t.Log("verified terminal descendant cleanup and ancestry guards under the strict gateway and nested native policy")
}
