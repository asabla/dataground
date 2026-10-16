package workspace

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
)

const ChangeSetContract = "dataground.workspace-changes/v1"
const MaximumChangeSetBytes = 64 << 20

var ErrChangesInvalid = errors.New("workspace change set is invalid")

// ChangeSet contains sensitive paths and literal binary replacements. Only its
// receipt is safe metadata. It describes the final difference, not write order.
type ChangeSet struct {
	encoded []byte
	receipt ChangeSetReceipt
}

type ChangeSetReceipt struct {
	SchemaVersion       string `json:"schemaVersion"`
	Digest              string `json:"digest"`
	SizeBytes           int64  `json:"sizeBytes"`
	BaseArchiveDigest   string `json:"baseArchiveDigest"`
	ResultArchiveDigest string `json:"resultArchiveDigest"`
	ChangeCount         int    `json:"changeCount"`
	EntryCount          int    `json:"entryCount"`
	SourceBytes         int64  `json:"sourceBytes"`
}

type changeNode struct {
	Kind       string `json:"kind"`
	Executable bool   `json:"executable,omitempty"`
	Content    []byte `json:"content,omitempty"`
	Target     string `json:"target,omitempty"`
}
type fileChange struct {
	Path   string      `json:"path"`
	Before string      `json:"before,omitempty"`
	After  *changeNode `json:"after,omitempty"`
}
type changeDocument struct {
	Contract            string       `json:"contract"`
	BaseArchiveDigest   string       `json:"baseArchiveDigest"`
	ResultArchiveDigest string       `json:"resultArchiveDigest"`
	Changes             []fileChange `json:"changes"`
}

func (*ChangeSet) MarshalJSON() ([]byte, error)      { return nil, ErrChangesInvalid }
func (changes *ChangeSet) Receipt() ChangeSetReceipt { return changes.receipt }

// Compare binds an exact verified base archive to a deterministic final tree.
// Applying the artifact creates a new snapshot; it never edits a live workspace.
func Compare(base, result *Snapshot) (*ChangeSet, error) {
	if base == nil || result == nil || base.entries == nil || result.entries == nil {
		return nil, ErrChangesInvalid
	}
	canonical, err := snapshotFromEntries(result.entries)
	if err != nil {
		return nil, ErrChangesInvalid
	}
	defer canonical.discard()
	document := changeDocument{Contract: ChangeSetContract, BaseArchiveDigest: base.receipt.ArchiveDigest, ResultArchiveDigest: canonical.receipt.ArchiveDigest, Changes: []fileChange{}}
	names := make(map[string]struct{}, len(base.entries)+len(result.entries))
	for name := range base.entries {
		names[name] = struct{}{}
	}
	for name := range result.entries {
		names[name] = struct{}{}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		before, hadBefore := base.entries[name]
		after, hasAfter := result.entries[name]
		if hadBefore && hasAfter && sameSourceEntry(before, after) {
			continue
		}
		change := fileChange{Path: name}
		if hadBefore {
			change.Before = sourceEntryDigest(before)
		}
		if hasAfter {
			node := nodeFromEntry(after)
			change.After = &node
		}
		document.Changes = append(document.Changes, change)
	}
	encoded, err := json.Marshal(document)
	if err != nil || len(encoded) > MaximumChangeSetBytes {
		clear(encoded)
		return nil, ErrChangesInvalid
	}
	return &ChangeSet{encoded: encoded, receipt: changeReceipt(encoded, document, canonical.receipt)}, nil
}

// ApplyChanges verifies the exact artifact bytes, base identity, per-path
// preconditions and result digest. Unknown fields, redundant operations, unsafe
// trees and noncanonical encodings fail before any filesystem mutation.
func ApplyChanges(base *Snapshot, encoded []byte, expectedDigest string) (*Snapshot, error) {
	if base == nil || base.entries == nil || len(encoded) == 0 || len(encoded) > MaximumChangeSetBytes || workspaceDigest(encoded) != expectedDigest {
		return nil, ErrChangesInvalid
	}
	var document changeDocument
	defer func() {
		for _, change := range document.Changes {
			if change.After != nil {
				clear(change.After.Content)
			}
		}
	}()
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || document.Contract != ChangeSetContract || document.BaseArchiveDigest != base.receipt.ArchiveDigest || document.Changes == nil || len(document.Changes) > 2*MaximumEntries {
		return nil, ErrChangesInvalid
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return nil, ErrChangesInvalid
	}
	equal := bytes.Equal(canonical, encoded)
	clear(canonical)
	if !equal {
		return nil, ErrChangesInvalid
	}
	entries := make(map[string]sourceEntry, len(base.entries))
	for name, entry := range base.entries {
		entries[name] = entry
	}
	previous := ""
	var replacementBytes int64
	for _, change := range document.Changes {
		if !validSourcePath(change.Path) || change.Path <= previous {
			return nil, ErrChangesInvalid
		}
		previous = change.Path
		before, exists := base.entries[change.Path]
		if exists && change.Before != sourceEntryDigest(before) || !exists && change.Before != "" || !exists && change.After == nil {
			return nil, ErrChangesInvalid
		}
		if change.After == nil {
			delete(entries, change.Path)
			continue
		}
		after, ok := entryFromNode(change.Path, *change.After)
		if !ok || exists && sameSourceEntry(before, after) {
			return nil, ErrChangesInvalid
		}
		replacementBytes += int64(len(after.content))
		if replacementBytes > MaximumSourceBytes {
			return nil, ErrChangesInvalid
		}
		entries[change.Path] = after
	}
	result, err := snapshotFromEntries(entries)
	if err != nil {
		return nil, ErrChangesInvalid
	}
	if result.receipt.ArchiveDigest != document.ResultArchiveDigest {
		result.discard()
		return nil, ErrChangesInvalid
	}
	return result, nil
}

// InspectChanges retains only an exact canonical artifact whose base,
// preconditions and complete result have been verified. The digest identifies
// bytes; authorization and durable custody remain the caller's responsibility.
func InspectChanges(base *Snapshot, encoded []byte, expectedDigest string) (*ChangeSet, error) {
	result, err := ApplyChanges(base, encoded, expectedDigest)
	if err != nil {
		return nil, err
	}
	defer result.discard()
	changes, err := Compare(base, result)
	if err != nil {
		return nil, err
	}
	if changes.receipt.Digest != expectedDigest {
		clear(changes.encoded)
		return nil, ErrChangesInvalid
	}
	return changes, nil
}

// Write streams sensitive artifact bytes. The caller must close a blocked writer
// on cancellation and confirm complete delivery before accepting the artifact.
func (changes *ChangeSet) Write(ctx context.Context, output io.Writer) error {
	if changes == nil || len(changes.encoded) == 0 || ctx == nil || output == nil {
		return ErrChangesInvalid
	}
	for offset := 0; offset < len(changes.encoded); {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(offset+32*1024, len(changes.encoded))
		n, err := output.Write(changes.encoded[offset:end])
		if err != nil {
			return err
		}
		if n != end-offset {
			return io.ErrShortWrite
		}
		offset = end
	}
	return ctx.Err()
}

func nodeFromEntry(entry sourceEntry) changeNode {
	switch entry.kind {
	case tar.TypeReg:
		return changeNode{Kind: "file", Executable: entry.executable, Content: entry.content}
	case tar.TypeDir:
		return changeNode{Kind: "directory"}
	case tar.TypeSymlink:
		return changeNode{Kind: "symlink", Target: entry.target}
	default:
		return changeNode{}
	}
}
func entryFromNode(name string, node changeNode) (sourceEntry, bool) {
	entry := sourceEntry{name: name}
	switch node.Kind {
	case "file":
		if node.Target != "" {
			return entry, false
		}
		entry.kind = tar.TypeReg
		entry.content = node.Content
		entry.executable = node.Executable
	case "directory":
		if node.Target != "" || node.Executable || len(node.Content) != 0 {
			return entry, false
		}
		entry.kind = tar.TypeDir
	case "symlink":
		if !validLinkText(node.Target) || node.Executable || len(node.Content) != 0 {
			return entry, false
		}
		entry.kind = tar.TypeSymlink
		entry.target = node.Target
	default:
		return entry, false
	}
	return entry, true
}
func sameSourceEntry(left, right sourceEntry) bool {
	return left.kind == right.kind && left.executable == right.executable && left.target == right.target && bytes.Equal(left.content, right.content)
}
func sourceEntryDigest(entry sourceEntry) string {
	hash := sha256.New()
	hash.Write([]byte("dataground.workspace-node/v1\x00"))
	hash.Write([]byte{entry.kind})
	if entry.executable {
		hash.Write([]byte{1})
	} else {
		hash.Write([]byte{0})
	}
	hash.Write([]byte(entry.target))
	hash.Write([]byte{0})
	hash.Write(entry.content)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
func workspaceDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func changeReceipt(encoded []byte, document changeDocument, result Receipt) ChangeSetReceipt {
	return ChangeSetReceipt{SchemaVersion: ChangeSetContract, Digest: workspaceDigest(encoded), SizeBytes: int64(len(encoded)), BaseArchiveDigest: document.BaseArchiveDigest, ResultArchiveDigest: document.ResultArchiveDigest, ChangeCount: len(document.Changes), EntryCount: result.EntryCount, SourceBytes: result.SourceBytes}
}

// Use the source validator for all tree invariants. Explicit parent entries are
// required: a patch cannot silently recreate a directory it claimed to delete.
func snapshotFromEntries(entries map[string]sourceEntry) (*Snapshot, error) {
	if len(entries) > MaximumEntries {
		return nil, ErrChangesInvalid
	}
	names := make([]string, 0, len(entries))
	var size int64
	for name, entry := range entries {
		if !validSourcePath(name) || entry.name != name {
			return nil, ErrChangesInvalid
		}
		size += int64(len(entry.content))
		if size > MaximumSourceBytes {
			return nil, ErrChangesInvalid
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var archive bytes.Buffer
	defer func() { clear(archive.Bytes()) }()
	writer := tar.NewWriter(&archive)
	for _, name := range names {
		entry := entries[name]
		mode := int64(0o600)
		if entry.kind == tar.TypeDir || entry.executable {
			mode = 0o700
		}
		header := &tar.Header{Name: name, Typeflag: entry.kind, Mode: mode, Linkname: entry.target, Size: int64(len(entry.content)), Format: tar.FormatPAX}
		if writer.WriteHeader(header) != nil {
			return nil, ErrChangesInvalid
		}
		if _, err := writer.Write(entry.content); err != nil {
			return nil, ErrChangesInvalid
		}
		if archive.Len() > MaximumArchiveBytes {
			return nil, ErrChangesInvalid
		}
	}
	if writer.Close() != nil || archive.Len() > MaximumArchiveBytes {
		return nil, ErrChangesInvalid
	}
	result, err := Inspect(archive.Bytes(), workspaceDigest(archive.Bytes()))
	if err != nil {
		return nil, err
	}
	if len(result.entries) != len(entries) {
		result.discard()
		return nil, ErrChangesInvalid
	}
	return result, nil
}
