package canaryruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"sync"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/security/canarysource"
)

const maxRuntimeErrorBytes = 16 << 20

var (
	ErrInvalidConfiguration = errors.New("invalid runtime credential source configuration")
	ErrCredentialSource     = errors.New("runtime credential evidence source unavailable")
	ErrSerialization        = errors.New("runtime credential source cannot be serialized")

	runIDPattern    = regexp.MustCompile(`^[a-f0-9]{32}$`)
	resourcePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

type Config struct {
	RunID       string
	RuntimeName string
	Session     execution.RuntimeSession
}

// Sources wraps the exact native session consumed by the runtime adapter and
// owns its complete stderr stream for one later credential-evidence scan.
type Sources struct {
	state *state
}

type state struct {
	mu sync.Mutex

	config Config
	limit  int

	errorsOpened  bool
	captureDone   chan struct{}
	captureOnce   sync.Once
	capture       []byte
	captureFailed bool
	overflow      bool

	sourceOpened bool
	abandoned    bool
	sourceAbort  chan struct{}
	abortOnce    sync.Once
	closeOnce    sync.Once
	closeErr     error
}

func New(config Config) (*Sources, error) {
	return newWithLimit(config, maxRuntimeErrorBytes)
}

func newWithLimit(config Config, limit int) (*Sources, error) {
	if !runIDPattern.MatchString(config.RunID) ||
		!resourcePattern.MatchString(config.RuntimeName) ||
		isNil(config.Session) ||
		limit <= 0 ||
		limit > maxRuntimeErrorBytes {
		return nil, ErrInvalidConfiguration
	}
	return &Sources{state: &state{
		config:      config,
		limit:       limit,
		captureDone: make(chan struct{}),
		sourceAbort: make(chan struct{}),
	}}, nil
}

func (sources *Sources) Input() io.WriteCloser {
	if sources == nil || sources.state == nil {
		return nil
	}
	return sources.state.config.Session.Input()
}

func (sources *Sources) Output() io.ReadCloser {
	if sources == nil || sources.state == nil {
		return nil
	}
	return sources.state.config.Session.Output()
}

func (sources *Sources) Errors() io.ReadCloser {
	if sources == nil || sources.state == nil {
		return failingReader{}
	}
	state := sources.state
	state.mu.Lock()
	if state.errorsOpened {
		state.captureFailed = true
		state.abandonLocked()
		state.mu.Unlock()
		return failingReader{}
	}
	state.errorsOpened = true
	session := state.config.Session
	state.mu.Unlock()

	stream := session.Errors()
	if isNil(stream) {
		state.finishCapture(true)
		return failingReader{}
	}
	return &captureStream{source: stream, state: state}
}

func (sources *Sources) Wait() error {
	if sources == nil || sources.state == nil {
		return ErrCredentialSource
	}
	return sources.state.config.Session.Wait()
}

func (sources *Sources) Close() error {
	if sources == nil || sources.state == nil {
		return ErrCredentialSource
	}
	state := sources.state
	state.closeOnce.Do(func() {
		state.closeErr = state.config.Session.Close()
	})
	return state.closeErr
}

// Discard clears an unused capture when collection fails before reaching the
// canonical runtime-errors surface.
func (sources *Sources) Discard() {
	if sources == nil || sources.state == nil {
		return
	}
	sources.state.abandon()
}

func (sources *Sources) OpenRuntimeErrors(
	ctx context.Context,
	request canarysource.Request,
) (io.ReadCloser, error) {
	if sources == nil || sources.state == nil || ctx == nil {
		return nil, ErrCredentialSource
	}
	state := sources.state
	state.mu.Lock()
	if state.sourceOpened ||
		!state.errorsOpened ||
		request.RunID != state.config.RunID ||
		request.Surface != "runtime-errors" ||
		request.ResourceName != state.config.RuntimeName {
		state.sourceOpened = true
		state.abandonLocked()
		state.mu.Unlock()
		return nil, ErrCredentialSource
	}
	state.sourceOpened = true
	done := state.captureDone
	abort := state.sourceAbort
	state.mu.Unlock()

	select {
	case <-ctx.Done():
		state.abandon()
		return nil, errors.Join(ErrCredentialSource, ctx.Err())
	case <-abort:
		return nil, ErrCredentialSource
	case <-done:
	}

	state.mu.Lock()
	if state.captureFailed || state.overflow || state.abandoned {
		state.clearCaptureLocked()
		state.mu.Unlock()
		return nil, ErrCredentialSource
	}
	captured := state.capture
	state.capture = nil
	state.mu.Unlock()
	return &sensitiveReader{reader: bytes.NewReader(captured), content: captured}, nil
}

func (Sources) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

func (state *state) appendCapture(value []byte) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.abandoned || state.captureFailed || state.overflow || len(value) == 0 {
		return
	}
	remaining := state.limit - len(state.capture)
	if remaining <= 0 || len(value) > remaining {
		state.overflow = true
		state.abandonLocked()
		return
	}
	state.capture = append(state.capture, value...)
}

func (state *state) finishCapture(failed bool) {
	state.captureOnce.Do(func() {
		state.mu.Lock()
		state.captureFailed = state.captureFailed || failed
		if failed {
			state.abandonLocked()
		} else if state.abandoned {
			state.clearCaptureLocked()
		}
		state.mu.Unlock()
		close(state.captureDone)
	})
}

func (state *state) abandon() {
	state.mu.Lock()
	state.abandonLocked()
	state.mu.Unlock()
}

func (state *state) abandonLocked() {
	state.abandoned = true
	state.clearCaptureLocked()
	state.abortOnce.Do(func() {
		close(state.sourceAbort)
	})
}

func (state *state) clearCaptureLocked() {
	clear(state.capture)
	state.capture = nil
}

type captureStream struct {
	mu       sync.Mutex
	source   io.ReadCloser
	state    *state
	eof      bool
	closed   bool
	closeErr error
}

func (stream *captureStream) Read(buffer []byte) (int, error) {
	count, err := stream.source.Read(buffer)
	if count > 0 {
		stream.state.appendCapture(buffer[:count])
	}
	if err != nil {
		stream.mu.Lock()
		if errors.Is(err, io.EOF) {
			stream.eof = true
		}
		stream.mu.Unlock()
		stream.state.finishCapture(!errors.Is(err, io.EOF))
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return count, ErrCredentialSource
	}
	return count, err
}

func (stream *captureStream) Close() error {
	stream.mu.Lock()
	if stream.closed {
		err := stream.closeErr
		stream.mu.Unlock()
		return err
	}
	stream.closed = true
	complete := stream.eof
	stream.mu.Unlock()

	err := stream.source.Close()
	failed := !complete || err != nil
	stream.state.finishCapture(failed)
	stream.mu.Lock()
	if failed {
		stream.closeErr = ErrCredentialSource
	}
	err = stream.closeErr
	stream.mu.Unlock()
	return err
}

type sensitiveReader struct {
	mu       sync.Mutex
	reader   *bytes.Reader
	content  []byte
	eof      bool
	closed   bool
	closeErr error
}

func (reader *sensitiveReader) Read(buffer []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.closed {
		return 0, ErrCredentialSource
	}
	count, err := reader.reader.Read(buffer)
	if errors.Is(err, io.EOF) {
		reader.eof = true
	}
	return count, err
}

func (reader *sensitiveReader) Close() error {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.closed {
		return reader.closeErr
	}
	reader.closed = true
	complete := reader.eof
	clear(reader.content)
	reader.content = nil
	reader.reader.Reset(nil)
	if !complete {
		reader.closeErr = ErrCredentialSource
	}
	return reader.closeErr
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, ErrCredentialSource }
func (failingReader) Close() error             { return ErrCredentialSource }

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var (
	_ execution.RuntimeSession    = (*Sources)(nil)
	_ canarysource.RuntimeSources = (*Sources)(nil)
	_ json.Marshaler              = (*Sources)(nil)
	_ io.ReadCloser               = (*captureStream)(nil)
	_ io.ReadCloser               = (*sensitiveReader)(nil)
)
