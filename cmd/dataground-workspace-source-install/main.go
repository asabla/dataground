// dataground-workspace-source-install installs one reviewed source archive into
// the explicit loopback development store under database-held operator authority.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/asabla/dataground/internal/execution/s3store"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/workspace"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "WORKSPACE_SOURCE_INSTALLATION_UNAVAILABLE")
		os.Exit(1)
	}
}

type installationOptions struct {
	binding                       workspace.SourceInstallation
	archivePath, endpoint, bucket string
}

func parseOptions(args []string) (installationOptions, error) {
	var o installationOptions
	f := flag.NewFlagSet("workspace-source-install", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	add := func(name string, target *string) {
		seen := false
		f.Func(name, "required reviewed installation value", func(v string) error {
			if seen || v == "" {
				return workspace.ErrSourceInvalid
			}
			seen = true
			*target = v
			return nil
		})
	}
	add("archive-file", &o.archivePath)
	add("archive-digest", &o.binding.Record.ArchiveDigest)
	add("isolation-domain", &o.binding.Record.IsolationDomainID)
	add("workspace", &o.binding.Record.WorkspaceID)
	add("source", &o.binding.Record.ID)
	add("actor", &o.binding.ActorID)
	add("correlation-id", &o.binding.CorrelationID)
	add("storage-endpoint", &o.endpoint)
	add("storage-bucket", &o.bucket)
	add("storage-profile-digest", &o.binding.Record.StorageProfileDigest)
	if f.Parse(args) != nil || f.NArg() != 0 || f.NFlag() != 10 {
		return o, workspace.ErrSourceInvalid
	}
	endpoint, err := url.Parse(o.endpoint)
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Opaque != "" || endpoint.ForceQuery || !net.ParseIP(endpoint.Hostname()).IsLoopback() {
		return o, workspace.ErrSourceInvalid
	}
	o.binding.Record.SchemaVersion = workspace.SourceRecordV1
	// Validate identity and attribution before opening the source file. Actual
	// sizes and counts come only from complete archive inspection below.
	check := o.binding
	check.Record.ArchiveBytes = 1024
	if !workspace.ValidSourceInstallation(check) {
		return o, workspace.ErrSourceInvalid
	}
	return o, nil
}
func run(ctx context.Context, args []string, output io.Writer) error {
	options, err := parseOptions(args)
	if err != nil {
		return err
	}
	archive, err := readArchive(options.archivePath)
	if err != nil {
		return err
	}
	defer clear(archive)
	snapshot, err := workspace.Inspect(archive, options.binding.Record.ArchiveDigest)
	if err != nil {
		return err
	}
	receipt := snapshot.Receipt()
	options.binding.Record.ArchiveBytes = int64(len(archive))
	options.binding.Record.SourceBytes = receipt.SourceBytes
	options.binding.Record.EntryCount = receipt.EntryCount
	transport := &http.Transport{Proxy: nil, DisableCompression: true, ResponseHeaderTimeout: 15 * time.Second}
	defer transport.CloseIdleConnections()
	base, err := s3store.New(s3store.Config{Endpoint: options.endpoint, Bucket: options.bucket, AddressingStyle: s3store.PathStyle, AllowHTTPForLoopback: true, HTTPClient: &http.Client{Transport: transport, Timeout: 30 * time.Second}})
	if err != nil {
		return workspace.ErrSourceInvalid
	}
	objects, err := s3store.NewWorkspaceSourceStore(base)
	if err != nil {
		return err
	}
	if objects.WorkspaceSourceStorageDigest() != options.binding.Record.StorageProfileDigest {
		return workspace.ErrSourceConflict
	}
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if strings.TrimSpace(databaseURL) == "" {
		return workspace.ErrSourceUnavailable
	}
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		return workspace.ErrSourceUnavailable
	}
	err = persistence.RequireCurrentSchema(ctx, database)
	closeErr := database.Close()
	if err != nil || closeErr != nil {
		return workspace.ErrSourceUnavailable
	}
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		return workspace.ErrSourceUnavailable
	}
	defer pool.Close()
	entry, err := workspace.InstallSource(ctx, persistence.NewRepository(pool), objects, options.binding, archive)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(entry.Installation.Record)
}
func readArchive(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !privateFile(before) {
		return nil, workspace.ErrSourceInvalid
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, workspace.ErrSourceInvalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !privateFile(opened) || !os.SameFile(before, opened) {
		return nil, workspace.ErrSourceInvalid
	}
	data, err := io.ReadAll(io.LimitReader(file, workspace.MaximumArchiveBytes+1))
	after, afterErr := os.Lstat(path)
	if err != nil || afterErr != nil || !privateFile(after) || !os.SameFile(opened, after) || len(data) < 1024 || len(data) > workspace.MaximumArchiveBytes {
		clear(data)
		return nil, workspace.ErrSourceInvalid
	}
	return data, nil
}
func privateFile(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Size() < 1024 || info.Size() > workspace.MaximumArchiveBytes {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1
}
