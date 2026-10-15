package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

type sourceWriterFunc func([]byte) (int, error)

func (f sourceWriterFunc) Write(p []byte) (int, error) { return f(p) }

func TestSnapshotTransfersExactOwnedArchive(t *testing.T) {
	data, digest := sourceArchive(t, archiveEntry{name: "file", content: "binary\x00\xff", mode: 0755})
	expected := bytes.Clone(data)
	snapshot, err := Inspect(data, digest)
	if err != nil {
		t.Fatal(err)
	}
	clear(data)
	for range 2 {
		var output bytes.Buffer
		if err := snapshot.WriteArchive(context.Background(), &output); err != nil || !bytes.Equal(output.Bytes(), expected) {
			t.Fatal("transfer changed original archive")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := snapshot.WriteArchive(ctx, sourceWriterFunc(func([]byte) (int, error) { t.Fatal("write after cancellation"); return 0, nil })); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := snapshot.WriteArchive(context.Background(), sourceWriterFunc(func([]byte) (int, error) { return 0, nil })); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	snapshot.discard()
	if err := snapshot.WriteArchive(context.Background(), io.Discard); !errors.Is(err, ErrSourceInvalid) {
		t.Fatal("discarded archive remained deliverable")
	}
}
