package canaryscan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"
)

const testCanary = "dataground-canary-v1:0123456789abcdefghijklmnopqrstuvwxyz_A-B-CD"

func TestScanFindsOnlyCommittedStructuredCanary(t *testing.T) {
	t.Parallel()

	other := "dataground-canary-v1:ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_a-b-cD"
	input := []byte("before " + other + " middle " + testCanary + " after")
	result, err := Scan(context.Background(), &singleByteReader{content: input}, int64(len(input)), commitment(testCanary))
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.InspectedBytes != int64(len(input)) || result.InspectedSHA256 != sha256.Sum256(input) || result.Candidates != 2 || result.Matches != 1 {
		t.Fatalf("Scan() result = %+v", result)
	}
}

func TestScanRejectsMalformedCandidates(t *testing.T) {
	t.Parallel()

	input := []byte("dataground-canary-v1:not valid and too short")
	result, err := Scan(context.Background(), bytes.NewReader(input), int64(len(input)), commitment(testCanary))
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.Candidates != 0 || result.Matches != 0 {
		t.Fatalf("Scan() result = %+v", result)
	}
}

func TestScanFailsClosedAtInputLimit(t *testing.T) {
	t.Parallel()

	input := []byte("one byte beyond")
	result, err := Scan(context.Background(), bytes.NewReader(input), int64(len(input)-1), commitment(testCanary))
	if !errors.Is(err, ErrInputLimit) {
		t.Fatalf("Scan() error = %v, want ErrInputLimit", err)
	}
	if result.InspectedBytes != int64(len(input)) {
		t.Fatalf("Scan() inspected bytes = %d, want %d", result.InspectedBytes, len(input))
	}
	if result.InspectedSHA256 != sha256.Sum256(input) {
		t.Fatalf("Scan() inspected digest = %x", result.InspectedSHA256)
	}
}

func TestScanPreservesCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Scan(ctx, bytes.NewReader(nil), 1, commitment(testCanary))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Scan() error = %v, want context.Canceled", err)
	}
}

func TestScanRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		input      io.Reader
		maxBytes   int64
		commitment string
		want       error
	}{
		{name: "nil input", input: nil, maxBytes: 1, commitment: commitment(testCanary), want: ErrInputLimit},
		{name: "zero bound", input: bytes.NewReader(nil), maxBytes: 0, commitment: commitment(testCanary), want: ErrInputLimit},
		{name: "uppercase digest", input: bytes.NewReader(nil), maxBytes: 1, commitment: "sha256:" + stringsOf("A", 64), want: ErrInvalidCommitment},
		{name: "short digest", input: bytes.NewReader(nil), maxBytes: 1, commitment: "sha256:00", want: ErrInvalidCommitment},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Scan(context.Background(), test.input, test.maxBytes, test.commitment)
			if !errors.Is(err, test.want) {
				t.Fatalf("Scan() error = %v, want %v", err, test.want)
			}
		})
	}
}

func commitment(canary string) string {
	sum := sha256.Sum256([]byte(canary))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func stringsOf(value string, count int) string {
	var result bytes.Buffer
	for range count {
		result.WriteString(value)
	}
	return result.String()
}

type singleByteReader struct {
	content []byte
	offset  int
}

func (reader *singleByteReader) Read(target []byte) (int, error) {
	if reader.offset == len(reader.content) {
		return 0, io.EOF
	}
	target[0] = reader.content[reader.offset]
	reader.offset++
	return 1, nil
}
