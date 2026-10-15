// dataground-workspace-source-access manages and verifies internal development
// source grants. Its database credential is an administrative trust boundary.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

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
		fmt.Fprintln(os.Stderr, "WORKSPACE_SOURCE_ACCESS_FAILED")
		os.Exit(1)
	}
}

type options struct {
	operation                     string
	change                        workspace.SourceGrantChange
	operationID, endpoint, bucket string
}

func parseOptions(args []string) (options, error) {
	var o options
	if len(args) == 0 {
		return o, workspace.ErrSourceInvalid
	}
	o.operation = args[0]
	if o.operation != "activate" && o.operation != "revoke" && o.operation != "verify" {
		return o, workspace.ErrSourceInvalid
	}
	flags := flag.NewFlagSet("source-access", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	add := func(name string, target *string) {
		seen := false
		flags.Func(name, "required reviewed value", func(v string) error {
			if seen || v == "" {
				return workspace.ErrSourceInvalid
			}
			seen = true
			*target = v
			return nil
		})
	}
	add("isolation-domain", &o.change.Record.IsolationDomainID)
	add("workspace", &o.change.Record.WorkspaceID)
	add("source", &o.change.Record.ID)
	add("archive-digest", &o.change.Record.ArchiveDigest)
	add("storage-profile-digest", &o.change.Record.StorageProfileDigest)
	add("revision", &o.change.RevisionID)
	add("actor", &o.change.ActorID)
	add("correlation-id", &o.change.CorrelationID)
	var generation, reason, activated, expires string
	add("generation", &generation)
	expected := 10
	if o.operation == "verify" {
		add("operation-id", &o.operationID)
		add("storage-endpoint", &o.endpoint)
		add("storage-bucket", &o.bucket)
		expected = 12
	} else {
		add("reason", &reason)
	}
	if o.operation == "activate" {
		add("activated-at", &activated)
		add("expires-at", &expires)
		expected = 12
	}
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || flags.NFlag() != expected {
		return o, workspace.ErrSourceInvalid
	}
	parsed, err := strconv.ParseInt(generation, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != generation {
		return o, workspace.ErrSourceInvalid
	}
	o.change.Generation = parsed
	o.change.Record.SchemaVersion = workspace.SourceRecordV1
	o.change.Record.ArchiveBytes = 1024
	if o.operation == "verify" {
		use := workspace.SourceUse{Contract: workspace.SourceUseV1, Record: o.change.Record, RevisionID: o.change.RevisionID, OperationID: o.operationID, GrantGeneration: parsed, ActorID: o.change.ActorID, CorrelationID: o.change.CorrelationID}
		endpoint, err := url.Parse(o.endpoint)
		if !use.Valid() || err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Opaque != "" || endpoint.ForceQuery || !net.ParseIP(endpoint.Hostname()).IsLoopback() {
			return o, workspace.ErrSourceInvalid
		}
	} else {
		if reason == "" || len(reason) > 512 || !utf8.ValidString(reason) || strings.TrimSpace(reason) != reason {
			return o, workspace.ErrSourceInvalid
		}
		for _, c := range reason {
			if unicode.IsControl(c) {
				return o, workspace.ErrSourceInvalid
			}
		}
		sum := sha256.Sum256([]byte(reason))
		o.change.ReasonDigest = "sha256:" + hex.EncodeToString(sum[:])
		o.change.Contract = workspace.SourceGrantV1
		o.change.Operation = o.operation
		if o.operation == "activate" {
			o.change.ActivatedAt, _ = time.Parse(time.RFC3339Nano, activated)
			o.change.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
		}
		if !o.change.Valid() {
			return o, workspace.ErrSourceInvalid
		}
	}
	return o, nil
}
func run(ctx context.Context, args []string, output io.Writer) error {
	o, err := parseOptions(args)
	if err != nil {
		return err
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
	repo := persistence.NewRepository(pool)
	entry, err := repo.GetWorkspaceSource(ctx, o.change.Record.IsolationDomainID, o.change.Record.WorkspaceID, o.change.Record.ID)
	if err != nil {
		return err
	}
	if !entry.Ready || entry.Installation.Record.ArchiveDigest != o.change.Record.ArchiveDigest || entry.Installation.Record.StorageProfileDigest != o.change.Record.StorageProfileDigest {
		return workspace.ErrSourceConflict
	}
	o.change.Record = entry.Installation.Record
	if o.operation != "verify" {
		return repo.ChangeWorkspaceSourceGrant(ctx, o.change)
	}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, ResponseHeaderTimeout: 15 * time.Second}
	defer transport.CloseIdleConnections()
	base, err := s3store.New(s3store.Config{Endpoint: o.endpoint, Bucket: o.bucket, AddressingStyle: s3store.PathStyle, AllowHTTPForLoopback: true, HTTPClient: &http.Client{Transport: transport, Timeout: 30 * time.Second}})
	if err != nil {
		return workspace.ErrSourceInvalid
	}
	objects, err := s3store.NewWorkspaceSourceStore(base)
	if err != nil {
		return err
	}
	use := workspace.SourceUse{Contract: workspace.SourceUseV1, Record: o.change.Record, RevisionID: o.change.RevisionID, OperationID: o.operationID, GrantGeneration: o.change.Generation, ActorID: o.change.ActorID, CorrelationID: o.change.CorrelationID}
	snapshot, err := workspace.ReadSource(ctx, repo, objects, repo, use)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		SchemaVersion   string                 `json:"schemaVersion"`
		Record          workspace.SourceRecord `json:"source"`
		RevisionID      string                 `json:"revisionId"`
		GrantGeneration int64                  `json:"grantGeneration"`
		Materialization workspace.Receipt      `json:"materialization"`
	}{"dataground.workspace-source-verification/v1", use.Record, use.RevisionID, use.GrantGeneration, snapshot.Receipt()})
}
