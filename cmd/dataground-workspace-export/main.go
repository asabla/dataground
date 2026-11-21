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
	if run(ctx, os.Args[1:], os.Stdin, os.Stdout, workspace.Capture, openStoppedArchive) != nil {
		fmt.Fprintln(os.Stderr, "WORKSPACE_CHANGE_EXPORT_FAILED")
		os.Exit(1)
	}
}

type capturer func(context.Context, string) (*workspace.Snapshot, error)

func openStoppedArchive() (io.ReadCloser, error) {
	file := os.NewFile(3, "stopped-workspace-archive")
	if file == nil {
		return nil, workspace.ErrSourceInvalid
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > workspace.MaximumArchiveBytes {
		_ = file.Close()
		return nil, workspace.ErrSourceInvalid
	}
	return file, nil
}

// The caller must hold the workload quiescence fence. This command supplies no
// authorization or teardown permission. Its output contains private file data.
// Stream Close methods must unblock concurrent I/O when the context ends.
func run(ctx context.Context, args []string, input io.ReadCloser, output io.WriteCloser, capture capturer, openArchive func() (io.ReadCloser, error)) error {
	flags := flag.NewFlagSet("workspace-export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	digest, stoppedDigest, destination := "", "", "/sandbox/workspace"
	seen := map[string]bool{}
	for name, target := range map[string]*string{"archive-digest": &digest, "destination": &destination, "stopped-docker-archive-digest": &stoppedDigest} {
		flags.Func(name, "", func(value string) error {
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
	if !validArchiveDigest(digest) || (seen["stopped-docker-archive-digest"] && (seen["destination"] || !validArchiveDigest(stoppedDigest) || openArchive == nil)) {
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
	var changes *workspace.ChangeSet
	if stoppedDigest != "" {
		changes, err = compareStoppedArchive(ctx, base, stoppedDigest, openArchive)
	} else {
		result, captureErr := capture(ctx, filepath.Dir(destination))
		if captureErr != nil {
			return captureErr
		}
		changes, err = workspace.Compare(base, result)
	}
	if err != nil {
		return err
	}
	err = changes.Write(ctx, output)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func validArchiveDigest(digest string) bool {
	encoded, prefixed := strings.CutPrefix(digest, "sha256:")
	if !prefixed || len(encoded) != 64 || strings.ToLower(encoded) != encoded {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

func compareStoppedArchive(ctx context.Context, base *workspace.Snapshot, digest string, openArchive func() (io.ReadCloser, error)) (*workspace.ChangeSet, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input, err := openArchive()
	if err != nil {
		return nil, err
	}
	defer input.Close()
	stopIO := context.AfterFunc(ctx, func() { _ = input.Close() })
	defer stopIO()
	archive, err := io.ReadAll(io.LimitReader(input, workspace.MaximumArchiveBytes+1))
	defer clear(archive)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, workspace.ErrSourceInvalid
	}
	return workspace.CompareStoppedDockerArchive(base, archive, digest)
}
