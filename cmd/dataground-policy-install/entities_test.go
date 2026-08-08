package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadEntityFileRequiresOwnerOnlyDirectRegularFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	valid := filepath.Join(directory, "entities.json")
	if err := os.WriteFile(valid, []byte("[{}]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if content, err := readEntityFile(valid); err != nil || string(content) != "[{}]" {
		t.Fatalf("read owner-only entity file = %q, %v", content, err)
	}

	groupReadable := filepath.Join(directory, "group-readable.json")
	if err := os.WriteFile(groupReadable, []byte("[{}]"), 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "entities-link.json")
	if err := os.Symlink(valid, link); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"group readable": groupReadable,
		"symbolic link":  link,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := readEntityFile(path); err == nil {
				t.Fatal("unsafe entity input was accepted")
			}
		})
	}
}
