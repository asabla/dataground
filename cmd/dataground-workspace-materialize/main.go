// dataground-workspace-materialize is an internal sandbox provisioning helper.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, installSource); err != nil {
		fmt.Fprintln(os.Stderr, "WORKSPACE_SOURCE_MATERIALIZATION_FAILED")
		os.Exit(1)
	}
}

type installer func(context.Context, *workspace.Snapshot, string) (workspace.Receipt, error)

func run(ctx context.Context, args []string, input io.ReadCloser, output io.Writer, install installer) error {
	flags := flag.NewFlagSet("workspace-materialize", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	digest := flags.String("archive-digest", "", "expected SHA-256 digest")
	destination := flags.String("destination", "/sandbox/workspace", "image-owned workspace destination")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *digest == "" || (*destination != "/sandbox/workspace" && *destination != "/workspace") {
		return workspace.ErrSourceInvalid
	}
	decoded, digestErr := hex.DecodeString(strings.TrimPrefix(*digest, "sha256:"))
	if digestErr != nil || len(decoded) != 32 || len(*digest) != 71 || !strings.HasPrefix(*digest, "sha256:") || strings.ToLower(*digest) != *digest {
		return workspace.ErrSourceInvalid
	}
	type readResult struct {
		data []byte
		err  error
	}
	result := make(chan readResult, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(input, workspace.MaximumArchiveBytes+1))
		result <- readResult{data, err}
	}()
	var archive []byte
	select {
	case read := <-result:
		if read.err != nil {
			clear(read.data)
			return workspace.ErrSourceInvalid
		}
		archive = read.data
	case <-ctx.Done():
		_ = input.Close()
		read := <-result
		clear(read.data)
		return ctx.Err()
	}
	defer clear(archive)
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot, err := workspace.Inspect(archive, *digest)
	if err != nil {
		return err
	}
	receipt, err := install(ctx, snapshot, filepath.Dir(*destination))
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(receipt)
}

func installSource(ctx context.Context, snapshot *workspace.Snapshot, parent string) (workspace.Receipt, error) {
	receipt, err := snapshot.Materialize(ctx, parent)
	if errors.Is(err, workspace.ErrSourceExists) {
		// An exact read-back handles a lost acknowledgement without writing over
		// existing state. The control plane still owns source identity and fencing.
		return snapshot.Verify(ctx, parent)
	}
	return receipt, err
}
