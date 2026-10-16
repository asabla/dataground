package nativehost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestServerRejectsInvalidControlSequences(t *testing.T) {
	for _, mode := range []string{"wrong-version", "input-before-hello", "empty-input", "duplicate-input-end", "payload-on-quiesce", "input-after-quiesce", "unknown", "lost-input"} {
		t.Run(mode, func(t *testing.T) {
			owner := newFakeOwner()
			raw := ownerTransport(owner)
			defer raw.Close()
			writer := frameWriter{output: raw.Input()}
			greeting := append([]byte(version), make([]byte, 32)...)
			kind := hello
			if mode == "wrong-version" {
				greeting[0] ^= 1
			}
			if mode == "input-before-hello" {
				kind = nativeInput
			}
			if err := writer.write(kind, greeting); err != nil {
				t.Fatal(err)
			}
			if mode != "wrong-version" && mode != "input-before-hello" {
				if response, err := readFrame(raw.Output()); err != nil || response.kind != ready {
					t.Fatal("missing ready", err)
				}
				switch mode {
				case "empty-input":
					_ = writer.write(nativeInput, nil)
				case "duplicate-input-end":
					_ = writer.write(inputEnd, nil)
					_ = writer.write(inputEnd, nil)
				case "payload-on-quiesce":
					_ = writer.write(quiesce, []byte("extra"))
				case "input-after-quiesce":
					_ = writer.write(quiesce, nil)
					_ = writer.write(nativeInput, []byte("late"))
				case "unknown":
					_ = writer.write(255, nil)
				}
				go io.Copy(io.Discard, raw.Output())
			}
			_ = raw.Input().Close()
			done := make(chan error, 1)
			go func() { done <- raw.Wait() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("invalid input accepted")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("invalid input did not stop host")
			}
			if mode == "wrong-version" || mode == "input-before-hello" {
				if owner.stopCount.Load() != 0 {
					t.Fatal("owner started before valid handshake")
				}
			} else if owner.stopCount.Load() == 0 {
				t.Fatal("failed transport omitted cleanup")
			}
		})
	}
}

type failingOutput struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (out *failingOutput) Write(data []byte) (int, error) {
	out.mu.Lock()
	defer out.mu.Unlock()
	if len(data) == 5 && data[0] == outputEnd {
		return 0, io.ErrClosedPipe
	}
	return out.data.Write(data)
}
func (*failingOutput) Close() error { return nil }

func TestServerCannotAcknowledgeFailedOutputClosure(t *testing.T) {
	var input bytes.Buffer
	writer := frameWriter{output: &input}
	_ = writer.write(hello, append([]byte(version), make([]byte, 32)...))
	_ = writer.write(quiesce, nil)
	owner := newFakeOwner()
	output := &failingOutput{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := serve(ctx, io.NopCloser(&input), output, "/fixed", []string{"/fixed"}, nil, func() (processOwner, error) { return owner, nil })
	if !errors.Is(err, ErrProtocol) {
		t.Fatal("failed stream closure acknowledged", err)
	}
	for output.data.Len() > 0 {
		next, err := readFrame(&output.data)
		if err != nil {
			t.Fatal(err)
		}
		if next.kind == quiesced {
			t.Fatal("sent terminal acknowledgement after output failure")
		}
	}
}
