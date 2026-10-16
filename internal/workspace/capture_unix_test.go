//go:build linux || darwin

package workspace

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestWorkspaceCaptureRejectsSpecialNodesWithoutBlocking(t *testing.T) {
	for _, kind := range []string{"fifo", "socket"} {
		t.Run(kind, func(t *testing.T) {
			base := inspectedFixture(t)
			parent := t.TempDir()
			if _, err := base.Materialize(context.Background(), parent); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(parent, "workspace", "node")
			if kind == "fifo" {
				if err := syscall.Mkfifo(name, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				// macOS bounds Unix socket paths below the length of some test temp paths.
				directory, err := os.MkdirTemp("/tmp", "dg-socket-")
				if err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(directory)
				socket := filepath.Join(directory, "socket")
				listener, err := net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				if err = os.Rename(socket, name); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if snapshot, err := Capture(ctx, parent); snapshot != nil || !errors.Is(err, ErrCaptureUnavailable) {
				t.Fatal("special node accepted or blocked", err)
			}
		})
	}
}
