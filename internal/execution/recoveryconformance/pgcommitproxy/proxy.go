// Package pgcommitproxy provides a bounded loopback-only PostgreSQL wire proxy
// for recovery conformance. It can drop the COMMIT request before PostgreSQL
// receives it, drop the durable completion, or hold the result through primary
// loss after forwarding the request.
package pgcommitproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
)

const maxMessageBytes = 16 << 20

type FaultPoint string

const (
	BeforeCommitDurability  FaultPoint = "before-commit-durability"
	AfterCommitDurability   FaultPoint = "after-commit-durability"
	DuringCommitPrimaryLoss FaultPoint = "during-commit-primary-loss"
)

type Proxy struct {
	listener net.Listener
	target   string
	fault    FaultPoint

	cancel      context.CancelFunc
	dropped     chan struct{}
	forwarded   chan struct{}
	drop        sync.Once
	forwardOnce sync.Once
	wait        sync.WaitGroup
}

func Start(ctx context.Context, target string, fault FaultPoint) (*Proxy, error) {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL commit proxy target")
	}
	address := net.ParseIP(host)
	if address == nil || !address.IsLoopback() {
		return nil, errors.New("PostgreSQL commit proxy target must be loopback")
	}
	if fault != BeforeCommitDurability && fault != AfterCommitDurability && fault != DuringCommitPrimaryLoss {
		return nil, errors.New("invalid PostgreSQL commit proxy fault point")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("listen for PostgreSQL commit proxy")
	}
	proxyContext, cancel := context.WithCancel(ctx)
	proxy := &Proxy{
		listener:  listener,
		target:    target,
		fault:     fault,
		cancel:    cancel,
		dropped:   make(chan struct{}),
		forwarded: make(chan struct{}),
	}
	proxy.wait.Add(1)
	go proxy.accept(proxyContext)
	return proxy, nil
}

// WaitForwarded reports when the one accepted connection has forwarded its
// COMMIT request to PostgreSQL. DuringCommitPrimaryLoss withholds every later
// server response so the caller cannot observe an outcome before primary loss.
func (proxy *Proxy) WaitForwarded(ctx context.Context) error {
	select {
	case <-proxy.forwarded:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (proxy *Proxy) Address() string {
	return proxy.listener.Addr().String()
}

func (proxy *Proxy) Wait(ctx context.Context) error {
	select {
	case <-proxy.dropped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (proxy *Proxy) Close() error {
	proxy.cancel()
	err := proxy.listener.Close()
	proxy.wait.Wait()
	if err != nil && !errors.Is(err, net.ErrClosed) {
		return errors.New("close PostgreSQL commit proxy")
	}
	return nil
}

func (proxy *Proxy) accept(ctx context.Context) {
	defer proxy.wait.Done()
	go func() {
		<-ctx.Done()
		_ = proxy.listener.Close()
	}()
	client, err := proxy.listener.Accept()
	if err != nil {
		return
	}
	// One accepted connection binds the observed COMMIT to the one-connection
	// conformance pool instead of allowing unrelated traffic to satisfy the gate.
	_ = proxy.listener.Close()
	server, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxy.target)
	if err != nil {
		_ = client.Close()
		return
	}
	proxy.wait.Add(1)
	go proxy.forward(ctx, client, server)
}

func (proxy *Proxy) forward(ctx context.Context, client net.Conn, server net.Conn) {
	defer proxy.wait.Done()
	defer client.Close()
	defer server.Close()
	var commitPending atomic.Bool
	var responseWithheld atomic.Bool
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		if err := copyStartupMessage(server, client); err != nil {
			return
		}
		for {
			kind, payload, raw, err := readTypedMessage(client)
			if err != nil {
				return
			}
			commitRequest := isCommitRequest(kind, payload)
			if commitRequest {
				if proxy.fault == BeforeCommitDurability {
					proxy.signalDrop()
					return
				}
				commitPending.Store(true)
			}
			if err := writeAll(server, raw); err != nil {
				return
			}
			if commitRequest && proxy.fault == DuringCommitPrimaryLoss {
				proxy.signalForwarded()
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			kind, payload, raw, err := readTypedMessage(server)
			if err != nil {
				return
			}
			if proxy.fault == AfterCommitDurability && commitPending.Load() &&
				kind == 'C' && bytes.Equal(payload, []byte("COMMIT\x00")) {
				proxy.signalDrop()
				return
			}
			if proxy.fault == DuringCommitPrimaryLoss && commitPending.Load() {
				responseWithheld.Store(true)
			}
			if responseWithheld.Load() {
				continue
			}
			if err := writeAll(client, raw); err != nil {
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
	case <-done:
	}
}

func (proxy *Proxy) signalForwarded() {
	proxy.forwardOnce.Do(func() { close(proxy.forwarded) })
}

func (proxy *Proxy) signalDrop() {
	proxy.drop.Do(func() { close(proxy.dropped) })
}

func copyStartupMessage(destination io.Writer, source io.Reader) error {
	header := make([]byte, 4)
	if _, err := io.ReadFull(source, header); err != nil {
		return err
	}
	length := int(binary.BigEndian.Uint32(header))
	if length < 8 || length > maxMessageBytes {
		return errors.New("invalid PostgreSQL startup message length")
	}
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(source, payload); err != nil {
		return err
	}
	if err := writeAll(destination, header); err != nil {
		return err
	}
	return writeAll(destination, payload)
}

func writeAll(destination io.Writer, content []byte) error {
	for len(content) > 0 {
		written, err := destination.Write(content)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrNoProgress
		}
		content = content[written:]
	}
	return nil
}

func readTypedMessage(source io.Reader) (byte, []byte, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(source, header); err != nil {
		return 0, nil, nil, err
	}
	length := int(binary.BigEndian.Uint32(header[1:]))
	if length < 4 || length > maxMessageBytes {
		return 0, nil, nil, errors.New("invalid PostgreSQL message length")
	}
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(source, payload); err != nil {
		return 0, nil, nil, err
	}
	raw := make([]byte, 0, len(header)+len(payload))
	raw = append(raw, header...)
	raw = append(raw, payload...)
	return header[0], payload, raw, nil
}

func isCommitRequest(kind byte, payload []byte) bool {
	var query []byte
	switch kind {
	case 'Q':
		query = bytes.TrimSuffix(payload, []byte{0})
	case 'P':
		separator := bytes.IndexByte(payload, 0)
		if separator < 0 {
			return false
		}
		query = payload[separator+1:]
		if end := bytes.IndexByte(query, 0); end >= 0 {
			query = query[:end]
		}
	default:
		return false
	}
	normalized := strings.TrimSpace(strings.ToLower(string(query)))
	normalized = strings.TrimSpace(strings.TrimSuffix(normalized, ";"))
	return normalized == "commit"
}
