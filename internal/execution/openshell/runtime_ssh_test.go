package openshell

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"golang.org/x/crypto/ssh"
)

type sshTestProxy struct {
	connection       net.Conn
	diagnostic       *io.PipeReader
	diagnosticWriter *io.PipeWriter
	done             chan struct{}
	serverDone       chan struct{}
	once             sync.Once
}

func (proxy *sshTestProxy) Input() io.WriteCloser { return proxy.connection }
func (proxy *sshTestProxy) Output() io.ReadCloser { return proxy.connection }
func (proxy *sshTestProxy) Errors() io.ReadCloser { return proxy.diagnostic }
func (proxy *sshTestProxy) Wait() error           { <-proxy.done; return nil }
func (proxy *sshTestProxy) Close() error {
	proxy.once.Do(func() {
		_ = proxy.connection.Close()
		_ = proxy.diagnostic.Close()
		_ = proxy.diagnosticWriter.Close()
		close(proxy.done)
	})
	return nil
}

func newSSHTestProxy(t *testing.T, handle func(ssh.Channel)) *sshTestProxy {
	t.Helper()
	local, remote := net.Pipe()
	diagnostic, writer := io.Pipe()
	proxy := &sshTestProxy{connection: local, diagnostic: diagnostic, diagnosticWriter: writer, done: make(chan struct{}), serverDone: make(chan struct{})}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	go func() {
		defer close(proxy.serverDone)
		defer remote.Close()
		server, channels, requests, err := ssh.NewServerConn(remote, config)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		for incoming := range channels {
			if incoming.ChannelType() != "session" {
				_ = incoming.Reject(ssh.UnknownChannelType, "unsupported")
				continue
			}
			channel, requests, err := incoming.Accept()
			if err != nil {
				return
			}
			go func() {
				defer channel.Close()
				for request := range requests {
					if request.Type != "exec" {
						t.Errorf("unexpected SSH request %s", request.Type)
						_ = request.Reply(false, nil)
						continue
					}
					var value struct{ Command string }
					if ssh.Unmarshal(request.Payload, &value) != nil || value.Command != "codex app-server" {
						t.Error("runtime command was not fixed")
						_ = request.Reply(false, nil)
						return
					}
					_ = request.Reply(true, nil)
					handle(channel)
					return
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = proxy.Close()
		select {
		case <-proxy.serverDone:
		case <-time.After(time.Second):
			t.Error("SSH test server leaked")
		}
	})
	return proxy
}

func finishSSHTest(channel ssh.Channel, status uint32) {
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
	_ = channel.Close()
}

func TestSSHRuntimeStreamsBeforeStdinClosesAndSeparatesDiagnostics(t *testing.T) {
	proxy := newSSHTestProxy(t, func(channel ssh.Channel) {
		value, err := bufio.NewReader(channel).ReadString('\n')
		if err != nil {
			return
		}
		_, _ = channel.Write([]byte("reply:" + value))
		_, _ = channel.Stderr().Write([]byte("private diagnostic\n"))
		_, _ = io.Copy(io.Discard, channel)
		finishSSHTest(channel, 0)
	})
	_, cancel := context.WithCancel(context.Background())
	session, err := openSSHRuntime(context.Background(), proxy, cancel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, err := session.Input().Write([]byte("request\n")); err != nil {
		t.Fatal(err)
	}
	output := make([]byte, len("reply:request\n"))
	if _, err := io.ReadFull(session.Output(), output); err != nil {
		t.Fatal(err)
	}
	if string(output) != "reply:request\n" {
		t.Fatal("duplex reply changed")
	}
	diagnostic := make([]byte, len("private diagnostic\n"))
	if _, err := io.ReadFull(session.Errors(), diagnostic); err != nil {
		t.Fatal(err)
	}
	if string(diagnostic) != "private diagnostic\n" {
		t.Fatal("stderr was mixed with stdout")
	}
	var closers sync.WaitGroup
	for range 8 {
		closers.Go(func() {
			if err := session.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		})
	}
	closers.Wait()
	if err := session.Wait(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proxy.done:
	default:
		t.Fatal("proxy survived runtime shutdown")
	}
}

func TestSSHRuntimePreservesBytesUnderBackpressure(t *testing.T) {
	proxy := newSSHTestProxy(t, func(channel ssh.Channel) { _, _ = io.Copy(channel, channel); finishSSHTest(channel, 0) })
	_, cancel := context.WithCancel(context.Background())
	session, err := openSSHRuntime(context.Background(), proxy, cancel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	expected := bytes.Repeat([]byte{0, 1, 10, 13, 27, 127, 255}, 32<<10)
	written := make(chan error, 1)
	go func() { _, err := session.Input().Write(expected); written <- err }()
	observed := make([]byte, len(expected))
	if _, err := io.ReadFull(session.Output(), observed); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(observed, expected) {
		t.Fatal("non-PTY transport changed bytes")
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSSHRuntimeCancellationClosesNativeProcessAndProxy(t *testing.T) {
	proxy := newSSHTestProxy(t, func(channel ssh.Channel) { _, _ = io.Copy(io.Discard, channel); finishSSHTest(channel, 0) })
	ctx, cancel := context.WithCancel(context.Background())
	_, cancelProxy := context.WithCancel(context.Background())
	session, err := openSSHRuntime(ctx, proxy, cancelProxy)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled runtime did not exit")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proxy.done:
	default:
		t.Fatal("proxy survived cancellation")
	}
}

func TestSSHRuntimeRejectsUnconfirmedRemoteExit(t *testing.T) {
	proxy := newSSHTestProxy(t, func(channel ssh.Channel) { _, _ = io.Copy(io.Discard, channel); _ = channel.Close() })
	_, cancel := context.WithCancel(context.Background())
	session, err := openSSHRuntime(context.Background(), proxy, cancel)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); !errors.Is(err, ErrRuntimeShutdown) {
		t.Fatalf("missing exit receipt accepted: %v", err)
	}
	if err := session.Close(); !errors.Is(err, ErrRuntimeShutdown) {
		t.Fatal("uncertain close was not stable")
	}
}

func TestSSHRuntimeSetupCancellationReapsProxy(t *testing.T) {
	local, remote := net.Pipe()
	diagnostic, writer := io.Pipe()
	proxy := &sshTestProxy{connection: local, diagnostic: diagnostic, diagnosticWriter: writer, done: make(chan struct{})}
	defer remote.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, cancelProxy := context.WithCancel(context.Background())
	if session, err := openSSHRuntime(ctx, proxy, cancelProxy); session != nil || !errors.Is(err, ErrRuntimeTransport) {
		t.Fatalf("stalled handshake accepted: %v", err)
	}
	select {
	case <-proxy.done:
	case <-time.After(time.Second):
		t.Fatal("failed setup left proxy running")
	}
}

func TestRuntimeProxyRequiresReadyExactDevelopmentScope(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{{result: CommandResult{Stdout: []byte("[]")}}, {}}}
	provider, _, placement, policy, digest := preparedProvider(t, runner)
	created, err := provider.Create(context.Background(), createRequest(placement, policy, digest))
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.ExecutionRef{IsolationDomainID: created.IsolationDomainID, ID: created.ID}
	before := len(runner.calls)
	if _, err := provider.StartRuntime(context.Background(), ref); !errors.Is(err, ErrRuntimeTransport) {
		t.Fatal("unready runtime was started")
	}
	if _, err := provider.StartRuntime(context.Background(), execution.ExecutionRef{IsolationDomainID: "other", ID: ref.ID}); err == nil {
		t.Fatal("cross-domain runtime was started")
	}
	if len(runner.calls) != before {
		t.Fatal("invalid runtime scope reached the native runner")
	}
	encoded, _ := json.Marshal(created)
	if bytes.Contains(encoded, []byte("ssh")) || bytes.Contains(encoded, []byte("127.0.0.1")) {
		t.Fatal("transport coordinates leaked")
	}
}

func TestRuntimeProxyRechecksTargetAfterVersionCheck(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{{result: CommandResult{Stdout: []byte("[]")}}, {}, {result: CommandResult{Stdout: []byte("openshell 0.0.86\n")}}}}
	provider, _, placement, policy, digest := preparedProvider(t, runner)
	created, err := provider.Create(context.Background(), createRequest(placement, policy, digest))
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.ExecutionRef{IsolationDomainID: created.IsolationDomainID, ID: created.ID}
	if err := provider.store.UpdateExecutionState(context.Background(), ref, "ready"); err != nil {
		t.Fatal(err)
	}
	runner.runHook = func(args []string) {
		if len(args) == 1 && args[0] == "--version" {
			if err := provider.store.UpdateExecutionState(context.Background(), ref, "terminated"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if session, err := provider.StartRuntime(context.Background(), ref); session != nil || !errors.Is(err, ErrRuntimeTransport) {
		t.Fatalf("terminated target accepted: %v", err)
	}
	for _, call := range runner.calls {
		if call.start {
			t.Fatal("stale readiness reached the native proxy")
		}
	}
}

func TestSSHRuntimeProxyDiagnosticOverflowFailsClosed(t *testing.T) {
	proxy := newSSHTestProxy(t, func(channel ssh.Channel) { _, _ = io.Copy(io.Discard, channel) })
	_, cancel := context.WithCancel(context.Background())
	session, err := openSSHRuntime(context.Background(), proxy, cancel)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = proxy.diagnosticWriter.Write(bytes.Repeat([]byte("x"), maxNativeDiagnosticBytes+1)) }()
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case err := <-done:
		_ = session.Close()
		if !errors.Is(err, ErrRuntimeShutdown) {
			t.Fatalf("overflow did not invalidate termination: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("diagnostic overflow left the runtime running")
	}
}

type lateRuntimeRunner struct {
	*scriptedRunner
	cancel context.CancelFunc
	proxy  *reapedRuntimeProxy
}

func (runner lateRuntimeRunner) Start(context.Context, string, ...string) (execution.RuntimeSession, error) {
	runner.cancel()
	return runner.proxy, nil
}

type reapedRuntimeProxy struct {
	inertSession
	closed chan struct{}
	waited chan struct{}
}

func (proxy *reapedRuntimeProxy) Close() error { close(proxy.closed); return nil }
func (proxy *reapedRuntimeProxy) Wait() error {
	<-proxy.closed
	close(proxy.waited)
	return nil
}

func TestRuntimeProxyReturnedAfterCancellationIsReaped(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{{result: CommandResult{Stdout: []byte("[]")}}, {}, {result: CommandResult{Stdout: []byte("openshell 0.0.86\n")}}}}
	provider, _, placement, policy, digest := preparedProvider(t, runner)
	created, err := provider.Create(context.Background(), createRequest(placement, policy, digest))
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.ExecutionRef{IsolationDomainID: created.IsolationDomainID, ID: created.ID}
	if err := provider.store.UpdateExecutionState(context.Background(), ref, "ready"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxy := &reapedRuntimeProxy{closed: make(chan struct{}), waited: make(chan struct{})}
	provider.runner = lateRuntimeRunner{scriptedRunner: runner, cancel: cancel, proxy: proxy}
	if session, err := provider.StartRuntime(ctx, ref); session != nil || !errors.Is(err, ErrRuntimeTransport) {
		t.Fatalf("late process accepted: %v", err)
	}
	select {
	case <-proxy.waited:
	case <-time.After(time.Second):
		t.Fatal("cancelled startup did not reap the proxy")
	}
}
