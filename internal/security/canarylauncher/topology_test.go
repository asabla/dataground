package canarylauncher

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTopologyWorkspaceFreezesAndRemovesExactInputs(t *testing.T) {
	root := topologyRoot(t)
	compose := []byte("services: {}\n")
	gateway := []byte("[openshell]\nversion = 1\n")
	workspace, err := openTopologyWorkspace(root, testRunID, compose, gateway)
	if err != nil {
		t.Fatal(err)
	}
	actualCompose, err := os.ReadFile(workspace.composePath)
	if err != nil || string(actualCompose) != string(compose) {
		t.Fatalf("compose copy = %q, %v", actualCompose, err)
	}
	actualGateway, err := os.ReadFile(workspace.gatewayPath)
	if err != nil || string(actualGateway) != string(gateway) {
		t.Fatalf("gateway copy = %q, %v", actualGateway, err)
	}
	if err := os.WriteFile(filepath.Join(workspace.statePath, "gateway.db"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		workspace.composePath,
		workspace.gatewayPath,
		filepath.Join(workspace.jwtPath, "signing.pem"),
		filepath.Join(workspace.jwtPath, "public.pem"),
		filepath.Join(workspace.jwtPath, "kid"),
	} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("topology file mode = %v, %v", info, err)
		}
	}
	privatePEM, err := os.ReadFile(filepath.Join(workspace.jwtPath, "signing.pem"))
	if err != nil {
		t.Fatal(err)
	}
	publicPEM, err := os.ReadFile(filepath.Join(workspace.jwtPath, "public.pem"))
	if err != nil {
		t.Fatal(err)
	}
	privateBlock, privateRest := pem.Decode(privatePEM)
	publicBlock, publicRest := pem.Decode(publicPEM)
	if privateBlock == nil || len(privateRest) != 0 || privateBlock.Type != "PRIVATE KEY" ||
		publicBlock == nil || len(publicRest) != 0 || publicBlock.Type != "PUBLIC KEY" {
		t.Fatal("gateway JWT keys are not canonical PEM values")
	}
	privateValue, err := x509.ParsePKCS8PrivateKey(privateBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	publicValue, err := x509.ParsePKIXPublicKey(publicBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, privateOK := privateValue.(ed25519.PrivateKey)
	publicKey, publicOK := publicValue.(ed25519.PublicKey)
	if !privateOK || !publicOK || !bytes.Equal(privateKey.Public().(ed25519.PublicKey), publicKey) {
		t.Fatal("gateway JWT keypair does not match")
	}
	kid, err := os.ReadFile(filepath.Join(workspace.jwtPath, "kid"))
	if err != nil {
		t.Fatal(err)
	}
	if len(kid) != 33 || kid[32] != '\n' {
		t.Fatalf("gateway JWT kid length = %d", len(kid))
	}
	if _, err := hex.DecodeString(string(kid[:32])); err != nil {
		t.Fatalf("gateway JWT kid is not hexadecimal: %v", err)
	}
	if _, err := json.Marshal(workspace); !errors.Is(err, ErrSerialization) {
		t.Fatalf("MarshalJSON() error = %v", err)
	}
	if err := workspace.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Lstat(workspace.statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("gateway state survived cleanup: %v", err)
	}
	if _, err := os.Lstat(workspace.jwtPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("gateway JWT keys survived cleanup: %v", err)
	}
	if _, err := os.Lstat(workspace.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("topology directory survived cleanup: %v", err)
	}
	if err := workspace.Cleanup(context.Background()); err != nil {
		t.Fatalf("idempotent Cleanup() error = %v", err)
	}
}

func TestTopologyWorkspaceRejectsReplacementDuringCleanup(t *testing.T) {
	root := topologyRoot(t)
	workspace, err := openTopologyWorkspace(
		root,
		testRunID,
		[]byte("services: {}\n"),
		[]byte("[openshell]\nversion = 1\n"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(workspace.composePath); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("do not remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, workspace.composePath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := workspace.Cleanup(context.Background()); !errors.Is(err, ErrLaunch) {
		t.Fatalf("replacement cleanup error = %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "do not remove" {
		t.Fatalf("replacement target changed = %q, %v", content, err)
	}
}

func topologyRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
