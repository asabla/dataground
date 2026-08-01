package persistence

import (
	"errors"
	"testing"
)

func TestAuthorizationExportCursorRoundTrip(t *testing.T) {
	t.Parallel()

	want := authorizationExportCursor{
		initialized:       true,
		apiAfter:          11,
		apiThrough:        19,
		invocationAfter:   23,
		invocationThrough: 29,
	}
	encoded, err := encodeAuthorizationExportCursor(want)
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	got, err := decodeAuthorizationExportCursor(encoded)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if got != want {
		t.Fatalf("decoded cursor = %#v, want %#v", got, want)
	}
}

func TestAuthorizationExportCursorRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"unknown version": "v2.AQ",
		"invalid encoding": "v1.***",
		"wrong length":     "v1.AQ",
	}
	for name, value := range tests {
		name, value := name, value
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := decodeAuthorizationExportCursor(value); !errors.Is(err, ErrAuthorizationExportInvalid) {
				t.Fatalf("decode error = %v, want invalid", err)
			}
		})
	}
	if _, err := encodeAuthorizationExportCursor(authorizationExportCursor{
		initialized:       true,
		apiAfter:          2,
		apiThrough:        1,
		invocationAfter:   0,
		invocationThrough: 0,
	}); !errors.Is(err, ErrAuthorizationExportInvalid) {
		t.Fatalf("encode error = %v, want invalid", err)
	}
}
