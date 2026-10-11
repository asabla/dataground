package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type sourceCatalogFixture struct {
	entry                         SourceCatalogEntry
	prepares, completes           int
	rejectPrepare, loseCompletion bool
}

func (c *sourceCatalogFixture) PrepareWorkspaceSource(_ context.Context, v SourceInstallation) (SourceCatalogEntry, error) {
	c.prepares++
	if c.rejectPrepare {
		return SourceCatalogEntry{}, ErrSourceConflict
	}
	if c.entry.Installation == (SourceInstallation{}) {
		c.entry.Installation = v
	}
	if c.entry.Installation != v {
		return SourceCatalogEntry{}, ErrSourceConflict
	}
	return c.entry, nil
}
func (c *sourceCatalogFixture) CompleteWorkspaceSource(_ context.Context, v SourceInstallation) (SourceCatalogEntry, error) {
	c.completes++
	c.entry.Ready = true
	if c.loseCompletion {
		c.loseCompletion = false
		return SourceCatalogEntry{}, ErrSourceUnavailable
	}
	return c.entry, nil
}
func (c *sourceCatalogFixture) GetWorkspaceSource(context.Context, string, string, string) (SourceCatalogEntry, error) {
	return c.entry, nil
}

type sourceObjectFixture struct {
	content                              []byte
	reads, writes                        int
	loseWrite, corruptWrite, readFailure bool
	profile                              string
}

func (o *sourceObjectFixture) WorkspaceSourceStorageDigest() string { return o.profile }
func (o *sourceObjectFixture) OpenWorkspaceSource(context.Context, string) (io.ReadCloser, error) {
	o.reads++
	if o.readFailure {
		return nil, ErrSourceUnavailable
	}
	if o.content == nil {
		return nil, ErrSourceObjectMissing
	}
	return io.NopCloser(bytes.NewReader(o.content)), nil
}
func (o *sourceObjectFixture) PutWorkspaceSourceIfAbsent(_ context.Context, _ string, data []byte, _ string) error {
	o.writes++
	if o.content != nil {
		return ErrSourceConflict
	}
	o.content = bytes.Clone(data)
	if o.corruptWrite {
		o.content[0] ^= 1
	}
	if o.loseWrite {
		return ErrSourceUnavailable
	}
	return nil
}
func sourceInstallation(t *testing.T) (SourceInstallation, []byte) {
	data, digest := sourceArchive(t, archiveEntry{name: "private-file", content: "binary\x00content"})
	snapshot, err := Inspect(data, digest)
	if err != nil {
		t.Fatal(err)
	}
	r := snapshot.Receipt()
	return SourceInstallation{Record: SourceRecord{SchemaVersion: SourceRecordV1, IsolationDomainID: "iso_" + strings.Repeat("1", 20), WorkspaceID: "wsp_" + strings.Repeat("2", 20), ID: "src_" + strings.Repeat("3", 20), ArchiveDigest: digest, StorageProfileDigest: "sha256:" + strings.Repeat("4", 64), ArchiveBytes: int64(len(data)), EntryCount: r.EntryCount, SourceBytes: r.SourceBytes}, ActorID: "operator", CorrelationID: "cor_" + strings.Repeat("5", 20)}, data
}
func TestSourceInstallationObservesLostAcknowledgementsAndExactReplay(t *testing.T) {
	v, data := sourceInstallation(t)
	c := &sourceCatalogFixture{loseCompletion: true}
	o := &sourceObjectFixture{profile: v.Record.StorageProfileDigest, loseWrite: true}
	if _, err := InstallSource(context.Background(), c, o, v, data); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatal("lost commit was not ambiguous", err)
	}
	if !c.entry.Ready || o.writes != 1 {
		t.Fatal("object and completion were not retained")
	}
	got, err := InstallSource(context.Background(), c, o, v, data)
	if err != nil || !got.Ready || got.Installation != v || o.writes != 1 {
		t.Fatal("exact retry did not observe retained state", err)
	}
	o.content = nil
	if _, err := InstallSource(context.Background(), c, o, v, data); !errors.Is(err, ErrSourceObjectMissing) || o.writes != 1 {
		t.Fatal("ready source was silently recreated", err)
	}
}
func TestSourceInstallationFailsClosedBeforeCompletion(t *testing.T) {
	for _, scenario := range []string{"scope-conflict", "profile", "digest", "count", "read-failure", "corrupt-write", "existing-conflict", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			v, data := sourceInstallation(t)
			c := &sourceCatalogFixture{}
			o := &sourceObjectFixture{profile: v.Record.StorageProfileDigest}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "scope-conflict":
				c.rejectPrepare = true
			case "profile":
				o.profile = "sha256:" + strings.Repeat("a", 64)
			case "digest":
				v.Record.ArchiveDigest = "sha256:" + strings.Repeat("b", 64)
			case "count":
				v.Record.EntryCount++
			case "read-failure":
				o.readFailure = true
			case "corrupt-write":
				o.corruptWrite = true
			case "existing-conflict":
				o.content = []byte("wrong")
			case "cancelled":
				cancel()
			}
			if _, err := InstallSource(ctx, c, o, v, data); err == nil || c.completes != 0 {
				t.Fatal("invalid source was completed", err)
			}
			if scenario != "corrupt-write" && o.writes != 0 {
				t.Fatal("failed source request wrote an object")
			}
			if (scenario == "profile" || scenario == "digest" || scenario == "count" || scenario == "cancelled") && c.prepares != 0 {
				t.Fatal("invalid input reached durable preparation")
			}
		})
	}
}
