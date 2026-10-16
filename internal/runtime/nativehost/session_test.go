package nativehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/runtime/processowner"
)

type testTransport struct {
	input        *io.PipeWriter
	output       *io.PipeReader
	diagnostic   *io.PipeReader
	closeStreams func()
	done         chan struct{}
	once         sync.Once
	err          error
}

func (s *testTransport) Input() io.WriteCloser { return s.input }
func (s *testTransport) Output() io.ReadCloser { return s.output }
func (s *testTransport) Errors() io.ReadCloser { return s.diagnostic }
func (s *testTransport) Wait() error           { <-s.done; return s.err }
func (s *testTransport) finish(err error)      { s.once.Do(func() { s.err = err; close(s.done) }) }
func (s *testTransport) Close() error          { s.closeStreams(); s.finish(ErrProtocol); return nil }
func transport(run func(io.ReadCloser, io.WriteCloser, io.WriteCloser) error) *testTransport {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	s := &testTransport{input: inW, output: outR, diagnostic: errR, done: make(chan struct{})}
	s.closeStreams = func() {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
	}
	go func() { err := run(inR, outW, errW); outW.Close(); errW.Close(); inR.Close(); s.finish(err) }()
	return s
}

type fakeOwner struct {
	streams         processowner.Streams
	in              *io.PipeReader
	out, diagnostic *io.PipeWriter
	exited          chan struct{}
	exitOnce        sync.Once
	stopping        atomic.Bool
	stopCount       atomic.Int32
	stopErr         error
	waitErr         error
}

func newFakeOwner() *fakeOwner {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	return &fakeOwner{streams: processowner.Streams{Input: inW, Output: outR, Errors: errR}, in: inR, out: outW, diagnostic: errW, exited: make(chan struct{})}
}
func (o *fakeOwner) Start(string, []string, []string) (processowner.Streams, error) {
	go func() {
		_, _ = io.Copy(o.out, o.in)
		o.out.Close()
		o.diagnostic.Close()
		o.exitOnce.Do(func() { close(o.exited) })
	}()
	return o.streams, nil
}
func (o *fakeOwner) Stop(context.Context) error {
	o.stopCount.Add(1)
	o.stopping.Store(true)
	o.in.Close()
	o.streams.Input.Close()
	o.out.Close()
	o.diagnostic.Close()
	o.exitOnce.Do(func() { close(o.exited) })
	return o.stopErr
}
func (o *fakeOwner) Wait(ctx context.Context) (processowner.Exit, error) {
	select {
	case <-o.exited:
		return processowner.Exit{Success: !o.stopping.Load()}, o.waitErr
	case <-ctx.Done():
		return processowner.Exit{}, ctx.Err()
	}
}
func ownerTransport(owner *fakeOwner) *testTransport {
	return transport(func(in io.ReadCloser, out, diagnostic io.WriteCloser) error {
		return serve(context.Background(), in, out, "/fixed", []string{"/fixed"}, nil, func() (processOwner, error) { return owner, nil })
	})
}

func TestNativeBytesCannotForgeOwnerAcknowledgement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	owner := newFakeOwner()
	raw := ownerTransport(owner)
	defer raw.Close()
	session, err := NewSession(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	var forged bytes.Buffer
	fw := frameWriter{output: &forged}
	_ = fw.write(quiesced, session.nonce[:])
	sent := make(chan error, 1)
	go func() { _, err := session.Input().Write(forged.Bytes()); sent <- err }()
	actual := make([]byte, forged.Len())
	if _, err := io.ReadFull(session.Output(), actual); err != nil || !bytes.Equal(actual, forged.Bytes()) {
		t.Fatal("native bytes changed", err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if session.confirmed.Load() || owner.stopCount.Load() != 0 {
		t.Fatal("native output became a control acknowledgement")
	}
	if _, err := json.Marshal(session); !errors.Is(err, ErrProtocol) {
		t.Fatal("process-local proof serialized")
	}
	if err := session.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
	if !session.confirmed.Load() || owner.stopCount.Load() == 0 {
		t.Fatal("terminal cleanup not confirmed")
	}
	if _, err := session.Input().Write([]byte("late")); !errors.Is(err, ErrProtocol) {
		t.Fatal("terminal input accepted")
	}
	if err := session.Quiesce(ctx); err != nil {
		t.Fatal("terminal replay failed", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeExitDoesNotConfirmDescendantCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	owner := newFakeOwner()
	raw := ownerTransport(owner)
	defer raw.Close()
	session, err := NewSession(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Input().Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Wait(); err != nil {
		t.Fatal(err)
	}
	if session.confirmed.Load() || owner.stopCount.Load() != 0 {
		t.Fatal("native exit supplied terminal proof")
	}
	if err := session.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Wait(); err != nil {
		t.Fatal("native exit replay changed after transport closure", err)
	}
}

func TestQuiesceDrainsAbandonedOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	owner := newFakeOwner()
	raw := ownerTransport(owner)
	defer raw.Close()
	session, err := NewSession(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	// No consumer reads the native output. A terminal request must still drain it.
	if _, err := session.Input().Write(bytes.Repeat([]byte("x"), maximumPayload)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := session.Quiesce(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestFailedCleanupAndNativeWaitRemainUnconfirmed(t *testing.T) {
	for _, mode := range []string{"stop", "wait"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			owner := newFakeOwner()
			if mode == "stop" {
				owner.stopErr = ErrProtocol
			} else {
				owner.waitErr = ErrProtocol
			}
			raw := ownerTransport(owner)
			defer raw.Close()
			session, err := NewSession(ctx, raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := session.Quiesce(ctx); err == nil || session.confirmed.Load() {
				t.Fatal("failed cleanup confirmed")
			}
		})
	}
}

func TestUntrustedOrIncompleteOwnerResponsesFailClosed(t *testing.T) {
	for _, mode := range []string{"wrong-nonce", "premature", "lost-ack", "extra-frame", "host-exit", "diagnostic", "missing-stream", "duplicate-exit", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			raw := transport(func(in io.ReadCloser, out, diagnostic io.WriteCloser) error {
				first, err := readFrame(in)
				if err != nil {
					return err
				}
				defer clear(first.data)
				nonce := append([]byte(nil), first.data[len(version):]...)
				defer clear(nonce)
				writer := frameWriter{output: out}
				if writer.write(ready, nonce) != nil {
					return ErrProtocol
				}
				request, err := readFrame(in)
				if err != nil || request.kind != quiesce {
					return ErrProtocol
				}
				if _, err := readFrame(in); err != io.EOF {
					return ErrProtocol
				}
				if mode == "premature" {
					return writer.write(quiesced, nonce)
				}
				if mode == "oversized" {
					_, err := out.Write([]byte{nativeOutput, 0xff, 0xff, 0xff, 0xff})
					return err
				}
				if writer.write(outputEnd, nil) != nil {
					return ErrProtocol
				}
				if mode != "missing-stream" && writer.write(diagnosticEnd, nil) != nil {
					return ErrProtocol
				}
				if writer.write(nativeExit, []byte{0}) != nil {
					return ErrProtocol
				}
				if mode == "duplicate-exit" {
					_ = writer.write(nativeExit, []byte{0})
				}
				if mode == "lost-ack" {
					return nil
				}
				if mode == "wrong-nonce" {
					nonce[0] ^= 1
				}
				if writer.write(quiesced, nonce) != nil {
					return ErrProtocol
				}
				if mode == "extra-frame" {
					_ = writer.write(nativeOutput, []byte("late"))
				}
				if mode == "diagnostic" {
					_, _ = diagnostic.Write([]byte("private host diagnostic"))
				}
				if mode == "host-exit" {
					return ErrProtocol
				}
				return nil
			})
			defer raw.Close()
			session, err := NewSession(ctx, raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := session.Quiesce(ctx); err == nil || session.confirmed.Load() {
				t.Fatal("unverified response confirmed")
			}
		})
	}
}

func TestCancelledQuiesceCannotConfirm(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	owner := newFakeOwner()
	raw := ownerTransport(owner)
	defer raw.Close()
	session, err := NewSession(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if err := session.Quiesce(cancelled); err == nil {
		t.Fatal("cancelled request succeeded")
	}
	select {
	case <-session.qDone:
	case <-ctx.Done():
		t.Fatal("cancelled cleanup leaked")
	}
	if session.confirmed.Load() {
		t.Fatal("cancelled cleanup supplied proof")
	}
}

func TestUnconstructedSessionCannotSupplyProof(t *testing.T) {
	for _, session := range []*Session{nil, {}} {
		if !errors.Is(session.Quiesce(context.Background()), ErrProtocol) || !errors.Is(session.Close(), ErrProtocol) || !errors.Is(session.Wait(), ErrProtocol) {
			t.Fatal("unconstructed session admitted")
		}
	}
}

func TestCancelledHandshakeClosesTransport(t *testing.T) {
	raw := transport(func(in io.ReadCloser, out, diagnostic io.WriteCloser) error {
		_, err := io.Copy(io.Discard, in)
		return err
	})
	defer raw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if session, err := NewSession(ctx, raw); session != nil || !errors.Is(err, ErrProtocol) {
		t.Fatal("stalled handshake admitted")
	}
	select {
	case <-raw.done:
	case <-time.After(time.Second):
		t.Fatal("cancelled handshake retained transport")
	}
}

func TestHandshakeRejectsAnotherConnectionNonce(t *testing.T) {
	raw := transport(func(in io.ReadCloser, out, diagnostic io.WriteCloser) error {
		first, err := readFrame(in)
		if err != nil {
			return err
		}
		defer clear(first.data)
		nonce := append([]byte(nil), first.data[len(version):]...)
		defer clear(nonce)
		nonce[0] ^= 1
		writer := frameWriter{output: out}
		return writer.write(ready, nonce)
	})
	defer raw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if session, err := NewSession(ctx, raw); session != nil || !errors.Is(err, ErrProtocol) {
		t.Fatal("foreign connection acknowledgement admitted")
	}
}
