package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/asabla/dataground/internal/execution/runtimeevidence"
)

func main() {
	var config runtimeevidence.LauncherConfig
	flag.StringVar(
		&config.RepositoryRoot,
		"repository-root",
		".",
		"repository root containing the checked OpenShell profile",
	)
	flag.StringVar(
		&config.WorkspaceRoot,
		"workspace-root",
		"",
		"pre-existing owner-only mode-0700 runtime workspace root",
	)
	flag.StringVar(
		&config.CredentialDirectory,
		"credential-directory",
		"",
		"fresh owner-only runtime credential bundle",
	)
	flag.StringVar(
		&config.OpenShellBinary,
		"openshell-binary",
		"openshell",
		"OpenShell CLI executable",
	)
	flag.StringVar(
		&config.DockerBinary,
		"docker-binary",
		"docker",
		"Docker CLI executable",
	)
	flag.StringVar(
		&config.Provenance.SourceCommit,
		"source-commit",
		"",
		"exact source commit under test",
	)
	flag.Int64Var(
		&config.Provenance.WorkflowRunID,
		"workflow-run-id",
		0,
		"workflow run producing the evidence",
	)
	flag.Parse()
	if flag.NArg() != 0 ||
		config.WorkspaceRoot == "" ||
		config.CredentialDirectory == "" ||
		config.Provenance.SourceCommit == "" ||
		config.Provenance.WorkflowRunID <= 0 {
		fail()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := runtimeevidence.Launch(ctx, config)
	if err != nil {
		fail()
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(result); err != nil {
		fail()
	}
}

func fail() {
	_, _ = fmt.Fprintln(os.Stderr, "runtime conformance run failed")
	os.Exit(1)
}
