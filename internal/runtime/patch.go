package runtime

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const PatchCompletedEvent = "output.patch.completed"
const PatchArtifactEvent = "output.patch.artifact"
const PatchSchemaV1 = "dataground.runtime-patch/v1"
const MaximumPatchBytes = 1 << 20
const MaximumPatchChanges = 256

// Patch paths are descriptive, workspace-relative names. Consumers must not
// treat a retained patch as permission to read or modify a filesystem.
type PatchChange struct {
	Path     string  `json:"path"`
	Kind     string  `json:"kind"`
	Diff     string  `json:"diff"`
	MovePath *string `json:"movePath"`
}

type CompletedPatch struct {
	SchemaVersion string        `json:"schemaVersion"`
	Status        string        `json:"status"`
	Changes       []PatchChange `json:"changes"`
}

func ValidPatchPath(value string) bool {
	return value != "" && value != "." && len(value) <= 4096 && utf8.ValidString(value) && !path.IsAbs(value) && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../") && !strings.Contains(value, `\`) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func (patch CompletedPatch) Payload() map[string]any {
	changes := make([]any, 0, len(patch.Changes))
	for _, change := range patch.Changes {
		var move any
		if change.MovePath != nil {
			move = *change.MovePath
		}
		changes = append(changes, map[string]any{"path": change.Path, "kind": change.Kind, "diff": change.Diff, "movePath": move})
	}
	return map[string]any{"schemaVersion": patch.SchemaVersion, "status": patch.Status, "changes": changes}
}

func ParseCompletedPatch(payload map[string]any) (CompletedPatch, error) {
	var patch CompletedPatch
	version, versionOK := payload["schemaVersion"].(string)
	status, statusOK := payload["status"].(string)
	changes, changesOK := payload["changes"].([]any)
	if len(payload) != 3 || !versionOK || version != PatchSchemaV1 || !statusOK || !validOutputStatus(status) || !changesOK || len(changes) > MaximumPatchChanges {
		return patch, ErrProtocol
	}
	patch = CompletedPatch{SchemaVersion: version, Status: status, Changes: make([]PatchChange, 0, len(changes))}
	rawBytes := 0
	for _, raw := range changes {
		item, ok := raw.(map[string]any)
		if !ok || len(item) != 4 {
			return CompletedPatch{}, ErrProtocol
		}
		name, nameOK := item["path"].(string)
		kind, kindOK := item["kind"].(string)
		diff, diffOK := item["diff"].(string)
		move, moveOK := item["movePath"]
		if !nameOK || !ValidPatchPath(name) || !kindOK || (kind != "add" && kind != "delete" && kind != "update") || !diffOK || !utf8.ValidString(diff) || len(diff) > MaximumPatchBytes || !moveOK {
			return CompletedPatch{}, ErrProtocol
		}
		change := PatchChange{Path: name, Kind: kind, Diff: diff}
		if move != nil {
			target, ok := move.(string)
			if !ok || kind != "update" || !ValidPatchPath(target) || target == name {
				return CompletedPatch{}, ErrProtocol
			}
			change.MovePath = &target
		}
		rawBytes += len(change.Path) + len(change.Kind) + len(change.Diff)
		if change.MovePath != nil {
			rawBytes += len(*change.MovePath)
		}
		if rawBytes > MaximumPatchBytes {
			return CompletedPatch{}, ErrProtocol
		}
		patch.Changes = append(patch.Changes, change)
	}
	encoded, err := json.Marshal(patch)
	if err != nil || len(encoded) > MaximumPatchBytes {
		return CompletedPatch{}, ErrProtocol
	}
	return patch, nil
}

type PatchArtifact struct {
	ArtifactID  string `json:"artifactId"`
	Digest      string `json:"digest"`
	SizeBytes   int64  `json:"sizeBytes"`
	Status      string `json:"status"`
	ChangeCount int    `json:"changeCount"`
	Preview     string `json:"preview"`
}

func (patch PatchArtifact) Payload() map[string]any {
	return map[string]any{"artifactId": patch.ArtifactID, "digest": patch.Digest, "sizeBytes": patch.SizeBytes, "status": patch.Status, "changeCount": patch.ChangeCount, "preview": patch.Preview}
}
func PatchPreview(count int) string {
	if count == 1 {
		return "Retained patch with 1 file change."
	}
	return fmt.Sprintf("Retained patch with %d file changes.", count)
}
func ParsePatchArtifact(payload map[string]any) (PatchArtifact, error) {
	var patch PatchArtifact
	for _, key := range []string{"artifactId", "digest", "status", "preview"} {
		if _, ok := payload[key].(string); !ok {
			return patch, ErrProtocol
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(payload) != 6 || json.Unmarshal(encoded, &patch) != nil || !runtimeArtifactID.MatchString(patch.ArtifactID) || !runtimeArtifactDigest.MatchString(patch.Digest) || patch.SizeBytes <= 0 || patch.SizeBytes > MaximumPatchBytes || !validOutputStatus(patch.Status) || patch.ChangeCount < 1 || patch.ChangeCount > MaximumPatchChanges || patch.Preview != PatchPreview(patch.ChangeCount) {
		return PatchArtifact{}, ErrProtocol
	}
	return patch, nil
}
