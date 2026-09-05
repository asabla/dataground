package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPolicyFileAcceptsBoundedRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.cedar")
	content := []byte("permit(principal, action, resource);")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPolicyFile(path)
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("policy content = %q, want %q", got, content)
	}
}

func TestReadPolicyFileRejectsUnsafeInputs(t *testing.T) {
	directory := t.TempDir()
	empty := filepath.Join(directory, "empty.cedar")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(directory, "oversized.cedar")
	if err := os.WriteFile(oversized, make([]byte, maximumPolicyFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"directory": directory,
		"empty":     empty,
		"oversized": oversized,
	} {
		name, path := name, path
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := readPolicyFile(path); err == nil {
				t.Fatal("unsafe policy input was accepted")
			}
		})
	}
}

func TestPolicyInstallRejectsAmbiguousInteractiveContract(t *testing.T) {
	if err := run(context.Background(), []string{"--approval-capable", "--question-capable"}); err == nil || !strings.Contains(err.Error(), "select only one interactive policy contract") {
		t.Fatalf("ambiguous policy selection: %v", err)
	}
}
