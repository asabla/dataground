package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/workspace"
)

func verifyLiveWorkspaceChanges(t *testing.T, ctx context.Context, repo *persistence.Repository, reader *artifact.Reader, public *liveWorkspaceAPI, invocation domain.Invocation, source workspace.SourceRecord, planDigest string, baseArchive, expected []byte) {
	t.Helper()
	entry, err := repo.GetWorkspaceChangeExport(ctx, source.IsolationDomainID, invocation.OperationID)
	if err != nil || !entry.Ready || entry.Record.Source != source || entry.Record.InvocationID != invocation.Metadata.ID || entry.Record.PlanDigest != planDigest {
		t.Fatal("missing exact retained workspace changes", err)
	}
	cleanup, err := repo.GetInvocationWorkspaceQuiescence(ctx, source.IsolationDomainID, invocation.OperationID)
	if err != nil || !cleanup.Confirmed || cleanup.Record.Source != source || cleanup.Record.InvocationID != entry.Record.InvocationID || cleanup.Record.ExecutionID != entry.Record.ExecutionID || cleanup.Record.EffectID != entry.Record.EffectID || cleanup.Record.PlanDigest != planDigest {
		t.Fatal("missing original native owner cleanup confirmation", err)
	}
	descriptor := entry.Record.ArtifactRecord()
	if !slices.Contains(invocation.ArtifactIDs, descriptor.ID) || !descriptor.Sensitive {
		t.Fatal("completed invocation does not expose the sensitive change artifact")
	}
	value, err := reader.Read(ctx, source.IsolationDomainID, invocation.Metadata.ID, descriptor.ID)
	if err != nil || value.Record != descriptor {
		t.Fatal("retained workspace artifact differs from its catalog", err)
	}
	defer clear(value.Bytes)
	base, err := workspace.Inspect(baseArchive, source.ArchiveDigest)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := workspace.InspectChanges(base, value.Bytes, descriptor.Digest)
	if err != nil || changes.Receipt() != entry.Record.Changes || changes.Receipt().ChangeCount < 1 {
		t.Fatal("retained changes cannot reconstruct the verified final tree", err)
	}
	restored, err := workspace.ApplyChanges(base, value.Bytes, descriptor.Digest)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	defer func() { clear(archive.Bytes()) }()
	if err := restored.WriteArchive(ctx, &archive); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{"source.txt": false, "result.txt": false}
	stream := tar.NewReader(bytes.NewReader(archive.Bytes()))
	for {
		header, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, wanted := found[header.Name]; !wanted {
			continue
		}
		content, err := io.ReadAll(io.LimitReader(stream, int64(len(expected))+1))
		matches := err == nil && header.Typeflag == tar.TypeReg && bytes.Equal(content, expected)
		clear(content)
		if !matches {
			t.Fatal("restored source or output bytes differ")
		}
		found[header.Name] = true
	}
	if !found["source.txt"] || !found["result.txt"] {
		t.Fatal("restored workspace is missing the source or its output")
	}
	if public != nil {
		public.verifyArtifact(invocation.Metadata.ID, descriptor.ID, value.Bytes)
	}
	if _, err := repo.GetWorkspaceChangeExport(ctx, identity.New("iso"), invocation.OperationID); !errors.Is(err, workspace.ErrChangeExportMissing) {
		t.Fatal("cross-domain change export lookup was not denied", err)
	}
}
