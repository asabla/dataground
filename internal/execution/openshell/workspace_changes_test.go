package openshell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/workspace"
	"golang.org/x/crypto/ssh"
)

func changeTransferFixture(t *testing.T) (*workspace.Snapshot, []byte, *workspace.ChangeSet, []byte) {
	t.Helper()
	base, archive := transferSourceFixture(t)
	parent := t.TempDir()
	if _, err := base.Materialize(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "workspace/src/file.bin"), []byte("changed\x00\xfe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(parent, "workspace/empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	final, err := workspace.Capture(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := workspace.Compare(base, final)
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := changes.Write(context.Background(), &encoded); err != nil {
		t.Fatal(err)
	}
	return base, archive, changes, encoded.Bytes()
}

func TestWorkspaceChangeExportVerifiesCompleteExchangeAndExactTree(t *testing.T) {
	base, archive, expected, encoded := changeTransferFixture(t)
	for _, mode := range []string{"exact", "repeat", "missing", "truncated", "different-base", "different-result", "unknown-field", "trailing", "diagnostic", "diagnostic-overflow", "failed-exit", "lost-acknowledgement", "output-overflow"} {
		t.Run(mode, func(t *testing.T) {
			proxy := newSSHCommandTestProxy(t, workspaceChangeExportCommand+base.Receipt().ArchiveDigest, func(channel ssh.Channel) {
				received, err := io.ReadAll(channel)
				if err != nil || !bytes.Equal(received, archive) {
					t.Error("base archive changed in transit")
				}
				output := encoded
				switch mode {
				case "missing":
					output = nil
				case "truncated":
					output = encoded[:len(encoded)-1]
				case "different-base":
					output = bytes.Replace(encoded, []byte(base.Receipt().ArchiveDigest), []byte("sha256:"+strings.Repeat("0", 64)), 1)
				case "different-result":
					output = bytes.Replace(encoded, []byte(expected.Receipt().ResultArchiveDigest), []byte("sha256:"+strings.Repeat("0", 64)), 1)
				case "unknown-field":
					output = bytes.Replace(encoded, []byte(`"contract":`), []byte(`"unknown":true,"contract":`), 1)
				case "trailing":
					output = append(bytes.Clone(encoded), '\n')
				case "lost-acknowledgement":
					_ = channel.Close()
					return
				case "output-overflow":
					_, _ = io.CopyN(channel, zeroExportReader{}, workspace.MaximumChangeSetBytes+1)
					finishSSHTest(channel, 0)
					return
				}
				_, _ = channel.Write(output)
				if mode == "diagnostic" {
					_, _ = channel.Stderr().Write([]byte("PRIVATE PATH AND CONTENT"))
				}
				if mode == "diagnostic-overflow" {
					_, _ = io.CopyN(channel.Stderr(), zeroExportReader{}, maxNativeDiagnosticBytes+1)
				}
				status := uint32(0)
				if mode == "failed-exit" {
					status = 1
				}
				finishSSHTest(channel, status)
			})
			provider, ref, runner := sourceTransferProvider(t, proxy)
			result, err := provider.ExportWorkspaceChanges(context.Background(), ref, base)
			if mode == "exact" || mode == "repeat" {
				if err != nil || result == nil || result.Receipt() != expected.Receipt() {
					t.Fatal("verified export failed", err)
				}
				var actual bytes.Buffer
				if err := result.Write(context.Background(), &actual); err != nil || !bytes.Equal(actual.Bytes(), encoded) {
					t.Fatal("export changed exact artifact", err)
				}
				if _, err := workspace.ApplyChanges(base, actual.Bytes(), result.Receipt().Digest); err != nil {
					t.Fatal(err)
				}
			} else if result != nil || !errors.Is(err, ErrWorkspaceChangeExport) {
				t.Fatal("unconfirmed export admitted", err)
			}
			for _, call := range runner.calls {
				for _, arg := range call.args {
					if strings.Contains(arg, "file.bin") || strings.Contains(arg, "changed") || strings.Contains(arg, "PRIVATE") {
						t.Fatal("workspace data entered proxy arguments")
					}
				}
			}
			select {
			case <-proxy.done:
			default:
				t.Fatal("export left proxy active")
			}
		})
	}
}

type zeroExportReader struct{}

func (zeroExportReader) Read(data []byte) (int, error) { clear(data); return len(data), nil }

func TestWorkspaceChangeExportRejectsTargetBeforeProxy(t *testing.T) {
	base, _ := transferSourceFixture(t)
	for _, mode := range []string{"domain", "not-ready", "nil-base", "empty-base", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			base := base
			provider, ref, runner := sourceTransferProvider(t, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "domain":
				ref.IsolationDomainID = "other"
			case "not-ready":
				if err := provider.store.UpdateExecutionState(ctx, ref, "terminated"); err != nil {
					t.Fatal(err)
				}
			case "nil-base":
				base = nil
			case "empty-base":
				base = &workspace.Snapshot{}
			case "cancelled":
				cancel()
			}
			if result, err := provider.ExportWorkspaceChanges(ctx, ref, base); result != nil || !errors.Is(err, ErrWorkspaceChangeExport) {
				t.Fatal("invalid target exported", err)
			}
			for _, call := range runner.calls {
				if call.start {
					t.Fatal("invalid target started proxy")
				}
			}
		})
	}
}

func TestWorkspaceChangeExportCancellationRejectsPartialOutput(t *testing.T) {
	base, _, _, encoded := changeTransferFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	proxy := newSSHCommandTestProxy(t, workspaceChangeExportCommand+base.Receipt().ArchiveDigest, func(channel ssh.Channel) {
		if _, err := io.Copy(io.Discard, channel); err != nil {
			return
		}
		if _, err := channel.Write(encoded[:len(encoded)/2]); err != nil {
			return
		}
		close(started)
		<-release
	})
	provider, ref, _ := sourceTransferProvider(t, proxy)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		result, err := provider.ExportWorkspaceChanges(ctx, ref, base)
		if result != nil {
			t.Error("partial output retained")
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(7 * time.Second):
		t.Fatal("export did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrWorkspaceChangeExport) {
			t.Fatal(err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("export did not stop")
	}
	select {
	case <-proxy.done:
	default:
		t.Fatal("cancelled export leaked proxy")
	}
}
