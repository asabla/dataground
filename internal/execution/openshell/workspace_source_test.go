package openshell

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/workspace"
	"golang.org/x/crypto/ssh"
)

func transferSourceFixture(t *testing.T) (*workspace.Snapshot, []byte) {
	t.Helper()
	var data bytes.Buffer
	stream := tar.NewWriter(&data)
	content := []byte("source\x00\xff\n")
	if err := stream.WriteHeader(&tar.Header{Name: "src/file.bin", Mode: 0755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data.Bytes())
	source, err := workspace.Inspect(data.Bytes(), "sha256:"+hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	return source, data.Bytes()
}

func sourceTransferProvider(t *testing.T, proxy execution.RuntimeSession) (*Provider, execution.ExecutionRef, *scriptedRunner) {
	t.Helper()
	runner := &scriptedRunner{session: proxy, results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("[]")}}, {result: CommandResult{}}, {result: CommandResult{Stdout: []byte("openshell 0.0.86\n")}},
	}}
	provider, _, placement, policy, digest := preparedProvider(t, runner)
	created, err := provider.Create(context.Background(), createRequest(placement, policy, digest))
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.ExecutionRef{IsolationDomainID: created.IsolationDomainID, ID: created.ID}
	if err := provider.store.UpdateExecutionState(context.Background(), ref, "ready"); err != nil {
		t.Fatal(err)
	}
	return provider, ref, runner
}

func TestWorkspaceSourceTransferRequiresExactReceiptAndSuccessfulExit(t *testing.T) {
	source, archive := transferSourceFixture(t)
	receipt, _ := json.Marshal(source.Receipt())
	for _, test := range []struct {
		name, output, diagnostic string
		status                   uint32
		lost                     bool
		ok                       bool
	}{
		{name: "exact", output: string(receipt) + "\n", ok: true},
		{name: "missing-receipt"},
		{name: "different-source", output: strings.Replace(string(receipt), source.Receipt().ArchiveDigest, "sha256:"+strings.Repeat("a", 64), 1)},
		{name: "different-count", output: strings.Replace(string(receipt), `"entryCount":2`, `"entryCount":3`, 1)},
		{name: "duplicate-field", output: strings.Replace(string(receipt), `{"schemaVersion":`, `{"entryCount":2,"schemaVersion":`, 1)},
		{name: "trailing-object", output: string(receipt) + "{}"},
		{name: "failed-exit", output: string(receipt), status: 1},
		{name: "private-diagnostic", output: string(receipt), diagnostic: "SECRET SOURCE PATH"},
		{name: "excess-output", output: strings.Repeat("PRIVATE", 1000)},
		{name: "excess-diagnostic", diagnostic: strings.Repeat("PRIVATE", 20000)},
		{name: "lost-acknowledgement", lost: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy := newSSHCommandTestProxy(t, workspaceMaterializationCommand+source.Receipt().ArchiveDigest, func(channel ssh.Channel) {
				received, err := io.ReadAll(channel)
				if err != nil || !bytes.Equal(received, archive) {
					t.Error("archive bytes changed in transit")
				}
				if test.lost {
					_ = channel.Close()
					return
				}
				_, _ = channel.Write([]byte(test.output))
				_, _ = channel.Stderr().Write([]byte(test.diagnostic))
				finishSSHTest(channel, test.status)
			})
			provider, ref, runner := sourceTransferProvider(t, proxy)
			got, err := provider.MaterializeWorkspace(context.Background(), ref, source)
			if test.ok {
				if err != nil || got != source.Receipt() {
					t.Fatalf("transfer: %v", err)
				}
			} else if !errors.Is(err, ErrWorkspaceSourceTransfer) || got != (workspace.Receipt{}) {
				t.Fatalf("unconfirmed transfer accepted: %v", err)
			}
			for _, call := range runner.calls {
				for _, arg := range call.args {
					if strings.Contains(arg, "file.bin") || strings.Contains(arg, "source\x00") {
						t.Fatal("source leaked into proxy arguments")
					}
				}
			}
		})
	}
}

func TestWorkspaceSourceTransferRejectsInvalidTargetBeforeProxy(t *testing.T) {
	source, _ := transferSourceFixture(t)
	for _, test := range []string{"domain", "not-ready", "nil-source", "empty-source", "cancelled"} {
		t.Run(test, func(t *testing.T) {
			source := source
			provider, ref, runner := sourceTransferProvider(t, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch test {
			case "domain":
				ref.IsolationDomainID = "other"
			case "not-ready":
				if err := provider.store.UpdateExecutionState(ctx, ref, "terminated"); err != nil {
					t.Fatal(err)
				}
			case "nil-source":
				source = nil
			case "empty-source":
				source = &workspace.Snapshot{}
			case "cancelled":
				cancel()
			}
			if _, err := provider.MaterializeWorkspace(ctx, ref, source); !errors.Is(err, ErrWorkspaceSourceTransfer) {
				t.Fatal("invalid transfer accepted")
			}
			for _, call := range runner.calls {
				if call.start {
					t.Fatal("invalid transfer started a proxy")
				}
			}
		})
	}
}

func TestWorkspaceSourceTransferCancellationClosesRemoteAndProxy(t *testing.T) {
	source, _ := transferSourceFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	proxy := newSSHCommandTestProxy(t, workspaceMaterializationCommand+source.Receipt().ArchiveDigest, func(channel ssh.Channel) {
		close(started)
		<-release
		_, _ = io.Copy(io.Discard, channel)
		finishSSHTest(channel, 0)
	})
	provider, ref, _ := sourceTransferProvider(t, proxy)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := provider.MaterializeWorkspace(ctx, ref, source); done <- err }()
	<-started
	cancel()
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, ErrWorkspaceSourceTransfer) {
			t.Fatal("cancelled transfer accepted")
		}
	case <-time.After(7 * time.Second):
		t.Fatal("transfer did not stop")
	}
	select {
	case <-proxy.done:
	default:
		t.Fatal("proxy remained active")
	}
}

func TestWorkspaceSourceTransferAbortsBlockedInput(t *testing.T) {
	var archive bytes.Buffer
	stream := tar.NewWriter(&archive)
	content := bytes.Repeat([]byte{0xa5}, 4<<20)
	if err := stream.WriteHeader(&tar.Header{Name: "large.bin", Mode: 0600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive.Bytes())
	source, err := workspace.Inspect(archive.Bytes(), "sha256:"+hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	proxy := newSSHCommandTestProxy(t, workspaceMaterializationCommand+source.Receipt().ArchiveDigest, func(channel ssh.Channel) {
		if _, err := io.ReadFull(channel, make([]byte, 32*1024)); err != nil {
			return
		}
		close(started)
		<-release // Deliberately neither read stdin nor acknowledge process exit.
	})
	provider, ref, _ := sourceTransferProvider(t, proxy)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := provider.MaterializeWorkspace(ctx, ref, source); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrWorkspaceSourceTransfer) {
			t.Fatal("blocked transfer reported success")
		}
	case <-time.After(7 * time.Second):
		t.Fatal("blocked input outlived transport shutdown")
	}
	select {
	case <-proxy.done:
	default:
		t.Fatal("blocked transfer leaked proxy")
	}
}
