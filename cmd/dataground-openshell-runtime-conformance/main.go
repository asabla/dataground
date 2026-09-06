package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/asabla/dataground/internal/execution/runtimeevidence"
)

func main() {
	var config runtimeevidence.LauncherConfig
	var localDiagnostic bool
	var model string
	var candidateImage string
	flag.BoolVar(&localDiagnostic, "local-diagnostic", false, "run local diagnostics without producing certification evidence")
	flag.StringVar(&model, "model", "", "explicit local diagnostic model; unavailable in CI evidence mode")
	flag.StringVar(&candidateImage, "candidate-image", "", "exact experimental local image ID; only with --local-diagnostic")
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
		(localDiagnostic && (config.Provenance.WorkflowRunID != 0 || model == "")) ||
		(!localDiagnostic && (config.Provenance.WorkflowRunID <= 0 || model != "" || candidateImage != "")) {
		fail()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var result json.Marshaler
	var err error
	if localDiagnostic {
		result, err = runtimeevidence.LaunchLocalDiagnostic(ctx, runtimeevidence.LocalDiagnosticConfig{
			RepositoryRoot: config.RepositoryRoot, WorkspaceRoot: config.WorkspaceRoot, CredentialDirectory: config.CredentialDirectory,
			OpenShellBinary: config.OpenShellBinary, DockerBinary: config.DockerBinary, SourceCommit: config.Provenance.SourceCommit, Model: model,
			CandidateImage: candidateImage,
		})
	} else {
		result, err = runtimeevidence.Launch(ctx, config)
	}
	if err != nil {
		var diagnosticFailure *runtimeevidence.LocalDiagnosticError
		if localDiagnostic && errors.As(err, &diagnosticFailure) {
			_, _ = fmt.Fprintln(os.Stderr, diagnosticFailure.Error())
			os.Exit(1)
		}
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
