// Package nativehost carries native protocol bytes separately from terminal
// process-owner acknowledgements. It is an internal, single-session transport.
package nativehost

import (
	"encoding/binary"
	"errors"
	"io"
	"sync"
)

var ErrProtocol = errors.New("native owner transport could not be confirmed")
var ErrNativeExit = errors.New("native entry point exited unsuccessfully")

const version = "dataground.native-owner/v1"
const maximumPayload = 64 << 10
const (
	hello byte = iota + 1
	nativeInput
	inputEnd
	quiesce
	ready            byte = 16
	nativeOutput     byte = 17
	nativeDiagnostic byte = 18
	outputEnd        byte = 19
	diagnosticEnd    byte = 20
	nativeExit       byte = 21
	quiesced         byte = 22
)

type frame struct {
	kind byte
	data []byte
}

func readFrame(reader io.Reader) (frame, error) {
	var header [5]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return frame{}, err
	}
	size := binary.BigEndian.Uint32(header[1:])
	if size > maximumPayload {
		return frame{}, ErrProtocol
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(reader, data); err != nil {
		clear(data)
		return frame{}, ErrProtocol
	}
	return frame{header[0], data}, nil
}

type frameWriter struct {
	mu     sync.Mutex
	output io.Writer
}

func (writer *frameWriter) write(kind byte, data []byte) error {
	if len(data) > maximumPayload {
		return ErrProtocol
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	var header [5]byte
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
	for _, part := range [][]byte{header[:], data} {
		for len(part) > 0 {
			n, err := writer.output.Write(part)
			if err != nil || n <= 0 || n > len(part) {
				return ErrProtocol
			}
			part = part[n:]
		}
	}
	return nil
}
