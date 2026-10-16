// dataground-workspace-export is an internal helper for a quiesced sandbox.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/asabla/dataground/internal/workspace"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if run(ctx, os.Args[1:], os.Stdin, os.Stdout, workspace.Capture) != nil {
		fmt.Fprintln(os.Stderr, "WORKSPACE_CHANGE_EXPORT_FAILED")
		os.Exit(1)
	}
}

type capturer func(context.Context, string) (*workspace.Snapshot, error)

// The caller must hold the workload quiescence fence. This command supplies no
// authorization or teardown permission. Its output contains private file data.
// Stream Close methods must unblock concurrent I/O when the context ends.
func run(ctx context.Context, args []string, input io.ReadCloser, output io.WriteCloser, capture capturer) error {
	flags := flag.NewFlagSet("workspace-export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	digest, destination := "", "/sandbox/workspace"
	seen := map[string]bool{}
	for name, target := range map[string]*string{"archive-digest": &digest, "destination": &destination} {
		flags.Func(name, "exact source digest or image-owned workspace", func(value string) error {
			if seen[name] {
				return workspace.ErrChangesInvalid
			}
			seen[name] = true
			*target = value
			return nil
		})
	}
	if flags.Parse(args) != nil || flags.NArg() != 0 || (destination != "/sandbox/workspace" && destination != "/workspace") {
		return workspace.ErrChangesInvalid
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil || len(decoded) != 32 || len(digest) != 71 || !strings.HasPrefix(digest, "sha256:") || strings.ToLower(digest) != digest {
		return workspace.ErrChangesInvalid
	}
	stopIO := context.AfterFunc(ctx, func() { _ = input.Close(); _ = output.Close() })
	defer stopIO()
	archive, err := io.ReadAll(io.LimitReader(input, workspace.MaximumArchiveBytes+1))
	defer clear(archive)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return workspace.ErrChangesInvalid
	}
	base, err := workspace.Inspect(archive, digest)
	if err != nil {
		return err
	}
	result, err := capture(ctx, filepath.Dir(destination))
	if err != nil {
		return err
	}
	changes, err := workspace.Compare(base, result)
	if err != nil {
		return err
	}
	err = changes.Write(ctx, output)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
