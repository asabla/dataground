package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const SourceRecordV1 = "dataground.workspace-source/v1"

var (
	ErrSourceMissing         = errors.New("workspace source was not found")
	ErrSourceConflict        = errors.New("workspace source conflicts with its immutable installation")
	ErrSourceObjectMissing   = errors.New("workspace source object was not found")
	sourceScopePattern       = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
	sourceWorkspacePattern   = regexp.MustCompile(`^wsp_[0-9a-z]{20,32}$`)
	sourceIDPattern          = regexp.MustCompile(`^src_[0-9a-z]{20,32}$`)
	sourceDigestPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	sourceCorrelationPattern = regexp.MustCompile(`^cor_[0-9a-z]{20,32}$`)
)

// SourceRecord binds an archive to one workspace. It carries no repository
// provenance, storage credentials, or authorization to provision a session.
type SourceRecord struct {
	SchemaVersion        string `json:"schemaVersion"`
	IsolationDomainID    string `json:"isolationDomainId"`
	WorkspaceID          string `json:"workspaceId"`
	ID                   string `json:"id"`
	ArchiveDigest        string `json:"archiveDigest"`
	StorageProfileDigest string `json:"storageProfileDigest"`
	ArchiveBytes         int64  `json:"archiveBytes"`
	EntryCount           int    `json:"entryCount"`
	SourceBytes          int64  `json:"sourceBytes"`
}
type SourceInstallation struct {
	Record        SourceRecord
	ActorID       string
	CorrelationID string
}
type SourceCatalogEntry struct {
	Installation SourceInstallation
	Ready        bool
}

// SourceCatalog must serialize immutable identity claims before any external
// write. Completion records are append-only and commit with audit and outbox.
type SourceCatalog interface {
	PrepareWorkspaceSource(context.Context, SourceInstallation) (SourceCatalogEntry, error)
	CompleteWorkspaceSource(context.Context, SourceInstallation) (SourceCatalogEntry, error)
	GetWorkspaceSource(context.Context, string, string, string) (SourceCatalogEntry, error)
}
type SourceObjectReader interface {
	WorkspaceSourceStorageDigest() string
	OpenWorkspaceSource(context.Context, string) (io.ReadCloser, error)
}
type SourceObjects interface {
	SourceObjectReader
	PutWorkspaceSourceIfAbsent(context.Context, string, []byte, string) error
}

func ValidSourceRecord(r SourceRecord) bool {
	return r.SchemaVersion == SourceRecordV1 &&
		sourceScopePattern.MatchString(r.IsolationDomainID) &&
		sourceWorkspacePattern.MatchString(r.WorkspaceID) &&
		sourceIDPattern.MatchString(r.ID) &&
		sourceDigestPattern.MatchString(r.ArchiveDigest) &&
		sourceDigestPattern.MatchString(r.StorageProfileDigest) &&
		r.ArchiveBytes >= 1024 &&
		r.ArchiveBytes <= MaximumArchiveBytes &&
		r.ArchiveBytes%512 == 0 &&
		r.EntryCount >= 0 &&
		r.EntryCount <= MaximumEntries &&
		r.SourceBytes >= 0 &&
		r.SourceBytes <= MaximumSourceBytes
}
func ValidSourceInstallation(v SourceInstallation) bool {
	if !ValidSourceRecord(v.Record) || !sourceCorrelationPattern.MatchString(v.CorrelationID) || v.ActorID == "" || len(v.ActorID) > 256 || strings.TrimSpace(v.ActorID) != v.ActorID || !utf8.ValidString(v.ActorID) {
		return false
	}
	for _, c := range v.ActorID {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// SourceObjectKey is protected routing state, never a public artifact URL.
func SourceObjectKey(r SourceRecord) (string, error) {
	if !ValidSourceRecord(r) {
		return "", ErrSourceInvalid
	}
	return "workspace-sources/v1/" + r.IsolationDomainID + "/" + r.WorkspaceID + "/" + r.ID + "/" + strings.TrimPrefix(r.ArchiveDigest, "sha256:"), nil
}
func sourceMatches(r SourceRecord, snapshot *Snapshot, size int) bool {
	receipt := snapshot.Receipt()
	return r.ArchiveBytes == int64(size) && r.ArchiveDigest == receipt.ArchiveDigest && r.EntryCount == receipt.EntryCount && r.SourceBytes == receipt.SourceBytes
}

// InstallSource requires an already authorized administrative caller. A durable
// intent precedes object access. Every write outcome is observed before binding
// completion; a replay reads immutable content and never overwrites it.
func InstallSource(ctx context.Context, catalog SourceCatalog, objects SourceObjects, installation SourceInstallation, archive []byte) (SourceCatalogEntry, error) {
	if ctx == nil || catalog == nil || objects == nil || !ValidSourceInstallation(installation) || len(archive) > MaximumArchiveBytes {
		return SourceCatalogEntry{}, ErrSourceInvalid
	}
	if err := ctx.Err(); err != nil {
		return SourceCatalogEntry{}, err
	}
	if objects.WorkspaceSourceStorageDigest() != installation.Record.StorageProfileDigest {
		return SourceCatalogEntry{}, ErrSourceConflict
	}
	owned := bytes.Clone(archive)
	defer clear(owned)
	snapshot, err := Inspect(owned, installation.Record.ArchiveDigest)
	if err != nil || !sourceMatches(installation.Record, snapshot, len(owned)) {
		return SourceCatalogEntry{}, ErrSourceInvalid
	}
	entry, err := catalog.PrepareWorkspaceSource(ctx, installation)
	if err != nil {
		return SourceCatalogEntry{}, err
	}
	if entry.Installation != installation {
		return SourceCatalogEntry{}, ErrSourceConflict
	}
	key, _ := SourceObjectKey(installation.Record)
	err = verifySourceObject(ctx, objects, key, installation.Record, owned)
	if errors.Is(err, ErrSourceObjectMissing) && !entry.Ready {
		// The conditional write can commit and lose its acknowledgement. Only the
		// independent read determines whether completion is safe.
		_ = objects.PutWorkspaceSourceIfAbsent(ctx, key, owned, installation.Record.ArchiveDigest)
		err = verifySourceObject(ctx, objects, key, installation.Record, owned)
	}
	if err != nil {
		return SourceCatalogEntry{}, err
	}
	if err := ctx.Err(); err != nil {
		return SourceCatalogEntry{}, err
	}
	completed, err := catalog.CompleteWorkspaceSource(ctx, installation)
	if err != nil {
		return SourceCatalogEntry{}, err
	}
	if completed.Installation != installation || !completed.Ready {
		return SourceCatalogEntry{}, ErrSourceConflict
	}
	return completed, nil
}
func verifySourceObject(ctx context.Context, objects SourceObjects, key string, record SourceRecord, expected []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reader, err := objects.OpenWorkspaceSource(ctx, key)
	if err != nil {
		return err
	}
	if reader == nil {
		return ErrSourceUnavailable
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, record.ArchiveBytes+1))
	closeErr := reader.Close()
	defer clear(data)
	if err := ctx.Err(); err != nil {
		return err
	}
	if readErr != nil || closeErr != nil {
		return ErrSourceUnavailable
	}
	if !bytes.Equal(data, expected) {
		return ErrSourceConflict
	}
	return nil
}
