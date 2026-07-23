// Package artifact owns internal, provider-independent artifact finalization.
package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	InvocationArtifactSchemaV1     = "dataground.invocation-artifact/v1"
	InvocationArtifactStateMachine = 2
	invocationArtifactKeyPrefix    = "invocation-artifacts/v1"
)

var (
	ErrInvocationArtifactMissing        = errors.New("invocation artifact not found")
	ErrInvocationArtifactConflict       = errors.New("invocation artifact conflicts with persisted metadata")
	ErrInvocationArtifactUnavailable    = errors.New("invocation artifact is unavailable")
	ErrInvocationArtifactInvalid        = errors.New("invocation artifact is invalid")
	ErrInvocationArtifactObjectMissing  = errors.New("invocation artifact object not found")
	ErrInvocationArtifactObjectConflict = errors.New("invocation artifact object conflicts with immutable content")

	isolationDomainPattern = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
	invocationPattern      = regexp.MustCompile(`^inv_[0-9a-z]{20,32}$`)
	operationPattern       = regexp.MustCompile(`^op_[0-9a-z]{20,32}$`)
	effectPattern          = regexp.MustCompile(`^eff_[0-9a-z]{20,32}$`)
	artifactPattern        = regexp.MustCompile(`^art_[0-9a-z]{20,32}$`)
	digestPattern          = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Record binds immutable object metadata to one invocation runtime effect.
// ObjectKey is protected routing state and must not cross a public contract.
type Record struct {
	SchemaVersion     string
	IsolationDomainID string
	ID                string
	InvocationID      string
	OperationID       string
	EffectID          string
	Name              string
	Kind              string
	MediaType         string
	SizeBytes         int64
	Digest            string
	Sensitive         bool
	ObjectKey         string `json:"-"`
}

// Binding carries the exact runtime claim authority that a durable catalog
// must verify before making artifact metadata visible.
type Binding struct {
	Record              Record
	ActorID             string
	CorrelationID       string
	LeaseOwner          string
	FencingToken        int64
	StateMachineVersion int
}

type Catalog interface {
	GetInvocationArtifactRecord(context.Context, string, string) (Record, error)
	BindInvocationArtifact(context.Context, Binding) (Record, error)
}

type ObjectReader interface {
	OpenInvocationArtifactObject(context.Context, string) (io.ReadCloser, error)
}

// ObjectWriter conditionally creates immutable content and consumes it before
// returning. Existing different content maps to ErrInvocationArtifactObjectConflict.
type ObjectWriter interface {
	PutInvocationArtifactObjectIfAbsent(context.Context, string, io.Reader, int64, string, string) error
}

// Finalization contains sensitive content and must not be serialized, logged,
// or copied into audit metadata.
type Finalization struct {
	Binding Binding
	Content []byte `json:"-"`
}

// Finalizer writes immutable content, verifies it after every write outcome,
// then binds relational metadata. It deliberately has no delete authority.
type Finalizer struct {
	catalog      Catalog
	reader       ObjectReader
	writer       ObjectWriter
	maximumBytes int64
}

type FinalizerConfig struct {
	MaximumBytes int64
}

func NewFinalizer(
	catalog Catalog,
	reader ObjectReader,
	writer ObjectWriter,
	config FinalizerConfig,
) (*Finalizer, error) {
	if dependencyMissing(catalog) ||
		dependencyMissing(reader) ||
		dependencyMissing(writer) ||
		config.MaximumBytes <= 0 ||
		config.MaximumBytes == int64(^uint64(0)>>1) {
		return nil, errors.New(
			"invocation artifact finalizer dependencies and bounded maximum are required",
		)
	}
	return &Finalizer{
		catalog:      catalog,
		reader:       reader,
		writer:       writer,
		maximumBytes: config.MaximumBytes,
	}, nil
}

func (finalizer *Finalizer) Finalize(
	ctx context.Context,
	finalization Finalization,
) (Record, error) {
	binding, err := NormalizeBinding(finalization.Binding)
	if err != nil {
		return Record{}, err
	}
	if binding.Record.SizeBytes > finalizer.maximumBytes ||
		int64(len(finalization.Content)) > finalizer.maximumBytes {
		return Record{}, ErrInvocationArtifactInvalid
	}
	content := bytes.Clone(finalization.Content)
	if int64(len(content)) != binding.Record.SizeBytes ||
		verifyContent(content, binding.Record.Digest) != nil {
		return Record{}, ErrInvocationArtifactInvalid
	}
	if existing, found, err := finalizer.preflight(ctx, binding.Record, content); err != nil {
		return Record{}, err
	} else if found {
		return existing, nil
	}

	writeErr := finalizer.writer.PutInvocationArtifactObjectIfAbsent(
		ctx,
		binding.Record.ObjectKey,
		bytes.NewReader(content),
		binding.Record.SizeBytes,
		binding.Record.Digest,
		binding.Record.MediaType,
	)
	if ctx.Err() != nil {
		return Record{}, ctx.Err()
	}
	persisted, readErr := readObject(
		ctx,
		finalizer.reader,
		binding.Record,
		finalizer.maximumBytes,
	)
	if readErr != nil {
		if ctx.Err() != nil {
			return Record{}, ctx.Err()
		}
		if errors.Is(readErr, ErrInvocationArtifactConflict) ||
			errors.Is(writeErr, ErrInvocationArtifactObjectConflict) {
			return Record{}, ErrInvocationArtifactConflict
		}
		return Record{}, ErrInvocationArtifactUnavailable
	}
	if !bytes.Equal(persisted, content) {
		return Record{}, ErrInvocationArtifactConflict
	}

	bound, err := finalizer.catalog.BindInvocationArtifact(ctx, binding)
	if err != nil {
		if ctx.Err() != nil {
			return Record{}, ctx.Err()
		}
		if errors.Is(err, ErrInvocationArtifactConflict) {
			return Record{}, err
		}
		return Record{}, ErrInvocationArtifactUnavailable
	}
	bound, err = NormalizeRecord(bound)
	if err != nil || !EqualRecords(bound, binding.Record) {
		return Record{}, ErrInvocationArtifactConflict
	}
	return bound, nil
}

func (finalizer *Finalizer) preflight(
	ctx context.Context,
	record Record,
	content []byte,
) (Record, bool, error) {
	existing, err := finalizer.catalog.GetInvocationArtifactRecord(
		ctx,
		record.IsolationDomainID,
		record.ID,
	)
	if err != nil {
		if ctx.Err() != nil {
			return Record{}, false, ctx.Err()
		}
		if errors.Is(err, ErrInvocationArtifactMissing) {
			return Record{}, false, nil
		}
		return Record{}, false, ErrInvocationArtifactUnavailable
	}
	existing, err = NormalizeRecord(existing)
	if err != nil || !EqualRecords(existing, record) {
		return Record{}, false, ErrInvocationArtifactConflict
	}
	persisted, err := readObject(ctx, finalizer.reader, existing, finalizer.maximumBytes)
	if err != nil {
		return Record{}, false, err
	}
	if !bytes.Equal(persisted, content) {
		return Record{}, false, ErrInvocationArtifactConflict
	}
	return existing, true, nil
}

func NormalizeBinding(binding Binding) (Binding, error) {
	record, err := NormalizeRecord(binding.Record)
	if err != nil {
		return Binding{}, err
	}
	if !validPortableValue(binding.ActorID, 256) ||
		!validPortableValue(binding.CorrelationID, 256) ||
		!validPortableValue(binding.LeaseOwner, 256) ||
		binding.FencingToken <= 0 ||
		binding.StateMachineVersion != InvocationArtifactStateMachine {
		return Binding{}, ErrInvocationArtifactInvalid
	}
	binding.Record = record
	return binding, nil
}

func NormalizeRecord(record Record) (Record, error) {
	if record.SchemaVersion != InvocationArtifactSchemaV1 ||
		!isolationDomainPattern.MatchString(record.IsolationDomainID) ||
		!artifactPattern.MatchString(record.ID) ||
		!invocationPattern.MatchString(record.InvocationID) ||
		!operationPattern.MatchString(record.OperationID) ||
		!effectPattern.MatchString(record.EffectID) ||
		!validPortableValue(record.Name, 255) ||
		!validPortableValue(record.MediaType, 255) ||
		!validKind(record.Kind) ||
		record.SizeBytes < 0 ||
		!digestPattern.MatchString(record.Digest) {
		return Record{}, ErrInvocationArtifactInvalid
	}
	expectedKey := ObjectKey(record)
	if record.ObjectKey != "" && record.ObjectKey != expectedKey {
		return Record{}, ErrInvocationArtifactInvalid
	}
	record.ObjectKey = expectedKey
	return record, nil
}

func ObjectKey(record Record) string {
	return strings.Join([]string{
		invocationArtifactKeyPrefix,
		record.IsolationDomainID,
		record.InvocationID,
		record.ID,
		strings.TrimPrefix(record.Digest, "sha256:"),
	}, "/")
}

func EqualRecords(left, right Record) bool {
	return left == right
}

func readObject(
	ctx context.Context,
	reader ObjectReader,
	record Record,
	maximumBytes int64,
) ([]byte, error) {
	object, err := reader.OpenInvocationArtifactObject(ctx, record.ObjectKey)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrInvocationArtifactUnavailable
	}
	content, readErr := io.ReadAll(io.LimitReader(object, maximumBytes+1))
	closeErr := object.Close()
	if readErr != nil || closeErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrInvocationArtifactUnavailable
	}
	if int64(len(content)) > maximumBytes ||
		int64(len(content)) != record.SizeBytes ||
		verifyContent(content, record.Digest) != nil {
		return nil, ErrInvocationArtifactConflict
	}
	return content, nil
}

func verifyContent(content []byte, digest string) error {
	if !digestPattern.MatchString(digest) {
		return ErrInvocationArtifactInvalid
	}
	actual := sha256.Sum256(content)
	if "sha256:"+hex.EncodeToString(actual[:]) != digest {
		return ErrInvocationArtifactInvalid
	}
	return nil
}

func validKind(kind string) bool {
	switch kind {
	case "file", "structured-output", "event-payload", "log", "other":
		return true
	default:
		return false
	}
}

func validPortableValue(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character == 0 || character == '\n' || character == '\r' {
			return false
		}
	}
	return true
}

func dependencyMissing(value any) bool {
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
