package auditseal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

func TestPublishRevocationSourceRegistryFileInstallsImmutablePublication(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	input := filepath.Join(directory, "reviewed.json")
	target := filepath.Join(directory, "published.json")
	writeTestRevocationSourceRegistry(t, input, "archive-revocations.primary")
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
	writeTestRevocationSourceRegistry(t, input, "archive-revocations.primary")
	writeTestRevocationSourceRegistry(t, target, "archive-revocations.secondary")
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
	writeTestRevocationSourceRegistry(t, input, "archive-revocations.primary")
	_, err := PublishRevocationSourceRegistryFile(context.Background(), RevocationSourceRegistryFilePublication{
		InputPath: input, Path: filepath.Join(directory, "published.json"),
		Purpose: RevocationNoticePurposeWorkloadIdentity, SourceID: "archive-revocations.primary",
	})
	if !errors.Is(err, ErrRevocationNoticeAcquisitionInvalid) {
		t.Fatalf("selection error = %v", err)
	}
}

func TestPublishRevocationSourceRegistryFileSerializesConcurrentWriters(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	target := filepath.Join(directory, "published.json")
	sourceIDs := []string{"archive-revocations.primary", "archive-revocations.secondary"}
	errorsByWriter := make([]error, len(sourceIDs))
	var wait sync.WaitGroup
	for index, sourceID := range sourceIDs {
		input := filepath.Join(directory, sourceID+".json")
		writeTestRevocationSourceRegistry(t, input, sourceID)
		wait.Add(1)
		go func(index int, sourceID, input string) {
			defer wait.Done()
			_, errorsByWriter[index] = PublishRevocationSourceRegistryFile(
				context.Background(),
				RevocationSourceRegistryFilePublication{
					InputPath: input, Path: target, Purpose: RevocationNoticePurposeRecipientProof,
					SourceID: sourceID,
				},
			)
		}(index, sourceID, input)
	}
	wait.Wait()
	succeeded := 0
	conflicted := 0
	for _, err := range errorsByWriter {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrRevocationSourceRegistryPublicationConflict) {
			conflicted++
		} else {
			t.Fatalf("concurrent publication error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent outcomes = success %d, conflict %d", succeeded, conflicted)
	}
}

func TestPublishRevocationSourceRegistryFileHonorsCancellationWhileWaitingForWriter(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	input := filepath.Join(directory, "reviewed.json")
	target := filepath.Join(directory, "published.json")
	writeTestRevocationSourceRegistry(t, input, "archive-revocations.primary")
	lock, err := os.OpenFile(target+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = PublishRevocationSourceRegistryFile(ctx, RevocationSourceRegistryFilePublication{
		InputPath: input, Path: target, Purpose: RevocationNoticePurposeRecipientProof,
		SourceID: "archive-revocations.primary",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled publication error = %v", err)
	}
}

func writeTestRevocationSourceRegistry(t *testing.T, path, sourceID string) {
	t.Helper()
	directory := filepath.Dir(path)
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCanonicalPrivate(t, path, revocationSourceRegistry{
		Contract: revocationSourceRegistryContract,
		Sources: []revocationSourceProfile{{
			ID: sourceID, Purpose: RevocationNoticePurposeRecipientProof,
			NoticeURL: "https://revocations.example.test/notice",
			TrustURL:  "https://revocations.example.test/trust",
			NoticeAuthentication: revocationSourceAuthentication{
				Kind: "bearer-credential-file", CredentialFile: filepath.Join(directory, "notice-credential.json"),
			},
			TrustAuthentication: revocationSourceAuthentication{
				Kind: "bearer-credential-file", CredentialFile: filepath.Join(directory, "trust-credential.json"),
			},
		}},
	})
}
