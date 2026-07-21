package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

type bundleCatalogStub struct {
	record EnforcementBundleRecord
	err    error
}

func (catalog *bundleCatalogStub) GetEnforcementBundleRecord(
	context.Context,
	string,
	string,
) (EnforcementBundleRecord, error) {
	return catalog.record, catalog.err
}

type bundleObjectReaderStub struct {
	content  []byte
	err      error
	closeErr error
	keys     []string
}

func (reader *bundleObjectReaderStub) OpenEnforcementObject(
	_ context.Context,
	key string,
) (io.ReadCloser, error) {
	reader.keys = append(reader.keys, key)
	if reader.err != nil {
		return nil, reader.err
	}
	return &bundleReadCloser{Reader: strings.NewReader(string(reader.content)), closeErr: reader.closeErr}, nil
}

type bundleReadCloser struct {
	io.Reader
	closeErr error
}

func (reader *bundleReadCloser) Close() error { return reader.closeErr }

func TestObjectEnforcementBundleSourceReturnsVerifiedOwnedBytes(t *testing.T) {
	policy := []byte("version: 1\n")
	record := enforcementBundleFixture(policy)
	catalog := &bundleCatalogStub{record: record}
	objects := &bundleObjectReaderStub{content: append([]byte(nil), policy...)}
	source, err := NewObjectEnforcementBundleSource(catalog, objects)
	if err != nil {
		t.Fatalf("new object source: %v", err)
	}
	bundle, err := source.GetEnforcementBundle(context.Background(), record.IsolationDomainID, record.ID)
	if err != nil {
		t.Fatalf("get enforcement bundle: %v", err)
	}
	if bundle.IsolationDomainID != record.IsolationDomainID || bundle.ID != record.ID ||
		bundle.RevisionID != record.RevisionID || bundle.Digest != record.Digest ||
		string(bundle.Content) != string(policy) {
		t.Fatalf("unexpected bundle: %#v", bundle)
	}
	if len(objects.keys) != 1 || objects.keys[0] != EnforcementBundleObjectKey(record) {
		t.Fatalf("object key = %#v", objects.keys)
	}
	objects.content[0] = 'x'
	if bundle.Content[0] == 'x' {
		t.Fatal("returned policy aliases object-reader storage")
	}
}

func TestObjectEnforcementBundleSourceFailsClosed(t *testing.T) {
	policy := []byte("version: 1\n")
	fixture := enforcementBundleFixture(policy)
	tests := map[string]struct {
		catalogRecord EnforcementBundleRecord
		catalogErr    error
		objectContent []byte
		objectErr     error
		closeErr      error
		want          error
	}{
		"metadata missing":    {catalogErr: ErrEnforcementBundleMissing, want: ErrEnforcementBundleMissing},
		"catalog unavailable": {catalogErr: errors.New("database detail"), want: ErrEnforcementBundleUnavailable},
		"object missing": {
			catalogRecord: fixture, objectErr: ErrEnforcementObjectMissing, want: ErrEnforcementBundleUnavailable,
		},
		"object unavailable": {
			catalogRecord: fixture, objectErr: errors.New("upstream detail"), want: ErrEnforcementBundleUnavailable,
		},
		"close failure": {
			catalogRecord: fixture, objectContent: policy, closeErr: errors.New("close detail"),
			want: ErrEnforcementBundleUnavailable,
		},
		"digest mismatch": {
			catalogRecord: fixture, objectContent: []byte("tampered"), want: ErrEnforcementBundleMismatch,
		},
		"size mismatch": {
			catalogRecord: func() EnforcementBundleRecord {
				record := fixture
				record.SizeBytes++
				return record
			}(),
			objectContent: policy, want: ErrEnforcementBundleMismatch,
		},
		"cross-domain record": {
			catalogRecord: func() EnforcementBundleRecord {
				record := fixture
				record.IsolationDomainID = "iso_" + strings.Repeat("b", 20)
				record.ObjectKey = ""
				return record
			}(),
			objectContent: policy, want: ErrEnforcementBundleMismatch,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			catalog := &bundleCatalogStub{record: test.catalogRecord, err: test.catalogErr}
			objects := &bundleObjectReaderStub{
				content: test.objectContent, err: test.objectErr, closeErr: test.closeErr,
			}
			source, err := NewObjectEnforcementBundleSource(catalog, objects)
			if err != nil {
				t.Fatal(err)
			}
			_, err = source.GetEnforcementBundle(context.Background(), fixture.IsolationDomainID, fixture.ID)
			if !errors.Is(err, test.want) {
				t.Fatalf("source error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNormalizeEnforcementBundleRecordRejectsInvalidMetadata(t *testing.T) {
	policy := []byte("version: 1\n")
	tests := map[string]func(*EnforcementBundleRecord){
		"schema":       func(record *EnforcementBundleRecord) { record.SchemaVersion = "v2" },
		"domain":       func(record *EnforcementBundleRecord) { record.IsolationDomainID = "iso_bad" },
		"revision":     func(record *EnforcementBundleRecord) { record.RevisionID = "rev_bad" },
		"identifier":   func(record *EnforcementBundleRecord) { record.ID = "../bad" },
		"digest":       func(record *EnforcementBundleRecord) { record.Digest = "sha256:bad" },
		"media type":   func(record *EnforcementBundleRecord) { record.MediaType = "text/yaml" },
		"empty object": func(record *EnforcementBundleRecord) { record.SizeBytes = 0 },
		"oversized object": func(record *EnforcementBundleRecord) {
			record.SizeBytes = maximumEnforcementPolicyBytes + 1
		},
		"producer":        func(record *EnforcementBundleRecord) { record.Provenance.Producer = "other" },
		"source revision": func(record *EnforcementBundleRecord) { record.Provenance.SourceRevision = "main" },
		"compiler":        func(record *EnforcementBundleRecord) { record.Provenance.CompilerVersion = "" },
		"catalog":         func(record *EnforcementBundleRecord) { record.Provenance.CatalogVersion = "" },
		"target":          func(record *EnforcementBundleRecord) { record.Provenance.TargetContractVersion = "" },
		"mode":            func(record *EnforcementBundleRecord) { record.Provenance.Mode = "permissive" },
		"input digest":    func(record *EnforcementBundleRecord) { record.Provenance.InputDigest = "bad" },
		"binding digest":  func(record *EnforcementBundleRecord) { record.Provenance.BindingDigest = "bad" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			record := enforcementBundleFixture(policy)
			mutate(&record)
			record.ObjectKey = ""
			if _, err := NormalizeEnforcementBundleRecord(record); err == nil {
				t.Fatal("invalid record accepted")
			}
		})
	}
}

func TestNormalizeEnforcementBundleRecordDerivesOnlyAllowedObjectKey(t *testing.T) {
	record := enforcementBundleFixture([]byte("version: 1\n"))
	record.ObjectKey = "../../other-tenant/policy.yaml"
	if _, err := NormalizeEnforcementBundleRecord(record); err == nil {
		t.Fatal("caller-controlled object key accepted")
	}
	record.ObjectKey = ""
	normalized, err := NormalizeEnforcementBundleRecord(record)
	if err != nil {
		t.Fatalf("normalize record: %v", err)
	}
	want := "enforcement-bundles/v1/" + record.IsolationDomainID + "/" + record.RevisionID + "/" +
		record.ID + "/" + strings.TrimPrefix(record.Digest, "sha256:") + ".yaml"
	if normalized.ObjectKey != want {
		t.Fatalf("object key = %q, want %q", normalized.ObjectKey, want)
	}
}

func enforcementBundleFixture(policy []byte) EnforcementBundleRecord {
	digest := sha256.Sum256(policy)
	record := EnforcementBundleRecord{
		SchemaVersion:     EnforcementBundleSchemaV1,
		IsolationDomainID: "iso_" + strings.Repeat("a", 20),
		ID:                "rosetta-" + strings.Repeat("b", 64),
		RevisionID:        "rev_" + strings.Repeat("c", 20),
		Digest:            "sha256:" + hex.EncodeToString(digest[:]),
		MediaType:         EnforcementBundleMediaType,
		SizeBytes:         int64(len(policy)),
		Provenance: EnforcementBundleProvenance{
			Producer: "rosetta", SourceRevision: strings.Repeat("d", 40), CompilerVersion: "1.0.0",
			CatalogVersion: "rosetta/v1", TargetContractVersion: "rosetta/openshell-policy-v1",
			Mode: "strict", InputDigest: "sha256:" + strings.Repeat("e", 64),
			BindingDigest: "sha256:" + strings.Repeat("f", 64),
		},
	}
	record.ObjectKey = EnforcementBundleObjectKey(record)
	return record
}
