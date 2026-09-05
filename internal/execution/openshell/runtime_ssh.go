package openshell

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"golang.org/x/crypto/ssh"
)

const (
	runtimeSSHSetupTimeout    = 10 * time.Second
	runtimeSSHCloseTimeout    = 5 * time.Second
	runtimeSSHGatewayEndpoint = "http://127.0.0.1:8080"
)

var (
	ErrRuntimeTransport = errors.New("OpenShell runtime transport failed")
	ErrRuntimeShutdown  = errors.New("OpenShell runtime termination could not be confirmed")
)

// runtimeProxyLink owns a CLI-created, gateway-authorized tunnel. No network
// dialer or user SSH configuration can enter this transport.
type runtimeProxyLink struct {
	proxy       execution.RuntimeSession
	cancel      context.CancelFunc
	local       net.Conn
	relay       net.Conn
	stopOnce    sync.Once
	processDone chan struct{}
}

func newRuntimeProxyLink(proxy execution.RuntimeSession, cancel context.CancelFunc) *runtimeProxyLink {
	local, relay := net.Pipe()
	link := &runtimeProxyLink{proxy: proxy, cancel: cancel, local: local, relay: relay, processDone: make(chan struct{})}
	go func() { _, _ = io.Copy(proxy.Input(), relay); link.stop() }()
	go func() { _, _ = io.Copy(relay, proxy.Output()); link.stop() }()
	// Proxy diagnostics stay private and bounded. The app-server's separate SSH
	// stderr stream remains available to the native adapter.
	go func() { _, _ = io.CopyN(io.Discard, proxy.Errors(), maxNativeDiagnosticBytes+1); link.stop() }()
	go func() { _ = proxy.Wait(); close(link.processDone); link.stop() }()
	return link
}

func (link *runtimeProxyLink) stop() {
	link.stopOnce.Do(func() {
		_ = link.local.Close()
		_ = link.relay.Close()
		_ = link.proxy.Input().Close()
		_ = link.proxy.Output().Close()
		_ = link.proxy.Errors().Close()
		link.cancel()
		_ = link.proxy.Close()
	})
}

type sshRuntimeSession struct {
	client     *ssh.Client
	session    *ssh.Session
	input      io.WriteCloser
	output     io.Reader
	diagnostic io.Reader
	link       *runtimeProxyLink
	remoteDone chan struct{}
	remoteErr  error
	closeOnce  sync.Once
	closeErr   error
	lifecycle  sync.Mutex
	stopParent func() bool
}

func openSSHRuntime(ctx context.Context, proxy execution.RuntimeSession, cancel context.CancelFunc) (execution.RuntimeSession, error) {
	link := newRuntimeProxyLink(proxy, cancel)
	accepted := false
	defer func() {
		if !accepted {
			link.stop()
		}
	}()
	setupCtx, setupCancel := context.WithTimeout(ctx, runtimeSSHSetupTimeout)
	defer setupCancel()
	stopSetup := context.AfterFunc(setupCtx, link.stop)
	defer stopSetup()
	deadline, _ := setupCtx.Deadline()
	_ = link.local.SetDeadline(deadline)
	connection, channels, requests, err := ssh.NewClientConn(link.local, "openshell-owned-proxy", &ssh.ClientConfig{
		User: "sandbox",
		// OpenShell 0.0.86 authorizes the exact sandbox through its authorized SSH
		// session tunnel and does not publish a host-key fingerprint. Peer identity
		// therefore comes from that gateway, as in OpenShell's own SSH client. This
		// callback is confined to the private pipe above; never use it for a direct
		// network connection or broaden the loopback deployment profile.
		HostKeyCallback: func(_ string, remote net.Addr, _ ssh.PublicKey) error {
			if remote != link.local.RemoteAddr() {
				return ErrRuntimeTransport
			}
			return nil
		},
	})
	if err != nil {
		return nil, ErrRuntimeTransport
	}
	client := ssh.NewClient(connection, channels, requests)
	defer func() {
		if !accepted {
			_ = client.Close()
		}
	}()
	session, err := client.NewSession()
	if err != nil {
		return nil, ErrRuntimeTransport
	}
	input, err := session.StdinPipe()
	if err != nil {
		return nil, ErrRuntimeTransport
	}
	output, err := session.StdoutPipe()
	if err != nil {
		return nil, ErrRuntimeTransport
	}
	diagnostic, err := session.StderrPipe()
	if err != nil {
		return nil, ErrRuntimeTransport
	}
	// This command is fixed and never interpolates a prompt, model, profile,
	// credential, path, or client-supplied argument. No PTY is requested.
	if err := session.Start("codex app-server"); err != nil {
		return nil, ErrRuntimeTransport
	}
	if !stopSetup() || setupCtx.Err() != nil {
		return nil, ErrRuntimeTransport
	}
	_ = link.local.SetDeadline(time.Time{})
	runtime := &sshRuntimeSession{client: client, session: session, input: input, output: output, diagnostic: diagnostic, link: link, remoteDone: make(chan struct{})}
	runtime.lifecycle.Lock()
	runtime.stopParent = context.AfterFunc(ctx, func() { _ = runtime.Close() })
	runtime.lifecycle.Unlock()
	go func() {
		if err := session.Wait(); err != nil {
			runtime.remoteErr = ErrRuntimeShutdown
		}
		close(runtime.remoteDone)
		_ = runtime.Close()
	}()
	accepted = true
	return runtime, nil
}

func (session *sshRuntimeSession) Input() io.WriteCloser { return session.input }
func (session *sshRuntimeSession) Output() io.ReadCloser {
	return runtimeStreamReader{Reader: session.output, close: session.Close}
}
func (session *sshRuntimeSession) Errors() io.ReadCloser {
	return runtimeStreamReader{Reader: session.diagnostic, close: session.Close}
}
func (session *sshRuntimeSession) Wait() error { <-session.remoteDone; return session.remoteErr }
func (session *sshRuntimeSession) Close() error {
	session.closeOnce.Do(func() {
		session.lifecycle.Lock()
		if session.stopParent != nil {
			session.stopParent()
		}
		session.lifecycle.Unlock()
		closeCtx, cancel := context.WithTimeout(context.Background(), runtimeSSHCloseTimeout)
		defer cancel()
		abort := context.AfterFunc(closeCtx, session.link.stop)
		defer abort()
		_ = session.input.Close()
		select {
		case <-session.remoteDone:
			session.closeErr = session.remoteErr
		case <-closeCtx.Done():
			session.closeErr = ErrRuntimeShutdown
		}
		_ = session.session.Close()
		_ = session.client.Close()
		session.link.stop()
		select {
		case <-session.link.processDone:
		case <-closeCtx.Done():
			session.closeErr = ErrRuntimeShutdown
		}
	})
	return session.closeErr
}

type runtimeStreamReader struct {
	io.Reader
	close func() error
}

func (reader runtimeStreamReader) Close() error { return reader.close() }

func validRuntimeTarget(ref execution.ExecutionRef, entry execution.ExecutionRecord, gateway execution.GatewayRecord) bool {
	return entry.Execution.State == "ready" && entry.Execution.IsolationDomainID == ref.IsolationDomainID && entry.Execution.ID == ref.ID &&
		entry.OperationID != "" && entry.PlacementID != "" && entry.SandboxName == sandboxName(ref.IsolationDomainID, entry.OperationID) &&
		entry.Execution.ID == derivedID("exe", ref.IsolationDomainID+":"+entry.OperationID) &&
		gateway.Endpoint == runtimeSSHGatewayEndpoint && gateway.Gateway.Driver == "docker" &&
		gateway.Gateway.ID == entry.Execution.GatewayID && gateway.Gateway.IsolationDomainID == ref.IsolationDomainID
}
