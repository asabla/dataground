package codex

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func (client *Client) handleCompletedPatch(item json.RawMessage) {
	var native struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Changes *[]struct {
			Path string  `json:"path"`
			Diff *string `json:"diff"`
			Kind struct {
				Type     string  `json:"type"`
				MovePath *string `json:"move_path"`
			} `json:"kind"`
		} `json:"changes"`
	}
	reject := func() { client.fail(fmt.Errorf("%w: completed patch is invalid", dgruntime.ErrProtocol)) }
	if !utf8.Valid(item) || json.Unmarshal(item, &native) != nil || native.ID == "" || len(native.ID) > 1024 || native.Changes == nil || len(*native.Changes) > dgruntime.MaximumPatchChanges {
		reject()
		return
	}
	status := native.Status
	if status == "declined" {
		status = "denied"
	}
	patch := dgruntime.CompletedPatch{SchemaVersion: dgruntime.PatchSchemaV1, Status: status}
	client.stateMu.Lock()
	root := client.workingDir
	client.stateMu.Unlock()
	for _, nativeChange := range *native.Changes {
		name, ok := relativePatchPath(root, nativeChange.Path)
		if !ok || nativeChange.Diff == nil {
			reject()
			return
		}
		change := dgruntime.PatchChange{Path: name, Kind: nativeChange.Kind.Type, Diff: *nativeChange.Diff}
		if nativeChange.Kind.MovePath != nil {
			move, ok := relativePatchPath(root, *nativeChange.Kind.MovePath)
			if !ok {
				reject()
				return
			}
			change.MovePath = &move
		}
		patch.Changes = append(patch.Changes, change)
	}
	if _, err := dgruntime.ParseCompletedPatch(patch.Payload()); err != nil {
		reject()
		return
	}
	encoded, _ := json.Marshal(patch)
	digest := sha256.Sum256(encoded)
	if previous, found := client.completedPatches[native.ID]; found {
		if previous != digest {
			reject()
		}
		return
	}
	if len(client.completedPatches) >= 4096 {
		reject()
		return
	}
	client.completedPatches[native.ID] = digest
	client.emit("activity.file.completed", map[string]any{"kind": "change", "status": status, "changeCount": len(patch.Changes)})
	if len(patch.Changes) > 0 {
		client.emit(dgruntime.PatchCompletedEvent, patch.Payload())
	}
}

func relativePatchPath(root, value string) (string, bool) {
	if !validWorkingDirectory(root) {
		return "", false
	}
	if path.IsAbs(value) {
		prefix := strings.TrimSuffix(root, "/") + "/"
		if !strings.HasPrefix(value, prefix) {
			return "", false
		}
		value = strings.TrimPrefix(value, prefix)
	}
	return value, dgruntime.ValidPatchPath(value)
}

func validWorkingDirectory(root string) bool {
	return root != "" && len(root) <= 4096 && utf8.ValidString(root) && path.IsAbs(root) && path.Clean(root) == root && strings.IndexFunc(root, unicode.IsControl) < 0
}
