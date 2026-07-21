package execution

import (
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
	object, err := source.objects.OpenEnforcementObject(ctx, record.ObjectKey)
	if err != nil {
		if ctx.Err() != nil {
			return EnforcementBundle{}, ctx.Err()
		}
		if errors.Is(err, ErrEnforcementObjectMissing) {
			return EnforcementBundle{}, ErrEnforcementBundleUnavailable
		}
		return EnforcementBundle{}, ErrEnforcementBundleUnavailable
	}
	content, readErr := io.ReadAll(io.LimitReader(object, maximumEnforcementPolicyBytes+1))
	closeErr := object.Close()
	if readErr != nil || closeErr != nil {
		if ctx.Err() != nil {
			return EnforcementBundle{}, ctx.Err()
		}
		return EnforcementBundle{}, ErrEnforcementBundleUnavailable
	}
	if int64(len(content)) != record.SizeBytes || len(content) > maximumEnforcementPolicyBytes ||
		VerifyEnforcementPolicy(content, record.Digest) != nil {
		return EnforcementBundle{}, ErrEnforcementBundleMismatch
	}
	return EnforcementBundle{
		IsolationDomainID: record.IsolationDomainID,
		ID:                record.ID,
		RevisionID:        record.RevisionID,
		Digest:            record.Digest,
		Content:           content,
	}, nil
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
