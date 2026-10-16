// Package processowner owns terminal descendant cleanup in a dedicated native
// runtime host. It is not a replacement for OpenShell's sandbox enforcement.
package processowner

import (
	"errors"
	"io"
)

var (
	ErrUnavailable = errors.New("native process ownership is unavailable")
	ErrState       = errors.New("native process owner state does not permit this operation")
)

// Streams contain native protocol bytes, never process-owner acknowledgements.
// The caller must keep these separate from its trusted control transport.
type Streams struct {
	Input  io.WriteCloser
	Output io.ReadCloser
	Errors io.ReadCloser
}

// Exit reports only whether the native entry point exited successfully. It does
// not report descendant cleanup or grant a quiescence boundary.
type Exit struct{ Success bool }
