//go:build !linux || (!amd64 && !arm64)

package processowner

import (
	"context"
	"errors"
	"testing"
)

func TestUnsupportedProcessOwnerFailsClosed(t *testing.T) {
	if owner, err := New(); owner != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatal("unsupported owner admitted")
	}
	owner := &Owner{}
	if _, err := owner.Wait(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unsupported native wait admitted")
	}
	if _, err := owner.Start("/no-command", []string{"/no-command"}, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unsupported start admitted")
	}
	if err := owner.Stop(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unsupported stop admitted")
	}
}
