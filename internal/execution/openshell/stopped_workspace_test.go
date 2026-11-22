package openshell

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/execution/openshell/retentionrpc"
	"github.com/asabla/dataground/internal/workspace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type stoppedArchiveFrames struct {
	frames []*retentionrpc.ReadStoppedWorkspaceFrame
	end    error
}

func (stream *stoppedArchiveFrames) Recv() (*retentionrpc.ReadStoppedWorkspaceFrame, error) {
	if len(stream.frames) == 0 {
		return nil, stream.end
	}
	frame := proto.Clone(stream.frames[0]).(*retentionrpc.ReadStoppedWorkspaceFrame)
	stream.frames = stream.frames[1:]
	return frame, nil
}

func TestStoppedWorkspaceArchiveRequiresExactFramesAndSuccessfulStatus(t *testing.T) {
	base, _ := transferSourceFixture(t)
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "workspace/", Mode: 0700, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buffer.Bytes()
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(archive))
	header := func(size uint64, digest string) *retentionrpc.ReadStoppedWorkspaceFrame {
		return &retentionrpc.ReadStoppedWorkspaceFrame{Payload: &retentionrpc.ReadStoppedWorkspaceFrame_Header{Header: &retentionrpc.StoppedWorkspaceArchiveHeader{SizeBytes: size, Digest: digest}}}
	}
	data := func(content []byte) *retentionrpc.ReadStoppedWorkspaceFrame {
		return &retentionrpc.ReadStoppedWorkspaceFrame{Payload: &retentionrpc.ReadStoppedWorkspaceFrame_Data{Data: content}}
	}
	manifest := header(uint64(len(archive)), digest)
	body := data(archive)
	changes, err := workspace.CompareStoppedDockerArchive(base, archive, digest)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		frames []*retentionrpc.ReadStoppedWorkspaceFrame
		end    error
		valid  bool
	}{
		{name: "complete", frames: []*retentionrpc.ReadStoppedWorkspaceFrame{manifest, data(archive[:512]), data(archive[512:])}, valid: true},
		{name: "missing header"},
		{name: "data before header", frames: []*retentionrpc.ReadStoppedWorkspaceFrame{body, manifest}},
		{name: "archive exceeds bound", frames: []*retentionrpc.ReadStoppedWorkspaceFrame{header(workspace.MaximumArchiveBytes+512, digest)}},
		{name: "second header", frames: []*retentionrpc.ReadStoppedWorkspaceFrame{manifest, manifest}},
		{name: "premature EOF", frames: []*retentionrpc.ReadStoppedWorkspaceFrame{manifest, data(archive[:512])}},
		{name: "data past declared size", frames: []*retentionrpc.ReadStoppedWorkspaceFrame{manifest, body, data([]byte{1})}},
		{name: "empty data frame", frames: []*retentionrpc.ReadStoppedWorkspaceFrame{manifest, data(nil), body}},
		{name: "oversized data frame", frames: []*retentionrpc.ReadStoppedWorkspaceFrame{header(2*stoppedWorkspaceChunkBytes, digest), data(make([]byte, stoppedWorkspaceChunkBytes+1))}},
		{name: "wrong digest", frames: []*retentionrpc.ReadStoppedWorkspaceFrame{header(uint64(len(archive)), "sha256:"+strings.Repeat("0", 64)), body}},
		{name: "no successful status", frames: []*retentionrpc.ReadStoppedWorkspaceFrame{manifest, body}, end: status.Error(codes.Unavailable, "lost acknowledgement")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			end := tc.end
			if end == nil {
				end = io.EOF
			}
			result, err := compareStoppedWorkspaceStream(t.Context(), &stoppedArchiveFrames{frames: tc.frames, end: end}, base)
			if tc.valid {
				if err != nil || result == nil || result.Receipt() != changes.Receipt() {
					t.Fatal("valid frames did not produce exact changes", err)
				}
			} else if result != nil || err != ErrWorkspaceChangeExport {
				t.Fatal("unconfirmed stream produced changes", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := compareStoppedWorkspaceStream(ctx, &stoppedArchiveFrames{frames: []*retentionrpc.ReadStoppedWorkspaceFrame{manifest, body}, end: io.EOF}, base); result != nil || err != ErrWorkspaceChangeExport {
		t.Fatal("cancelled stream produced changes", err)
	}
}
