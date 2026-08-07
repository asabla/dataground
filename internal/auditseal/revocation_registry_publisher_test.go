package auditseal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishRevocationSourceRegistryFileInstallsImmutablePublication(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	input := filepath.Join(directory, "reviewed.json")
	target := filepath.Join(directory, "published.json")
	registry := testRevocationSourceRegistry("archive-revocations.primary")
	if err := os.WriteFile(input, registry, 0o600); err != nil {
		t.Fatal(err)
	}
	publication := RevocationSourceRegistryFilePublication{
		InputPath: input, Path: target, Purpose: RevocationNoticePurposeRecipientProof,
		SourceID: "archive-revocations.primary",
	}
	evidence, err := PublishRevocationSourceRegistryFile(context.Background(), publication)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || evidence.SourceRegistrySHA256 == "" {
		t.Fatalf("publication = %o, %#v", info.Mode().Perm(), evidence)
	}
	before := info.ModTime()
	replayed, err := PublishRevocationSourceRegistryFile(context.Background(), publication)
	if err != nil || replayed != evidence {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
	after, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before) {
		t.Fatal("exact replay rewrote the publication")
	}
}

func TestPublishRevocationSourceRegistryFileRejectsConflictingInstalledContent(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	input := filepath.Join(directory, "reviewed.json")
	target := filepath.Join(directory, "published.json")
	if err := os.WriteFile(input, testRevocationSourceRegistry("archive-revocations.primary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, testRevocationSourceRegistry("archive-revocations.secondary"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := PublishRevocationSourceRegistryFile(context.Background(), RevocationSourceRegistryFilePublication{
		InputPath: input, Path: target, Purpose: RevocationNoticePurposeRecipientProof,
		SourceID: "archive-revocations.primary",
	})
	if !errors.Is(err, ErrRevocationSourceRegistryPublicationConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestPublishRevocationSourceRegistryFileRejectsUnselectedSource(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	input := filepath.Join(directory, "reviewed.json")
	if err := os.WriteFile(input, testRevocationSourceRegistry("archive-revocations.primary"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := PublishRevocationSourceRegistryFile(context.Background(), RevocationSourceRegistryFilePublication{
		InputPath: input, Path: filepath.Join(directory, "published.json"),
		Purpose: RevocationNoticePurposeWorkloadIdentity, SourceID: "archive-revocations.primary",
	})
	if !errors.Is(err, ErrRevocationNoticeAcquisitionInvalid) {
		t.Fatalf("selection error = %v", err)
	}
}

func testRevocationSourceRegistry(sourceID string) []byte {
	return []byte(`{"contract":"dataground.audit-export-revocation-source-registry/v1","sources":[{"id":"` + sourceID + `","purpose":"recipient-proof","noticeUrl":"https://revocations.example.invalid/notice","trustUrl":"https://revocations.example.invalid/trust","noticeAuthentication":{"kind":"bearer-credential-file","credentialFile":"/run/dataground/audit/notice-credential.json"},"trustAuthentication":{"kind":"bearer-credential-file","credentialFile":"/run/dataground/audit/trust-credential.json"}}]}`)
}
