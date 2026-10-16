package nativehost

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asabla/dataground/internal/execution"
)

// Session exposes only native bytes through RuntimeSession. Quiesce additionally
// requires the private owner's nonce-bound acknowledgement, complete output,
// empty host diagnostics and successful host exit. It grants no export authority.
// A Session must not be copied after construction.
type Session struct {
	raw            execution.RuntimeSession
	input          io.WriteCloser
	writer         frameWriter
	sendMu         sync.Mutex
	sealed         atomic.Bool
	inputClosed    bool
	nonce          [32]byte
	outR           *io.PipeReader
	outW           *io.PipeWriter
	errR           *io.PipeReader
	errW           *io.PipeWriter
	ready          chan struct{}
	exited         chan struct{}
	wireDone       chan struct{}
	rawDone        chan struct{}
	diagnosticDone chan struct{}
	failed         chan struct{}
	failOnce       sync.Once
	waitOnce       sync.Once
	closeOnce      sync.Once
	qOnce          sync.Once
	qDone          chan struct{}
	confirmed      atomic.Bool
	mu             sync.Mutex
	waitErr        error
	qErr           error
	stopParent     func() bool
}

func NewSession(ctx context.Context, raw execution.RuntimeSession) (*Session, error) {
	if ctx == nil || ctx.Err() != nil || raw == nil {
		if raw != nil {
			_ = raw.Close()
		}
		return nil, ErrProtocol
	}
	input, output, diagnostic := raw.Input(), raw.Output(), raw.Errors()
	if input == nil || output == nil || diagnostic == nil {
		_ = raw.Close()
		return nil, ErrProtocol
	}
	s := &Session{raw: raw, input: input, writer: frameWriter{output: input}, ready: make(chan struct{}), exited: make(chan struct{}), wireDone: make(chan struct{}), rawDone: make(chan struct{}), diagnosticDone: make(chan struct{}), failed: make(chan struct{}), qDone: make(chan struct{})}
	s.outR, s.outW = io.Pipe()
	s.errR, s.errW = io.Pipe()
	if _, err := rand.Read(s.nonce[:]); err != nil {
		_ = raw.Close()
		return nil, ErrProtocol
	}
	s.stopParent = context.AfterFunc(ctx, s.fail)
	accepted := false
	defer func() {
		if !accepted {
			s.stopParent()
			s.fail()
		}
	}()
	go s.receive(output)
	go func() {
		var data [1]byte
		n, err := diagnostic.Read(data[:])
		if n != 0 || err != io.EOF {
			s.fail()
			return
		}
		close(s.diagnosticDone)
	}()
	go func() {
		if raw.Wait() != nil {
			s.fail()
			return
		}
		close(s.rawDone)
	}()
	handshake, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	abort := context.AfterFunc(handshake, s.fail)
	defer abort()
	go func() {
		data := append([]byte(version), s.nonce[:]...)
		defer clear(data)
		if s.writer.write(hello, data) != nil {
			s.fail()
		}
	}()
	select {
	case <-s.ready:
		if handshake.Err() != nil {
			s.fail()
			return nil, ErrProtocol
		}
		select {
		case <-s.failed:
			return nil, ErrProtocol
		default:
		}
		accepted = true
		return s, nil
	case <-s.failed:
		return nil, ErrProtocol
	case <-handshake.Done():
		s.fail()
		return nil, ErrProtocol
	}
}

func (s *Session) Input() io.WriteCloser { return nativeWriter{s} }
func (s *Session) Output() io.ReadCloser { return s.outR }
func (s *Session) Errors() io.ReadCloser { return s.errR }
func (s *Session) Wait() error {
	if s == nil || s.raw == nil {
		return ErrProtocol
	}
	<-s.exited
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waitErr
}

func (s *Session) finishWait(err error) {
	s.waitOnce.Do(func() {
		s.mu.Lock()
		s.waitErr = err
		s.mu.Unlock()
		close(s.exited)
	})
}

// A process-local acknowledgement is not a durable or transferable certificate.
func (*Session) MarshalJSON() ([]byte, error) { return nil, ErrProtocol }

// Quiesce is terminal. Native output is drained rather than delivered once it
// starts, so an abandoned consumer cannot prevent cleanup acknowledgement.
// Callers must finish processing required native output before requesting it.
// Failed or lost acknowledgement remains unconfirmed; opening a new session
// cannot recover this session's proof or permit another native attempt.
func (s *Session) Quiesce(ctx context.Context) error {
	if s == nil || s.raw == nil || ctx == nil {
		return ErrProtocol
	}
	if s.confirmed.Load() {
		return nil
	}
	s.qOnce.Do(func() { go s.quiesce(ctx) })
	select {
	case <-s.qDone:
		s.mu.Lock()
		err := s.qErr
		s.mu.Unlock()
		return err
	case <-ctx.Done():
		return ErrProtocol
	}
}

func (s *Session) quiesce(parent context.Context) {
	err := ErrProtocol
	defer func() { s.mu.Lock(); s.qErr = err; s.mu.Unlock(); close(s.qDone) }()
	ctx, cancel := context.WithTimeout(parent, 40*time.Second)
	defer cancel()
	abort := context.AfterFunc(ctx, s.fail)
	defer abort()
	s.sealed.Store(true)
	_ = s.outW.Close()
	_ = s.errW.Close()
	sent := make(chan bool, 1)
	go func() {
		s.sendMu.Lock()
		defer s.sendMu.Unlock()
		result := s.writer.write(quiesce, nil)
		if result == nil {
			result = s.input.Close()
		}
		sent <- result == nil
	}()
	select {
	case ok := <-sent:
		if !ok {
			s.fail()
			return
		}
	case <-s.failed:
		return
	case <-ctx.Done():
		s.fail()
		return
	}
	for _, done := range []chan struct{}{s.wireDone, s.rawDone, s.diagnosticDone} {
		select {
		case <-done:
		case <-s.failed:
			return
		case <-ctx.Done():
			s.fail()
			return
		}
	}
	if ctx.Err() != nil {
		s.fail()
		return
	}
	select {
	case <-s.failed:
		return
	default:
	}
	s.confirmed.Store(true)
	s.stopParent()
	err = nil
}

func (s *Session) Close() error {
	if s == nil || s.raw == nil {
		return ErrProtocol
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	err := s.Quiesce(ctx)
	s.closeOnce.Do(func() { s.stopParent(); s.fail() })
	return err
}

func (s *Session) fail() {
	s.failOnce.Do(func() {
		close(s.failed)
		s.finishWait(ErrProtocol)
		s.sealed.Store(true)
		_ = s.outW.CloseWithError(ErrProtocol)
		_ = s.errW.CloseWithError(ErrProtocol)
		// Closing input first unblocks any concurrent frame write. Raw Close owns
		// bounded transport shutdown; no failure can supply a terminal proof.
		go func() { _ = s.input.Close(); _ = s.raw.Close() }()
	})
}

func (s *Session) receive(source io.Reader) {
	first, err := readFrame(source)
	if err != nil || first.kind != ready || !bytes.Equal(first.data, s.nonce[:]) {
		clear(first.data)
		s.fail()
		return
	}
	clear(first.data)
	close(s.ready)
	outputClosed, diagnosticClosed, exited, acknowledged := false, false, false, false
	for {
		next, err := readFrame(source)
		if err == io.EOF && acknowledged {
			close(s.wireDone)
			return
		}
		if err != nil || acknowledged {
			clear(next.data)
			s.fail()
			return
		}
		valid := true
		switch next.kind {
		case nativeOutput, nativeDiagnostic:
			isOutput := next.kind == nativeOutput
			if len(next.data) == 0 || (isOutput && outputClosed) || (!isOutput && diagnosticClosed) {
				valid = false
				break
			}
			target := s.errW
			if isOutput {
				target = s.outW
			}
			if !s.sealed.Load() {
				if _, err := target.Write(next.data); err != nil && !s.sealed.Load() {
					valid = false
				}
			}
		case outputEnd:
			valid = len(next.data) == 0 && !outputClosed
			outputClosed = true
			_ = s.outW.Close()
		case diagnosticEnd:
			valid = len(next.data) == 0 && !diagnosticClosed
			diagnosticClosed = true
			_ = s.errW.Close()
		case nativeExit:
			valid = !exited && len(next.data) == 1 && next.data[0] <= 1
			if valid {
				result := error(nil)
				if next.data[0] != 0 {
					result = ErrNativeExit
				}
				s.finishWait(result)
				exited = true
			}
		case quiesced:
			valid = s.sealed.Load() && outputClosed && diagnosticClosed && exited && bytes.Equal(next.data, s.nonce[:])
			acknowledged = valid
		default:
			valid = false
		}
		clear(next.data)
		if !valid {
			s.fail()
			return
		}
	}
}

type nativeWriter struct{ session *Session }

func (writer nativeWriter) Write(data []byte) (int, error) {
	s := writer.session
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sealed.Load() || s.inputClosed {
		return 0, ErrProtocol
	}
	total := 0
	for len(data) > 0 {
		if s.sealed.Load() {
			return total, ErrProtocol
		}
		size := min(len(data), maximumPayload)
		if s.writer.write(nativeInput, data[:size]) != nil {
			s.fail()
			return total, ErrProtocol
		}
		total += size
		data = data[size:]
	}
	return total, nil
}
func (writer nativeWriter) Close() error {
	s := writer.session
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.inputClosed {
		return nil
	}
	if s.sealed.Load() {
		return ErrProtocol
	}
	s.inputClosed = true
	if s.writer.write(inputEnd, nil) != nil {
		s.fail()
		return ErrProtocol
	}
	return nil
}
