package execution

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
)

const (
	EnforcementBundleSchemaV1   = "dataground.enforcement-bundle/v1"
	EnforcementBundleMediaType  = "application/yaml"
	enforcementBundleKeyPrefix  = "enforcement-bundles/v1"
	enforcementBundleProducerV1 = "rosetta"
)

var (
	ErrEnforcementBundleConflict        = errors.New("enforcement bundle conflicts with persisted metadata")
	ErrEnforcementBundleRevisionMissing = errors.New("enforcement bundle revision not found")
	ErrEnforcementBundleUnavailable     = errors.New("enforcement bundle is unavailable")
	ErrEnforcementObjectMissing         = errors.New("enforcement bundle object not found")
	ErrEnforcementObjectConflict        = errors.New("enforcement bundle object conflicts with immutable content")
	sourceRevisionPattern               = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// EnforcementBundleProvenance records the non-sensitive Rosetta identity and
// hashes needed to bind retrieved bytes back to an approved compilation.
type EnforcementBundleProvenance struct {
	Producer              string
	SourceRevision        string
	CompilerVersion       string
	CatalogVersion        string
	TargetContractVersion string
	Mode                  string
	InputDigest           string
	BindingDigest         string
}

// EnforcementBundleRecord is immutable relational metadata for one object in
// the platform-object storage class. ObjectKey is internal routing state and
// must never be serialized into public resources, events, or audit metadata.
type EnforcementBundleRecord struct {
	SchemaVersion     string
	IsolationDomainID string
	ID                string
	RevisionID        string
	Digest            string
	MediaType         string
	SizeBytes         int64
	ObjectKey         string `json:"-"`
	Provenance        EnforcementBundleProvenance
}

type EnforcementBundleBinding struct {
	Record        EnforcementBundleRecord
	ActorID       string
	CorrelationID string
}

type EnforcementBundleCatalog interface {
	GetEnforcementBundleRecord(context.Context, string, string) (EnforcementBundleRecord, error)
}

type EnforcementBundleStore interface {
	EnforcementBundleCatalog
	BindEnforcementBundle(context.Context, EnforcementBundleBinding) (EnforcementBundleRecord, error)
}

// EnforcementObjectReader is the narrow S3-compatible read seam needed by
// execution admission. Implementations are configured for the platform-object
// storage class and map a missing key to ErrEnforcementObjectMissing.
type EnforcementObjectReader interface {
	OpenEnforcementObject(context.Context, string) (io.ReadCloser, error)
}

// EnforcementObjectWriter creates immutable objects in the platform-object
// storage class. Implementations must use a conditional create, must never
// replace an existing key, and must return only after consuming content. An
// existing key with different bytes maps to ErrEnforcementObjectConflict.
// Authentication, bucket selection, encryption and transport belong to the
// operator-supplied implementation rather than this product contract.
type EnforcementObjectWriter interface {
	PutEnforcementObjectIfAbsent(context.Context, string, io.Reader, int64, string) error
}

type ObjectEnforcementBundleSource struct {
	catalog EnforcementBundleCatalog
	objects EnforcementObjectReader
}

func NewObjectEnforcementBundleSource(
	catalog EnforcementBundleCatalog,
	objects EnforcementObjectReader,
) (*ObjectEnforcementBundleSource, error) {
	if catalog == nil || objects == nil {
		return nil, errors.New("enforcement bundle source dependencies are required")
	}
	return &ObjectEnforcementBundleSource{catalog: catalog, objects: objects}, nil
}

func (source *ObjectEnforcementBundleSource) GetEnforcementBundle(
	ctx context.Context,
	isolationDomainID string,
	bundleID string,
) (EnforcementBundle, error) {
	record, err := source.catalog.GetEnforcementBundleRecord(ctx, isolationDomainID, bundleID)
	if err != nil {
		if ctx.Err() != nil {
			return EnforcementBundle{}, ctx.Err()
		}
		if errors.Is(err, ErrEnforcementBundleMissing) {
			return EnforcementBundle{}, ErrEnforcementBundleMissing
		}
		return EnforcementBundle{}, ErrEnforcementBundleUnavailable
	}
	record, err = NormalizeEnforcementBundleRecord(record)
	if err != nil || record.IsolationDomainID != isolationDomainID || record.ID != bundleID {
		return EnforcementBundle{}, ErrEnforcementBundleMismatch
	}
	content, err := readEnforcementObject(ctx, source.objects, record)
	if err != nil {
		return EnforcementBundle{}, err
	}
	return EnforcementBundle{
		IsolationDomainID: record.IsolationDomainID,
		ID:                record.ID,
		RevisionID:        record.RevisionID,
		Digest:            record.Digest,
		Content:           content,
	}, nil
}

func readEnforcementObject(
	ctx context.Context,
	objects EnforcementObjectReader,
	record EnforcementBundleRecord,
) ([]byte, error) {
	object, err := objects.OpenEnforcementObject(ctx, record.ObjectKey)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrEnforcementBundleUnavailable
	}
	content, readErr := io.ReadAll(io.LimitReader(object, maximumEnforcementPolicyBytes+1))
	closeErr := object.Close()
	if readErr != nil || closeErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrEnforcementBundleUnavailable
	}
	if int64(len(content)) != record.SizeBytes || len(content) > maximumEnforcementPolicyBytes ||
		VerifyEnforcementPolicy(content, record.Digest) != nil {
		return nil, ErrEnforcementBundleMismatch
	}
	return bytes.Clone(content), nil
}

func NormalizeEnforcementBundleBinding(binding EnforcementBundleBinding) (EnforcementBundleBinding, error) {
	record, err := NormalizeEnforcementBundleRecord(binding.Record)
	if err != nil {
		return EnforcementBundleBinding{}, err
	}
	if !validPortableValue(binding.ActorID, 256) {
		return EnforcementBundleBinding{}, errors.New("invalid enforcement bundle actor")
	}
	if !validPortableValue(binding.CorrelationID, 256) {
		return EnforcementBundleBinding{}, errors.New("invalid enforcement bundle correlation identifier")
	}
	binding.Record = record
	return binding, nil
}

func NormalizeEnforcementBundleRecord(record EnforcementBundleRecord) (EnforcementBundleRecord, error) {
	if record.SchemaVersion != EnforcementBundleSchemaV1 {
		return EnforcementBundleRecord{}, errors.New("unsupported enforcement bundle schema version")
	}
	if !isolationDomainPattern.MatchString(record.IsolationDomainID) || !revisionPattern.MatchString(record.RevisionID) {
		return EnforcementBundleRecord{}, errors.New("invalid enforcement bundle scope")
	}
	if !opaqueIdentifierPattern.MatchString(record.ID) || !digestPattern.MatchString(record.Digest) {
		return EnforcementBundleRecord{}, errors.New("invalid enforcement bundle identity")
	}
	if record.MediaType != EnforcementBundleMediaType || record.SizeBytes <= 0 || record.SizeBytes > maximumEnforcementPolicyBytes {
		return EnforcementBundleRecord{}, errors.New("invalid enforcement bundle object metadata")
	}
	provenance := record.Provenance
	if provenance.Producer != enforcementBundleProducerV1 || !sourceRevisionPattern.MatchString(provenance.SourceRevision) ||
		!validPortableValue(provenance.CompilerVersion, 128) || !validPortableValue(provenance.CatalogVersion, 128) ||
		!validPortableValue(provenance.TargetContractVersion, 128) || provenance.Mode != "strict" ||
		!digestPattern.MatchString(provenance.InputDigest) || !digestPattern.MatchString(provenance.BindingDigest) {
		return EnforcementBundleRecord{}, errors.New("invalid enforcement bundle provenance")
	}
	expectedKey := EnforcementBundleObjectKey(record)
	if record.ObjectKey != "" && record.ObjectKey != expectedKey {
		return EnforcementBundleRecord{}, errors.New("invalid enforcement bundle object key")
	}
	record.ObjectKey = expectedKey
	return record, nil
}

func EnforcementBundleObjectKey(record EnforcementBundleRecord) string {
	return strings.Join([]string{
		enforcementBundleKeyPrefix,
		record.IsolationDomainID,
		record.RevisionID,
		record.ID,
		strings.TrimPrefix(record.Digest, "sha256:") + ".yaml",
	}, "/")
}

func EqualEnforcementBundleRecords(left, right EnforcementBundleRecord) bool {
	return left == right
}

var _ EnforcementBundleSource = (*ObjectEnforcementBundleSource)(nil)
