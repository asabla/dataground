package execution

import (
	"bytes"
	"context"
	"errors"
)

// EnforcementBundleFinalization is the internal write-side handoff for a
// validated Rosetta materialization. Content is sensitive enforcement material
// and must never be serialized into resources, events, logs or audit records.
type EnforcementBundleFinalization struct {
	Binding EnforcementBundleBinding
	Content []byte `json:"-"`
}

// EnforcementBundleFinalizer makes immutable enforcement bytes durable before
// binding relational metadata. The deterministic object key and append-only
// catalog make retries safe across lost acknowledgements and process restarts.
// It intentionally has no object-delete authority: a database failure can
// leave an unreachable object that a retry adopts, but cannot remove content
// another successful retry may already have bound.
type EnforcementBundleFinalizer struct {
	catalog EnforcementBundleStore
	reader  EnforcementObjectReader
	writer  EnforcementObjectWriter
}

func NewEnforcementBundleFinalizer(
	catalog EnforcementBundleStore,
	reader EnforcementObjectReader,
	writer EnforcementObjectWriter,
) (*EnforcementBundleFinalizer, error) {
	if catalog == nil || reader == nil || writer == nil {
		return nil, errors.New("enforcement bundle finalizer dependencies are required")
	}
	return &EnforcementBundleFinalizer{catalog: catalog, reader: reader, writer: writer}, nil
}

func (finalizer *EnforcementBundleFinalizer) Finalize(
	ctx context.Context,
	finalization EnforcementBundleFinalization,
) (EnforcementBundleRecord, error) {
	binding, err := NormalizeEnforcementBundleBinding(finalization.Binding)
	if err != nil {
		return EnforcementBundleRecord{}, err
	}
	content := bytes.Clone(finalization.Content)
	if int64(len(content)) != binding.Record.SizeBytes || VerifyEnforcementPolicy(content, binding.Record.Digest) != nil {
		return EnforcementBundleRecord{}, ErrEnforcementBundleMismatch
	}
	if existing, found, err := finalizer.preflight(ctx, binding.Record, content); err != nil {
		return EnforcementBundleRecord{}, err
	} else if found {
		return existing, nil
	}

	writeErr := finalizer.writer.PutEnforcementObjectIfAbsent(
		ctx,
		binding.Record.ObjectKey,
		bytes.NewReader(content),
		binding.Record.SizeBytes,
		binding.Record.Digest,
	)
	if ctx.Err() != nil {
		return EnforcementBundleRecord{}, ctx.Err()
	}

	persisted, readErr := readEnforcementObject(ctx, finalizer.reader, binding.Record)
	if readErr != nil {
		if ctx.Err() != nil {
			return EnforcementBundleRecord{}, ctx.Err()
		}
		if errors.Is(readErr, ErrEnforcementBundleMismatch) || errors.Is(writeErr, ErrEnforcementObjectConflict) {
			return EnforcementBundleRecord{}, ErrEnforcementBundleConflict
		}
		return EnforcementBundleRecord{}, ErrEnforcementBundleUnavailable
	}
	if !bytes.Equal(persisted, content) {
		return EnforcementBundleRecord{}, ErrEnforcementBundleConflict
	}
	// A matching read-back proves an ambiguous or conflicting write response was
	// already durably satisfied by the same immutable object.

	bound, err := finalizer.catalog.BindEnforcementBundle(ctx, binding)
	if err != nil {
		if ctx.Err() != nil {
			return EnforcementBundleRecord{}, ctx.Err()
		}
		if errors.Is(err, ErrEnforcementBundleConflict) || errors.Is(err, ErrEnforcementBundleRevisionMissing) {
			return EnforcementBundleRecord{}, err
		}
		return EnforcementBundleRecord{}, ErrEnforcementBundleUnavailable
	}
	bound, err = NormalizeEnforcementBundleRecord(bound)
	if err != nil || !EqualEnforcementBundleRecords(bound, binding.Record) {
		return EnforcementBundleRecord{}, ErrEnforcementBundleConflict
	}
	return bound, nil
}

func (finalizer *EnforcementBundleFinalizer) preflight(
	ctx context.Context,
	record EnforcementBundleRecord,
	content []byte,
) (EnforcementBundleRecord, bool, error) {
	existing, err := finalizer.catalog.GetEnforcementBundleRecord(ctx, record.IsolationDomainID, record.ID)
	if err != nil {
		if ctx.Err() != nil {
			return EnforcementBundleRecord{}, false, ctx.Err()
		}
		if errors.Is(err, ErrEnforcementBundleMissing) {
			return EnforcementBundleRecord{}, false, nil
		}
		return EnforcementBundleRecord{}, false, ErrEnforcementBundleUnavailable
	}
	existing, err = NormalizeEnforcementBundleRecord(existing)
	if err != nil || !EqualEnforcementBundleRecords(existing, record) {
		return EnforcementBundleRecord{}, false, ErrEnforcementBundleConflict
	}
	persisted, err := readEnforcementObject(ctx, finalizer.reader, existing)
	if err != nil {
		return EnforcementBundleRecord{}, false, err
	}
	if !bytes.Equal(persisted, content) {
		return EnforcementBundleRecord{}, false, ErrEnforcementBundleConflict
	}
	return existing, true, nil
}
