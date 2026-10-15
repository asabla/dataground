package runtimeevidence

import (
	"encoding/json"
	"testing"
)

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
