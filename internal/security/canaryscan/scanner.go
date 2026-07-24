package canaryscan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

const (
	Prefix            = "dataground-canary-v1:"
	EntropyTextLength = 43
	PlaintextLength   = len(Prefix) + EntropyTextLength
)

var (
	ErrInvalidCommitment = errors.New("invalid canary commitment")
	ErrInputLimit        = errors.New("canary scan input limit exceeded")
)

// result contains bounded scan metrics and its private inspected-input digest.
type result struct {
	InspectedBytes  int64
	inspectedSHA256 [sha256.Size]byte
	Candidates      int64
	Matches         int64
}

// scan searches a bounded stream for structured canaries matching commitment.
// Candidate plaintext is kept only in the rolling buffer and is never returned.
func scan(ctx context.Context, input io.Reader, maxBytes int64, commitment string) (value result, err error) {
	target, err := parseCommitment(commitment)
	if err != nil {
		return result{}, err
	}
	if input == nil || maxBytes <= 0 {
		return result{}, ErrInputLimit
	}

	limited := &io.LimitedReader{R: input, N: maxBytes + 1}
	inputHash := sha256.New()
	defer func() {
		copy(value.inspectedSHA256[:], inputHash.Sum(nil))
	}()
	chunk := make([]byte, 64*1024)
	window := make([]byte, 0, len(chunk)+PlaintextLength-1)

	for {
		if err := ctx.Err(); err != nil {
			return value, err
		}
		read, readErr := limited.Read(chunk)
		if read > 0 {
			_, _ = inputHash.Write(chunk[:read])
			value.InspectedBytes += int64(read)
			if value.InspectedBytes > maxBytes {
				return value, ErrInputLimit
			}
			window = append(window, chunk[:read]...)
			window = inspectCompleteCandidates(window, target, &value)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return value, nil
			}
			return value, readErr
		}
		if read == 0 {
			return value, io.ErrNoProgress
		}
	}
}

func inspectCompleteCandidates(window []byte, target [sha256.Size]byte, value *result) []byte {
	completeStarts := len(window) - PlaintextLength + 1
	if completeStarts <= 0 {
		return window
	}

	searchAt := 0
	for searchAt < completeStarts {
		offset := bytes.Index(window[searchAt:], []byte(Prefix))
		if offset < 0 {
			break
		}
		start := searchAt + offset
		if start >= completeStarts {
			break
		}
		candidate := window[start : start+PlaintextLength]
		if validEntropyText(candidate[len(Prefix):]) {
			value.Candidates++
			if sha256.Sum256(candidate) == target {
				value.Matches++
			}
		}
		searchAt = start + 1
	}

	return append(window[:0], window[completeStarts:]...)
}

func parseCommitment(value string) ([sha256.Size]byte, error) {
	var commitment [sha256.Size]byte
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+sha256.Size*2 {
		return commitment, ErrInvalidCommitment
	}
	decoded, err := hex.DecodeString(value[len(prefix):])
	if err != nil || strings.ToLower(value) != value {
		return commitment, ErrInvalidCommitment
	}
	copy(commitment[:], decoded)
	return commitment, nil
}

func validEntropyText(value []byte) bool {
	if len(value) != EntropyTextLength {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}
