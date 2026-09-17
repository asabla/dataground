//go:build linux

package runtimeevidence

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGatewayConfigurationRewriteCannotRestoreStartupIdentityWithMtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.toml")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * gatewayTimestampUncertainty)
	started := time.Now()
	if !gatewayFilePredatesStart(original, started) {
		t.Fatal("unchanged startup file rejected")
	}
	if err := os.WriteFile(path, []byte("changed!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, original.ModTime(), original.ModTime()); err != nil {
		t.Fatal(err)
	}
	changed, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed.ModTime().Equal(original.ModTime()) || gatewayFilePredatesStart(changed, started) {
		t.Fatal("restored mtime concealed a configuration rewrite")
	}
}
