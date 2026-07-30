package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/asabla/dataground/internal/security/canarylauncher"
)

func main() {
	var config canarylauncher.Config
	flag.StringVar(&config.RepositoryRoot, "repository-root", ".", "repository root containing the checked OpenShell profile")
	flag.StringVar(&config.WorkspaceRoot, "workspace-root", "", "pre-existing owner-only mode-0700 verifier workspace root")
	flag.StringVar(&config.OpenShellBinary, "openshell-binary", "openshell", "OpenShell CLI executable")
	flag.StringVar(&config.DockerBinary, "docker-binary", "docker", "Docker CLI executable")
	flag.Parse()
	if flag.NArg() != 0 || config.WorkspaceRoot == "" {
		fail(canarylauncher.FailureStageConfiguration)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := canarylauncher.Run(ctx, config)
	if err != nil {
		fail(canarylauncher.StageOf(err))
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(result); err != nil {
		fail(canarylauncher.FailureStageSerialization)
	}
}

func fail(stage canarylauncher.FailureStage) {
	_, _ = fmt.Fprintf(os.Stderr, "credential evidence run failed at %s stage\n", stage)
	os.Exit(1)
}
