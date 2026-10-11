// Package workspace owns internal source validation and filesystem materialization.
package workspace

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaximumArchiveBytes = 32 << 20
const MaximumSourceBytes = 24 << 20
const MaximumEntries = 4096

var (
	ErrSourceInvalid     = errors.New("workspace source archive is invalid")
	ErrSourceUnavailable = errors.New("workspace source materialization is unavailable")
	ErrSourceExists      = errors.New("workspace destination already exists")
	ErrSourceUnsupported = errors.New("workspace source materialization is unsupported on this platform")
)

type sourceEntry struct {
	name       string
	kind       byte
	executable bool
	content    []byte
	target     string
}

// Snapshot retains validated source bytes privately. It supplies no repository
// provenance or authorization; those must be bound by the control plane.
type Snapshot struct {
	entries map[string]sourceEntry
	names   []string
	receipt Receipt
}

type Receipt struct {
	SchemaVersion string `json:"schemaVersion"`
	ArchiveDigest string `json:"archiveDigest"`
	EntryCount    int    `json:"entryCount"`
	SourceBytes   int64  `json:"sourceBytes"`
}

func (*Snapshot) MarshalJSON() ([]byte, error) { return nil, ErrSourceInvalid }
func (snapshot *Snapshot) Receipt() Receipt    { return snapshot.receipt }

// Inspect verifies the complete archive before any filesystem mutation. Only
// ordinary files, directories and contained symbolic links are representable.
// Ownership, timestamps, extended attributes and archive permissions are not
// installed; materialized files are private and preserve only executable intent.
func Inspect(archive []byte, expectedDigest string) (*Snapshot, error) {
	if len(archive) > MaximumArchiveBytes || len(archive) < 1024 || len(archive)%512 != 0 {
		return nil, ErrSourceInvalid
	}
	owned := bytes.Clone(archive)
	defer clear(owned)
	sum := sha256.Sum256(owned)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if digest != expectedDigest {
		return nil, ErrSourceInvalid
	}
	snapshot := &Snapshot{entries: make(map[string]sourceEntry), receipt: Receipt{SchemaVersion: "dataground.workspace-source-materialization/v1", ArchiveDigest: digest}}
	reader := bytes.NewReader(owned)
	stream := tar.NewReader(reader)
	headers := 0
	for {
		offset := len(owned) - reader.Len()
		header, err := stream.Next()
		if err == io.EOF {
			end := (offset + 511) / 512 * 512
			if len(owned)-end < 1024 || !zeroBytes(owned[end:]) {
				return nil, ErrSourceInvalid
			}
			break
		}
		headers++
		if err != nil || headers > MaximumEntries || len(snapshot.entries) >= MaximumEntries {
			return nil, ErrSourceInvalid
		}
		if header.Typeflag == tar.TypeXGlobalHeader {
			for key, value := range header.PAXRecords {
				if key != "comment" || len(value) > 1024 {
					return nil, ErrSourceInvalid
				}
			}
			continue
		}
		name := header.Name
		if header.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if !validSourcePath(name) || header.Mode < 0 || header.Mode & ^int64(0777) != 0 {
			return nil, ErrSourceInvalid
		}
		if _, exists := snapshot.entries[name]; exists {
			return nil, ErrSourceInvalid
		}
		for key := range header.PAXRecords {
			if strings.HasPrefix(key, "GNU.sparse") {
				return nil, ErrSourceInvalid
			}
		}
		entry := sourceEntry{name: name, kind: header.Typeflag}
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			entry.kind = tar.TypeReg
			if header.Size < 0 || header.Size > MaximumSourceBytes-snapshot.receipt.SourceBytes {
				return nil, ErrSourceInvalid
			}
			entry.content, err = io.ReadAll(io.LimitReader(stream, header.Size+1))
			if err != nil || int64(len(entry.content)) != header.Size {
				return nil, ErrSourceInvalid
			}
			entry.executable = header.Mode&0111 != 0
			snapshot.receipt.SourceBytes += header.Size
		case tar.TypeDir:
			if header.Size != 0 {
				return nil, ErrSourceInvalid
			}
		case tar.TypeSymlink:
			if header.Size != 0 || !validLinkText(header.Linkname) {
				return nil, ErrSourceInvalid
			}
			entry.target = header.Linkname
		default:
			return nil, ErrSourceInvalid
		}
		snapshot.entries[name] = entry
	}
	// Insert implicit parents once, and reject archives that would write through
	// a file or a symbolic link regardless of header order.
	names := make([]string, 0, len(snapshot.entries))
	for name := range snapshot.entries {
		names = append(names, name)
	}
	for _, name := range names {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if entry, exists := snapshot.entries[parent]; exists {
				if entry.kind != tar.TypeDir {
					return nil, ErrSourceInvalid
				}
			} else {
				if len(snapshot.entries) >= MaximumEntries {
					return nil, ErrSourceInvalid
				}
				snapshot.entries[parent] = sourceEntry{name: parent, kind: tar.TypeDir}
			}
		}
	}
	for _, entry := range snapshot.entries {
		if entry.kind == tar.TypeSymlink && !snapshot.containedLink(entry) {
			return nil, ErrSourceInvalid
		}
	}
	for name := range snapshot.entries {
		snapshot.names = append(snapshot.names, name)
	}
	sort.Strings(snapshot.names)
	snapshot.receipt.EntryCount = len(snapshot.names)
	return snapshot, nil
}

func validSourcePath(name string) bool {
	if name == "" || name == "." || len(name) > 4096 || path.IsAbs(name) || path.Clean(name) != name || !validLinkText(name) {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) > 64 {
		return false
	}
	for _, part := range parts {
		if part == ".." || strings.EqualFold(part, ".git") || len(part) > 255 {
			return false
		}
	}
	return true
}
func validLinkText(value string) bool {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.Contains(value, "\\") || path.IsAbs(value) {
		return false
	}
	for _, value := range value {
		if unicode.IsControl(value) {
			return false
		}
	}
	return true
}
func zeroBytes(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}

func (snapshot *Snapshot) containedLink(link sourceEntry) bool {
	// Resolve components in filesystem order. Cleaning a/../ before following
	// a symbolic link can conceal an escape through that link's target.
	pending := append(strings.Split(path.Dir(link.name), "/"), strings.Split(link.target, "/")...)
	resolved := []string{}
	seen := map[string]bool{link.name: true}
	for steps := 0; len(pending) != 0; steps++ {
		if steps >= 4096 || len(seen) > 40 || len(pending) > 4096 {
			return false
		}
		part := pending[0]
		pending = pending[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if len(resolved) == 0 {
				return false
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}
		name := strings.Join(append(append([]string(nil), resolved...), part), "/")
		entry, exists := snapshot.entries[name]
		if !exists {
			return false
		}
		if entry.kind == tar.TypeSymlink {
			if seen[name] {
				return false
			}
			seen[name] = true
			pending = append(strings.Split(entry.target, "/"), pending...)
			continue
		}
		if len(pending) != 0 && entry.kind != tar.TypeDir {
			return false
		}
		resolved = append(resolved, part)
	}
	return true
}
