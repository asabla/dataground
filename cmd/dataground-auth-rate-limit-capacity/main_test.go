package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func TestReadCapacityRequestAcceptsClosedOwnerOnlyInput(t *testing.T) {
	t.Parallel()

	request, err := readCapacityRequest(context.Background(), writeCapacityRequest(t, validCapacityRequest()))
	if err != nil {
		t.Fatalf("read capacity request: %v", err)
	}
	if request.Contract != authenticationRateLimitCapacityContract ||
		request.RunID != "cap_0123456789abcdefghij" || request.DeploymentProfile != "team" ||
		request.SourceRevision != "0123456789abcdef0123456789abcdef01234567" ||
		request.DatabaseName != "dataground_capacity" || request.Window.value != time.Minute ||
		request.GlobalBurst != 100 || request.IsolationDomainBurst != 20 ||
		request.CredentialBurst != 10 || request.AttemptsPerPhase != 200 || request.Workers != 20 ||
		request.MaximumP99Latency.value != 100*time.Millisecond ||
		request.MinimumThroughputPerSecond != 50 || request.MaximumRunDuration.value != 10*time.Minute {
		t.Fatalf("capacity request = %#v", request)
	}
}

func TestReadCapacityRequestRejectsAmbiguousOrUnsafeInput(t *testing.T) {
	t.Parallel()

	valid := validCapacityRequest()
	for name, content := range map[string]string{
		"duplicate":        strings.Replace(valid, `"workers":20`, `"workers":20,"workers":21`, 1),
		"unknown":          strings.Replace(valid, `"contract":`, `"unknown":true,"contract":`, 1),
		"missing duration": strings.Replace(valid, `"maximumRunDuration":"10m",`, "", 1),
		"trailing":         valid + `{}`,
	} {
		name, content := name, content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := readCapacityRequest(context.Background(), writeCapacityRequest(t, content)); err == nil {
				t.Fatal("invalid capacity request was accepted")
			}
		})
	}

	unsafe := writeCapacityRequest(t, valid)
	if err := os.Chmod(unsafe, 0o620); err != nil {
		t.Fatal(err)
	}
	if _, err := readCapacityRequest(context.Background(), unsafe); err == nil {
		t.Fatal("group-readable capacity request was accepted")
	}

	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "capacity.json")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readCapacityRequest(context.Background(), symlink); err == nil {
		t.Fatal("capacity request symlink was accepted")
	}
}

func TestWriteCapacityEvidenceCreatesOneDurableFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "evidence.json")
	content := []byte("{\"contract\":\"test\"}\n")
	if err := validateCapacityOutputPath(path); err != nil {
		t.Fatalf("validate output path: %v", err)
	}
	if err := writeCapacityEvidence(path, content); err != nil {
		t.Fatalf("write capacity evidence: %v", err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(content) {
		t.Fatalf("stored evidence = %q", stored)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("evidence mode = %o", info.Mode().Perm())
	}
	if err := validateCapacityOutputPath(path); err == nil {
		t.Fatal("existing evidence output was accepted")
	}
	if err := writeCapacityEvidence(path, content); err == nil {
		t.Fatal("existing evidence was replaced")
	}

	unsafeDirectory := t.TempDir()
	if err := os.Chmod(unsafeDirectory, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := writeCapacityEvidence(filepath.Join(unsafeDirectory, "evidence.json"), content); err == nil {
		t.Fatal("group-writable output directory was accepted")
	}
}

func TestCapacitySourceRevisionRequiresExactCleanBuild(t *testing.T) {
	t.Parallel()

	const revision = "0123456789abcdef0123456789abcdef01234567"
	clean := []debug.BuildSetting{
		{Key: "vcs.revision", Value: revision},
		{Key: "vcs.modified", Value: "false"},
	}
	if !capacitySourceRevisionMatches(revision, clean) {
		t.Fatal("exact clean source revision was rejected")
	}
	for name, settings := range map[string][]debug.BuildSetting{
		"missing":  nil,
		"mismatch": {{Key: "vcs.revision", Value: strings.Repeat("a", 40)}, {Key: "vcs.modified", Value: "false"}},
		"modified": {{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "true"}},
	} {
		if capacitySourceRevisionMatches(revision, settings) {
			t.Fatalf("%s build metadata was accepted", name)
		}
	}
}

func writeCapacityRequest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capacity.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validCapacityRequest() string {
	return `{
		"contract":"dataground.authentication-rate-limit-capacity/v1",
		"runId":"cap_0123456789abcdefghij",
		"sourceRevision":"0123456789abcdef0123456789abcdef01234567",
		"deploymentProfile":"team",
		"databaseName":"dataground_capacity",
		"window":"1m",
		"globalBurst":100,
		"isolationDomainBurst":20,
		"credentialBurst":10,
		"attemptsPerPhase":200,
		"workers":20,
		"maximumP99Latency":"100ms",
		"minimumThroughputPerSecond":50,
		"maximumRunDuration":"10m"
	}`
}
