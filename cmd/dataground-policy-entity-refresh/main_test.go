package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestParseArgumentsSeparatesPublicationAndActivation(t *testing.T) {
	t.Parallel()
	base := []string{
		"-isolation-domain", "iso_00000000000000000001",
		"-service", "svc_00000000000000000001",
		"-revision", "rev_00000000000000000001",
		"-generation", "1", "-actor", "operator",
		"-reason", "reviewed refresh", "-correlation-id", "cor_00000000000000000001",
	}
	publish, err := parseArguments(append(append([]string{}, base...),
		"-operation", "publish", "-entity-file", "entities.json"))
	if err != nil || publish.operation != "publish" || publish.entityFile != "entities.json" {
		t.Fatalf("publish request = %#v, %v", publish, err)
	}
	digest := "sha256:" + string(bytes.Repeat([]byte{'1'}, 64))
	activate, err := parseArguments(append(append([]string{}, base...),
		"-operation", "activate", "-policy-digest", digest))
	if err != nil || activate.operation != "activate" || len(activate.installedDigest) != 32 {
		t.Fatalf("activation request = %#v, %v", activate, err)
	}
	invalid := [][]string{
		append(append([]string{}, base...), "-operation", "publish"),
		append(append([]string{}, base...), "-operation", "activate", "-entity-file", "entities.json", "-policy-digest", digest),
		append(append([]string{}, base...), "-operation", "activate", "-policy-digest", "sha256:bad"),
	}
	for _, arguments := range invalid {
		if _, err := parseArguments(arguments); err == nil {
			t.Fatalf("invalid arguments accepted: %v", arguments)
		}
	}
}

func TestReadEntitySnapshotRequiresStableOwnerOnlyFile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "entities.json")
	if err := os.WriteFile(path, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := readEntitySnapshot(path)
	if err != nil || string(content) != "[]" {
		t.Fatalf("entity content = %q, %v", content, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readEntitySnapshot(path); err == nil {
		t.Fatal("group-readable entity snapshot was accepted")
	}
}
