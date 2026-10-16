package nativehost

import (
	"bytes"
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asabla/dataground/internal/runtime/processowner"
)

type processOwner interface {
	Start(string, []string, []string) (processowner.Streams, error)
	Stop(context.Context) error
	Wait(context.Context) (processowner.Exit, error)
}

// Serve runs only in the dedicated host. Executable, arguments and environment
// are deployment-owned, never selected by incoming frames. Close must unblock
// the supplied streams. A successful return requires terminal descendant cleanup.
func Serve(ctx context.Context, input io.ReadCloser, output io.WriteCloser, executable string, arguments, environment []string) error {
	return serve(ctx, input, output, executable, arguments, environment, func() (processOwner, error) { return processowner.New() })
}

func serve(parent context.Context, input io.ReadCloser, output io.WriteCloser, executable string, arguments, environment []string, create func() (processOwner, error)) error {
	if parent == nil || input == nil || output == nil || create == nil {
		return ErrProtocol
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	abort := context.AfterFunc(ctx, func() { _ = input.Close(); _ = output.Close() })
	defer abort()
	defer input.Close()
	defer output.Close()
	// An incomplete or stalled handshake must not retain an idle host indefinitely.
	timer := time.AfterFunc(15*time.Second, cancel)
	defer timer.Stop()
	request, err := readFrame(input)
	defer clear(request.data)
	if err != nil || request.kind != hello || len(request.data) != len(version)+32 || !bytes.Equal(request.data[:len(version)], []byte(version)) || !timer.Stop() || ctx.Err() != nil {
		return ErrProtocol
	}
	nonce := append([]byte(nil), request.data[len(version):]...)
	defer clear(nonce)
	owner, err := create()
	if err != nil {
		return ErrProtocol
	}
	var cleanupDeadline time.Time
	defer func() {
		deadline := cleanupDeadline
		if deadline.IsZero() {
			deadline = time.Now().Add(30 * time.Second)
		}
		cleanup, stop := context.WithDeadline(context.Background(), deadline)
		defer stop()
		_ = owner.Stop(cleanup)
	}()
	streams, err := owner.Start(executable, arguments, environment)
	if err != nil {
		return ErrProtocol
	}
	defer streams.Input.Close()
	defer streams.Output.Close()
	defer streams.Errors.Close()
	writer := &frameWriter{output: output}
	if writer.write(ready, nonce) != nil {
		return ErrProtocol
	}

	type incomingFrame struct {
		frame
		err error
	}
	frames := make(chan incomingFrame, 1)
	failures := make(chan struct{}, 1)
	fail := func() {
		select {
		case failures <- struct{}{}:
		default:
		}
	}
	go func() {
		for {
			next, err := readFrame(input)
			select {
			case frames <- incomingFrame{next, err}:
			case <-ctx.Done():
				clear(next.data)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		for {
			select {
			case next := <-frames:
				clear(next.data)
			default:
				return
			}
		}
	}()

	incoming := make(chan []byte, 8)
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		defer func() {
			for {
				select {
				case data, ok := <-incoming:
					if !ok {
						return
					}
					clear(data)
				default:
					return
				}
			}
		}()
		for data := range incoming {
			err := writeNative(streams.Input, data)
			clear(data)
			if err != nil {
				fail()
				return
			}
		}
		_ = streams.Input.Close()
	}()
	inputClosed := false
	defer func() {
		if !inputClosed {
			close(incoming)
		}
	}()

	var outputFailed atomic.Bool
	var outputWG sync.WaitGroup
	outputWG.Add(3)
	outputDone := make(chan struct{})
	forward := func(source io.Reader, kind, end byte) {
		defer outputWG.Done()
		data := make([]byte, 32<<10)
		defer clear(data)
		for {
			n, err := source.Read(data)
			if n > 0 && writer.write(kind, data[:n]) != nil {
				outputFailed.Store(true)
				fail()
				return
			}
			if err != nil {
				if err != io.EOF || writer.write(end, nil) != nil {
					outputFailed.Store(true)
					fail()
				}
				return
			}
			if n == 0 {
				outputFailed.Store(true)
				fail()
				return
			}
		}
	}
	go forward(streams.Output, nativeOutput, outputEnd)
	go forward(streams.Errors, nativeDiagnostic, diagnosticEnd)
	go func() {
		defer outputWG.Done()
		exit, err := owner.Wait(ctx)
		if err != nil {
			outputFailed.Store(true)
			fail()
			return
		}
		status := byte(1)
		if exit.Success {
			status = 0
		}
		if writer.write(nativeExit, []byte{status}) != nil {
			outputFailed.Store(true)
			fail()
		}
	}()
	go func() { outputWG.Wait(); close(outputDone) }()

	for {
		select {
		case <-ctx.Done():
			return ErrProtocol
		case <-failures:
			return ErrProtocol
		case next := <-frames:
			if next.err != nil {
				return ErrProtocol
			}
			switch next.kind {
			case nativeInput:
				if inputClosed || len(next.data) == 0 {
					clear(next.data)
					return ErrProtocol
				}
				select {
				case incoming <- next.data:
				default:
					clear(next.data)
					return ErrProtocol
				}
			case inputEnd:
				if inputClosed || len(next.data) != 0 {
					clear(next.data)
					return ErrProtocol
				}
				inputClosed = true
				close(incoming)
			case quiesce:
				if len(next.data) != 0 {
					clear(next.data)
					return ErrProtocol
				}
				// Stop seals starts and closes input before it kills and reaps descendants.
				// Native I/O failures caused by this stop are expected; stream closure and
				// the exact kernel result remain mandatory before acknowledgement.
				cleanupDeadline = time.Now().Add(35 * time.Second)
				terminalTimer := time.AfterFunc(time.Until(cleanupDeadline), cancel)
				defer terminalTimer.Stop()
				err := owner.Stop(ctx)
				if !inputClosed {
					inputClosed = true
					close(incoming)
				}
				if err == nil {
					select {
					case <-inputDone:
					case <-ctx.Done():
						err = ErrProtocol
					}
				}
				if err == nil {
					select {
					case <-outputDone:
					case <-ctx.Done():
						err = ErrProtocol
					}
				}
				// The quiesce request is the final input frame. EOF proves the
				// controller did not append another native input or control.
				if err == nil {
					select {
					case last := <-frames:
						clear(last.data)
						if last.err != io.EOF {
							err = ErrProtocol
						}
					case <-ctx.Done():
						err = ErrProtocol
					}
				}
				if err != nil || outputFailed.Load() || ctx.Err() != nil {
					return ErrProtocol
				}
				if writer.write(quiesced, nonce) != nil {
					return ErrProtocol
				}
				return nil
			default:
				clear(next.data)
				return ErrProtocol
			}
		}
	}
}

func writeNative(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil || n <= 0 || n > len(data) {
			return ErrProtocol
		}
		data = data[n:]
	}
	return nil
}
