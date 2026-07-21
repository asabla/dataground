package s3conformance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"github.com/asabla/dataground/internal/execution"
)

type observedBackend struct {
	Backend
	writes               int
	lostAcknowledgements int
}

func (backend *observedBackend) PutEnforcementObjectIfAbsent(
	ctx context.Context,
	key string,
	content io.Reader,
	size int64,
	digest string,
) error {
	backend.writes++
	err := backend.Backend.PutEnforcementObjectIfAbsent(ctx, key, content, size, digest)
	if err == nil && backend.lostAcknowledgements > 0 {
		backend.lostAcknowledgements--
		return errors.New("conformance write acknowledgement lost")
	}
	return err
}

type finalizerCatalog struct {
	record       execution.EnforcementBundleRecord
	bindFailures int
	binds        int
}

func (catalog *finalizerCatalog) GetEnforcementBundleRecord(
	ctx context.Context,
	isolationDomainID string,
	bundleID string,
) (execution.EnforcementBundleRecord, error) {
	if err := ctx.Err(); err != nil {
		return execution.EnforcementBundleRecord{}, err
	}
	if catalog.record.SchemaVersion == "" || catalog.record.IsolationDomainID != isolationDomainID ||
		catalog.record.ID != bundleID {
		return execution.EnforcementBundleRecord{}, execution.ErrEnforcementBundleMissing
	}
	return catalog.record, nil
}

func (catalog *finalizerCatalog) BindEnforcementBundle(
	ctx context.Context,
	binding execution.EnforcementBundleBinding,
) (execution.EnforcementBundleRecord, error) {
	if err := ctx.Err(); err != nil {
		return execution.EnforcementBundleRecord{}, err
	}
	normalized, err := execution.NormalizeEnforcementBundleBinding(binding)
	if err != nil {
		return execution.EnforcementBundleRecord{}, err
	}
	catalog.binds++
	if catalog.bindFailures > 0 {
		catalog.bindFailures--
		return execution.EnforcementBundleRecord{}, errors.New("conformance catalog unavailable")
	}
	if catalog.record.SchemaVersion != "" && !execution.EqualEnforcementBundleRecords(catalog.record, normalized.Record) {
		return execution.EnforcementBundleRecord{}, execution.ErrEnforcementBundleConflict
	}
	catalog.record = normalized.Record
	return normalized.Record, nil
}

func verifyFinalizerLostAcknowledgement(ctx context.Context, backend Backend, config Config) error {
	content := conformanceContent("finalizer-lost-ack", 0)
	record := finalizerRecord(config.RunID, "finalizer-lost-ack", content)
	catalog := &finalizerCatalog{}
	observed := &observedBackend{Backend: backend, lostAcknowledgements: 1}
	finalizer, err := execution.NewEnforcementBundleFinalizer(catalog, observed, observed)
	if err != nil {
		return err
	}
	request := finalizerRequest(record, content)
	for range 2 {
		bound, err := finalizer.Finalize(ctx, request)
		if err != nil || !execution.EqualEnforcementBundleRecords(bound, record) {
			return errors.New("finalizer did not recover a lost write acknowledgement")
		}
	}
	if observed.writes != 1 || catalog.binds != 1 {
		return errors.New("finalizer replay repeated a recovered external effect")
	}
	return nil
}

func verifyFinalizerCatalogRetry(ctx context.Context, backend Backend, config Config) error {
	content := conformanceContent("finalizer-catalog-retry", 0)
	record := finalizerRecord(config.RunID, "finalizer-catalog-retry", content)
	catalog := &finalizerCatalog{bindFailures: 1}
	observed := &observedBackend{Backend: backend}
	finalizer, err := execution.NewEnforcementBundleFinalizer(catalog, observed, observed)
	if err != nil {
		return err
	}
	request := finalizerRequest(record, content)
	if _, err := finalizer.Finalize(ctx, request); !errors.Is(err, execution.ErrEnforcementBundleUnavailable) {
		return errors.New("finalizer did not expose a failed catalog binding safely")
	}
	for range 2 {
		bound, err := finalizer.Finalize(ctx, request)
		if err != nil || !execution.EqualEnforcementBundleRecords(bound, record) {
			return errors.New("finalizer did not adopt the retained object after catalog recovery")
		}
	}
	if observed.writes != 2 || catalog.binds != 2 {
		return errors.New("catalog recovery repeated a bound external effect")
	}
	return nil
}

func verifyFinalizerConflict(ctx context.Context, backend Backend, config Config) error {
	wanted := conformanceContent("finalizer-conflict", 0)
	conflicting := conformanceContent("finalizer-conflict", 1)
	record := finalizerRecord(config.RunID, "finalizer-conflict", wanted)
	if err := put(ctx, backend, record.ObjectKey, conflicting); err != nil {
		return err
	}
	catalog := &finalizerCatalog{}
	observed := &observedBackend{Backend: backend}
	finalizer, err := execution.NewEnforcementBundleFinalizer(catalog, observed, observed)
	if err != nil {
		return err
	}
	if _, err := finalizer.Finalize(ctx, finalizerRequest(record, wanted)); !errors.Is(err, execution.ErrEnforcementBundleConflict) {
		return errors.New("finalizer accepted conflicting immutable content")
	}
	persisted, err := read(ctx, backend, record.ObjectKey)
	if err != nil || !bytes.Equal(persisted, conflicting) || observed.writes != 1 || catalog.binds != 0 {
		return errors.New("finalizer conflict changed or bound immutable content")
	}
	return nil
}

func finalizerRequest(
	record execution.EnforcementBundleRecord,
	content []byte,
) execution.EnforcementBundleFinalization {
	return execution.EnforcementBundleFinalization{
		Binding: execution.EnforcementBundleBinding{
			Record:        record,
			ActorID:       "s3-conformance",
			CorrelationID: "s3-conformance-" + record.ID,
		},
		Content: bytes.Clone(content),
	}
}

func finalizerRecord(runID string, caseName string, content []byte) execution.EnforcementBundleRecord {
	scope := sha256.Sum256([]byte(runID + ":" + caseName))
	input := sha256.Sum256([]byte("input:" + runID + ":" + caseName))
	binding := sha256.Sum256([]byte("binding:" + runID + ":" + caseName))
	digest := sha256.Sum256(content)
	record := execution.EnforcementBundleRecord{
		SchemaVersion:     execution.EnforcementBundleSchemaV1,
		IsolationDomainID: "iso_" + hex.EncodeToString(scope[:10]),
		ID:                "conformance-" + hex.EncodeToString(scope[20:]),
		RevisionID:        "rev_" + hex.EncodeToString(scope[10:20]),
		Digest:            "sha256:" + hex.EncodeToString(digest[:]),
		MediaType:         execution.EnforcementBundleMediaType,
		SizeBytes:         int64(len(content)),
		Provenance: execution.EnforcementBundleProvenance{
			Producer:              "rosetta",
			SourceRevision:        hex.EncodeToString(input[:20]),
			CompilerVersion:       "1.0.0-conformance",
			CatalogVersion:        "rosetta/v1",
			TargetContractVersion: "rosetta/openshell-policy-v1",
			Mode:                  "strict",
			InputDigest:           "sha256:" + hex.EncodeToString(input[:]),
			BindingDigest:         "sha256:" + hex.EncodeToString(binding[:]),
		},
	}
	record.ObjectKey = execution.EnforcementBundleObjectKey(record)
	return record
}

var _ Backend = (*observedBackend)(nil)
var _ execution.EnforcementBundleStore = (*finalizerCatalog)(nil)
