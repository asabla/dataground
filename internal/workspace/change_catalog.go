package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"regexp"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/identity"
)

const ChangeExportContract = "dataground.invocation-workspace-change-export/v1"

var (
	ErrChangeExportMissing     = errors.New("workspace change export was not found")
	ErrChangeExportConflict    = errors.New("workspace change export conflicts with its retained intent")
	ErrChangeExportUnavailable = errors.New("workspace change export is unavailable")
	changeExecutionPattern     = regexp.MustCompile(`^exe_[0-9a-z]{20,32}$`)
)

// ChangeExportRecord binds safe metadata to one invocation and its prepared
// source. It does not attest workload quiescence or authorize sandbox teardown.
type ChangeExportRecord struct {
	SchemaVersion string           `json:"schemaVersion"`
	Source        SourceRecord     `json:"source"`
	InvocationID  string           `json:"invocationId"`
	OperationID   string           `json:"operationId"`
	EffectID      string           `json:"effectId"`
	ExecutionID   string           `json:"executionId"`
	PlanDigest    string           `json:"planDigest"`
	Changes       ChangeSetReceipt `json:"changes"`
}

type ChangeExportBinding struct {
	Record              ChangeExportRecord
	ActorID             string
	CorrelationID       string
	LeaseOwner          string
	FencingToken        int64
	StateMachineVersion int
}

type ChangeExportEntry struct {
	Record ChangeExportRecord
	Ready  bool
}

// A catalog must preserve one immutable intent before object access and bind
// completion, the public artifact descriptor, audit and outbox atomically.
// Replacement workers may recover that same intent under a fresh current claim.
type ChangeExportCatalog interface {
	PrepareWorkspaceChangeExport(context.Context, ChangeExportBinding) (ChangeExportEntry, error)
	CompleteWorkspaceChangeExport(context.Context, ChangeExportBinding) (ChangeExportEntry, error)
	GetWorkspaceChangeExport(context.Context, string, string) (ChangeExportEntry, error)
}

func (record ChangeExportRecord) ArtifactRecord() artifact.Record {
	result := artifact.Record{
		SchemaVersion:     artifact.InvocationArtifactSchemaV1,
		IsolationDomainID: record.Source.IsolationDomainID,
		ID:                identity.Derived("art", record.Source.IsolationDomainID+":"+record.OperationID+":workspace-changes:v1"),
		InvocationID:      record.InvocationID, OperationID: record.OperationID, EffectID: record.EffectID,
		Name: "Workspace changes", Kind: "file", MediaType: "application/vnd.dataground.workspace-changes+json",
		SizeBytes: record.Changes.SizeBytes, Digest: record.Changes.Digest, Sensitive: true,
	}
	result.ObjectKey = artifact.ObjectKey(result)
	return result
}
func ValidChangeExportRecord(record ChangeExportRecord) bool {
	receipt := record.Changes
	if record.SchemaVersion != ChangeExportContract || !ValidSourceRecord(record.Source) || !changeExecutionPattern.MatchString(record.ExecutionID) || !sourceDigestPattern.MatchString(record.PlanDigest) ||
		receipt.SchemaVersion != ChangeSetContract || receipt.BaseArchiveDigest != record.Source.ArchiveDigest || !sourceDigestPattern.MatchString(receipt.ResultArchiveDigest) || receipt.SizeBytes <= 0 || receipt.SizeBytes > MaximumChangeSetBytes ||
		receipt.ChangeCount < 0 || receipt.ChangeCount > 2*MaximumEntries || receipt.EntryCount < 0 || receipt.EntryCount > MaximumEntries || receipt.SourceBytes < 0 || receipt.SourceBytes > MaximumSourceBytes {
		return false
	}
	_, err := artifact.NormalizeRecord(record.ArtifactRecord())
	return err == nil && record.EffectID == identity.Derived("eff", record.Source.IsolationDomainID+":invocation-execution:"+record.OperationID+":run-invocation")
}
func ValidChangeExportBinding(binding ChangeExportBinding) bool {
	if !ValidChangeExportRecord(binding.Record) || !ValidSourceInstallation(SourceInstallation{Record: binding.Record.Source, ActorID: binding.ActorID, CorrelationID: binding.CorrelationID}) {
		return false
	}
	_, err := artifact.NormalizeBinding(binding.artifactBinding())
	return err == nil
}
func (binding ChangeExportBinding) artifactBinding() artifact.Binding {
	return artifact.Binding{Record: binding.Record.ArtifactRecord(), ActorID: binding.ActorID, CorrelationID: binding.CorrelationID, LeaseOwner: binding.LeaseOwner, FencingToken: binding.FencingToken, StateMachineVersion: binding.StateMachineVersion}
}

// RetainChanges requires an already authorized internal caller and verified
// change bytes. It reserves immutable intent before storage access, observes
// every object write outcome and publishes only after exact read-back. It never
// reads a live workspace, grants export authority or deletes scratch storage.
func RetainChanges(ctx context.Context, catalog ChangeExportCatalog, reader artifact.ObjectReader, writer artifact.ObjectWriter, binding ChangeExportBinding, changes *ChangeSet) (ChangeExportEntry, error) {
	if ctx == nil || missingChangeDependency(catalog) || missingChangeDependency(reader) || missingChangeDependency(writer) || !ValidChangeExportBinding(binding) || changes == nil || changes.Receipt() != binding.Record.Changes {
		return ChangeExportEntry{}, ErrChangeExportConflict
	}
	if err := ctx.Err(); err != nil {
		return ChangeExportEntry{}, err
	}
	entry, err := catalog.PrepareWorkspaceChangeExport(ctx, binding)
	if err != nil {
		return ChangeExportEntry{}, err
	}
	if entry.Record != binding.Record {
		return ChangeExportEntry{}, ErrChangeExportConflict
	}
	var content bytes.Buffer
	if err := changes.Write(ctx, &content); err != nil {
		return ChangeExportEntry{}, err
	}
	defer clear(content.Bytes())
	finalizer, err := artifact.NewFinalizer(changeArtifactCatalog{catalog: catalog, binding: binding}, reader, changeObjectWriter{catalog: catalog, binding: binding, reader: reader, writer: writer}, artifact.FinalizerConfig{MaximumBytes: MaximumChangeSetBytes})
	if err != nil {
		return ChangeExportEntry{}, ErrChangeExportUnavailable
	}
	record, err := finalizer.Finalize(ctx, artifact.Finalization{Binding: binding.artifactBinding(), Content: content.Bytes()})
	if err != nil {
		return ChangeExportEntry{}, err
	}
	if record != binding.Record.ArtifactRecord() {
		return ChangeExportEntry{}, ErrChangeExportConflict
	}
	return ChangeExportEntry{Record: binding.Record, Ready: true}, nil
}

type changeArtifactCatalog struct {
	catalog ChangeExportCatalog
	binding ChangeExportBinding
}

func (adapter changeArtifactCatalog) GetInvocationArtifactRecord(ctx context.Context, scope, id string) (artifact.Record, error) {
	if scope != adapter.binding.Record.Source.IsolationDomainID || id != adapter.binding.Record.ArtifactRecord().ID {
		return artifact.Record{}, ErrChangeExportConflict
	}
	entry, err := adapter.catalog.GetWorkspaceChangeExport(ctx, scope, adapter.binding.Record.OperationID)
	if errors.Is(err, ErrChangeExportMissing) {
		return artifact.Record{}, artifact.ErrInvocationArtifactMissing
	}
	if err != nil {
		return artifact.Record{}, err
	}
	if entry.Record != adapter.binding.Record {
		return artifact.Record{}, ErrChangeExportConflict
	}
	if !entry.Ready {
		return artifact.Record{}, artifact.ErrInvocationArtifactMissing
	}
	return entry.Record.ArtifactRecord(), nil
}
func (adapter changeArtifactCatalog) BindInvocationArtifact(ctx context.Context, binding artifact.Binding) (artifact.Record, error) {
	if binding != adapter.binding.artifactBinding() {
		return artifact.Record{}, ErrChangeExportConflict
	}
	entry, err := adapter.catalog.CompleteWorkspaceChangeExport(ctx, adapter.binding)
	if err != nil {
		return artifact.Record{}, err
	}
	if !entry.Ready || entry.Record != adapter.binding.Record {
		return artifact.Record{}, ErrChangeExportConflict
	}
	return entry.Record.ArtifactRecord(), nil
}

// Recheck the durable claim immediately before a consequential object write.
// A completion that won the race needs only read-back, never another write.
type changeObjectWriter struct {
	catalog ChangeExportCatalog
	binding ChangeExportBinding
	reader  artifact.ObjectReader
	writer  artifact.ObjectWriter
}

func (guard changeObjectWriter) PutInvocationArtifactObjectIfAbsent(ctx context.Context, key string, content io.Reader, size int64, digest, media string) error {
	expected := guard.binding.Record.ArtifactRecord()
	if key != expected.ObjectKey || size != expected.SizeBytes || digest != expected.Digest || media != expected.MediaType {
		return ErrChangeExportConflict
	}
	object, err := guard.reader.OpenInvocationArtifactObject(ctx, key)
	if err == nil {
		data, readErr := io.ReadAll(io.LimitReader(object, size+1))
		defer clear(data)
		closeErr := object.Close()
		if readErr != nil || closeErr != nil {
			return ErrChangeExportUnavailable
		}
		if int64(len(data)) != size || workspaceDigest(data) != digest {
			return artifact.ErrInvocationArtifactObjectConflict
		}
		return nil
	}
	if !errors.Is(err, artifact.ErrInvocationArtifactObjectMissing) {
		return ErrChangeExportUnavailable
	}
	entry, err := guard.catalog.PrepareWorkspaceChangeExport(ctx, guard.binding)
	if err != nil {
		return err
	}
	if entry.Record != guard.binding.Record {
		return ErrChangeExportConflict
	}
	if entry.Ready {
		return nil
	}
	return guard.writer.PutInvocationArtifactObjectIfAbsent(ctx, key, content, size, digest, media)
}

func missingChangeDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
