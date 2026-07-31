package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/asabla/dataground/internal/execution"
)

func TestRuntimeCredentialSourceLoadsAndConsumesExactBundle(t *testing.T) {
	t.Parallel()

	directory := writeRuntimeCredentialBundle(t)
	source, err := NewRuntimeCredentialSource(CredentialSourceConfig{
		Directory: directory,
	})
	if err != nil {
		t.Fatalf("NewRuntimeCredentialSource() error = %v", err)
	}
	port := &runtimeProviderPort{}
	provider, err := NewRuntimeProviderFromCredentialSource(
		context.Background(),
		RuntimeProviderSourceConfig{
			RunID:    testRunID,
			Source:   source,
			Provider: port,
		},
	)
	if err != nil {
		t.Fatalf("NewRuntimeProviderFromCredentialSource() error = %v", err)
	}
	if err := provider.Provision(context.Background()); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	for name, expected := range map[string]string{
		"access":  "access-value",
		"refresh": "refresh-value",
		"account": "account-value",
		"id":      "id-value",
	} {
		if string(port.observed[name]) != expected {
			t.Fatalf("%s credential = %q", name, port.observed[name])
		}
	}
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential directory remains: %v", err)
	}
	if err := source.Cleanup(context.Background()); err != nil {
		t.Fatalf("idempotent Cleanup() error = %v", err)
	}
}

func TestRuntimeCredentialSourceRejectsUnsafeBundles(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string){
		"parent permissions": func(t *testing.T, directory string) {
			t.Helper()
			if err := os.Chmod(filepath.Dir(directory), 0o770); err != nil {
				t.Fatal(err)
			}
		},
		"directory permissions": func(t *testing.T, directory string) {
			t.Helper()
			if err := os.Chmod(directory, 0o750); err != nil {
				t.Fatal(err)
			}
		},
		"extra entry": func(t *testing.T, directory string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(directory, "extra"), []byte("extra"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"file permissions": func(t *testing.T, directory string) {
			t.Helper()
			if err := os.Chmod(filepath.Join(directory, "access_token"), 0o640); err != nil {
				t.Fatal(err)
			}
		},
		"empty file": func(t *testing.T, directory string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(directory, "access_token"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"symlink": func(t *testing.T, directory string) {
			t.Helper()
			if err := os.Remove(filepath.Join(directory, "access_token")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("refresh_token", filepath.Join(directory, "access_token")); err != nil {
				t.Fatal(err)
			}
		},
		"hard link": func(t *testing.T, directory string) {
			t.Helper()
			if err := os.Link(
				filepath.Join(directory, "access_token"),
				filepath.Join(filepath.Dir(directory), "linked-secret"),
			); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			directory := writeRuntimeCredentialBundle(t)
			mutate(t, directory)
			if _, err := NewRuntimeCredentialSource(CredentialSourceConfig{
				Directory: directory,
			}); !errors.Is(err, ErrCredentialSourceConfiguration) {
				t.Fatalf("NewRuntimeCredentialSource() error = %v", err)
			}
		})
	}
}

func TestRuntimeCredentialSourceRejectsSubstitutionWithoutDeletingReplacement(t *testing.T) {
	t.Parallel()

	directory := writeRuntimeCredentialBundle(t)
	source, err := NewRuntimeCredentialSource(CredentialSourceConfig{
		Directory: directory,
	})
	if err != nil {
		t.Fatalf("NewRuntimeCredentialSource() error = %v", err)
	}
	path := filepath.Join(directory, "access_token")
	original := filepath.Join(directory, "original-access-token")
	if err := os.Rename(path, original); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := source.load(context.Background()); !errors.Is(err, ErrCredentialSourceLoad) {
		t.Fatalf("Load() error = %v", err)
	}
	replacement, err := os.ReadFile(path)
	if err != nil || string(replacement) != "replacement" {
		t.Fatalf("replacement = %q, error = %v", replacement, err)
	}
	clear(replacement)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, path); err != nil {
		t.Fatal(err)
	}
	if err := source.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential directory remains: %v", err)
	}
}

func TestRuntimeCredentialSourceOverlapPoisonsLoadButPreservesCleanup(t *testing.T) {
	t.Parallel()

	directory := writeRuntimeCredentialBundle(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	source, err := newRuntimeCredentialSource(
		CredentialSourceConfig{Directory: directory},
		func(ctx context.Context, file *ownedCredentialFile) ([]byte, error) {
			once.Do(func() {
				close(entered)
				<-release
			})
			return readRuntimeCredentialFile(ctx, file)
		},
	)
	if err != nil {
		t.Fatalf("newRuntimeCredentialSource() error = %v", err)
	}
	first := make(chan error, 1)
	go func() {
		credentials, err := source.load(context.Background())
		clearRuntimeProviderCredentials(&credentials)
		first <- err
	}()
	<-entered
	if _, err := source.load(context.Background()); !errors.Is(err, ErrCredentialSourceOrder) {
		t.Fatalf("overlap Load() error = %v", err)
	}
	close(release)
	if err := <-first; !errors.Is(err, ErrCredentialSourceOrder) {
		t.Fatalf("first Load() error = %v", err)
	}
	if err := source.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential directory remains: %v", err)
	}
}

func TestRuntimeCredentialSourceCancellationConsumesBundle(t *testing.T) {
	t.Parallel()

	directory := writeRuntimeCredentialBundle(t)
	source, err := NewRuntimeCredentialSource(CredentialSourceConfig{
		Directory: directory,
	})
	if err != nil {
		t.Fatalf("NewRuntimeCredentialSource() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	credentials, err := source.load(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Load() error = %v", err)
	}
	if credentials != (execution.RuntimeConformanceCredentials{}) {
		t.Fatal("cancelled load returned credentials")
	}
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential directory remains: %v", err)
	}
}

func TestRuntimeCredentialSourceCleanupBeforeLoadConsumesBundle(t *testing.T) {
	t.Parallel()

	directory := writeRuntimeCredentialBundle(t)
	source, err := NewRuntimeCredentialSource(CredentialSourceConfig{
		Directory: directory,
	})
	if err != nil {
		t.Fatalf("NewRuntimeCredentialSource() error = %v", err)
	}
	if err := source.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := source.load(context.Background()); !errors.Is(err, ErrCredentialSourceOrder) {
		t.Fatalf("Load() after Cleanup() error = %v", err)
	}
}


func TestRuntimeCredentialSourceRejectsInvalidProviderBeforeAcquisition(t *testing.T) {
	t.Parallel()

	directory := writeRuntimeCredentialBundle(t)
	source, err := NewRuntimeCredentialSource(CredentialSourceConfig{Directory: directory})
	if err != nil {
		t.Fatalf("NewRuntimeCredentialSource() error = %v", err)
	}
	defer func() {
		if err := source.Cleanup(context.Background()); err != nil {
			t.Errorf("Cleanup() error = %v", err)
		}
	}()
	var typedNil *runtimeProviderPort
	for name, config := range map[string]RuntimeProviderSourceConfig{
		"run": {
			RunID:    "invalid",
			Source:   source,
			Provider: &runtimeProviderPort{},
		},
		"source": {
			RunID:    testRunID,
			Provider: &runtimeProviderPort{},
		},
		"provider": {
			RunID:    testRunID,
			Source:   source,
			Provider: typedNil,
		},
	} {
		name, config := name, config
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewRuntimeProviderFromCredentialSource(
				context.Background(),
				config,
			); !errors.Is(err, ErrCredentialSourceConfiguration) {
				t.Fatalf("NewRuntimeProviderFromCredentialSource() error = %v", err)
			}
		})
	}
	if _, err := os.Lstat(directory); err != nil {
		t.Fatalf("invalid provider consumed credential bundle: %v", err)
	}
}

func TestRuntimeCredentialSourceRejectsInvalidConfigurationAndSerialization(t *testing.T) {
	t.Parallel()

	for name, config := range map[string]CredentialSourceConfig{
		"empty":    {},
		"relative": {Directory: "credentials"},
		"root":     {Directory: string(filepath.Separator)},
	} {
		name, config := name, config
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewRuntimeCredentialSource(config); !errors.Is(
				err,
				ErrCredentialSourceConfiguration,
			) {
				t.Fatalf("NewRuntimeCredentialSource() error = %v", err)
			}
		})
	}

	directory := writeRuntimeCredentialBundle(t)
	config := CredentialSourceConfig{Directory: directory}
	source, err := NewRuntimeCredentialSource(config)
	if err != nil {
		t.Fatalf("NewRuntimeCredentialSource() error = %v", err)
	}
	defer func() {
		if err := source.Cleanup(context.Background()); err != nil {
			t.Errorf("Cleanup() error = %v", err)
		}
	}()
	if _, err := json.Marshal(config); !errors.Is(err, ErrSerialization) {
		t.Fatalf("config MarshalJSON() error = %v", err)
	}
	providerSourceConfig := RuntimeProviderSourceConfig{
		RunID:    testRunID,
		Source:   source,
		Provider: &runtimeProviderPort{},
	}
	if _, err := json.Marshal(providerSourceConfig); !errors.Is(err, ErrSerialization) {
		t.Fatalf("provider source config MarshalJSON() error = %v", err)
	}
	if _, err := json.Marshal(source); !errors.Is(err, ErrSerialization) {
		t.Fatalf("source MarshalJSON() error = %v", err)
	}
}

func writeRuntimeCredentialBundle(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "credentials")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"access_token":  "access-value",
		"refresh_token": "refresh-value",
		"account_id":    "account-value",
		"id_token":      "id-value",
	}
	for _, name := range runtimeCredentialSourceNames {
		if err := os.WriteFile(
			filepath.Join(directory, name),
			[]byte(values[name]),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}
