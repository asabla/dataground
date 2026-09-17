package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/asabla/dataground/internal/execution/runtimeevidence"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, runtimeevidence.RunDevelopmentProvider); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type providerOperation func(context.Context, string, runtimeevidence.DevelopmentProviderConfig) (runtimeevidence.DevelopmentProviderReceipt, error)

func run(ctx context.Context, args []string, output io.Writer, operation providerOperation) error {
	if len(args) == 0 || (args[0] != "install" && args[0] != "inspect") {
		return errors.New("provider action must be install or inspect")
	}
	flags := flag.NewFlagSet("dataground-development-provider", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var config runtimeevidence.DevelopmentProviderConfig
	for _, input := range []struct {
		name  string
		value *string
	}{
		{"isolation-domain", &config.Gateway.IsolationDomainID}, {"service", &config.Gateway.ServiceID}, {"revision", &config.Gateway.RevisionID}, {"run-id", &config.Gateway.RunID}, {"repository-root", &config.Gateway.RepositoryRoot}, {"workspace-root", &config.Gateway.WorkspaceRoot}, {"docker-binary", &config.Gateway.DockerBinary}, {"supervisor-local-image-id", &config.Gateway.SupervisorLocalImageID}, {"gateway-config-sha256", &config.Gateway.GatewayConfigSHA256},
		{"openshell-binary", &config.OpenShellBinary}, {"credential-directory", &config.CredentialDirectory}, {"actor", &config.ActorID}, {"correlation-id", &config.CorrelationID},
	} {
		seen := false
		flags.Func(input.name, "exact deployment pin", func(value string) error {
			if seen || value == "" {
				return errors.New("invalid flag")
			}
			seen = true
			*input.value = value
			return nil
		})
	}
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || flags.NFlag() < 12 || config.Gateway.IsolationDomainID == "" || config.Gateway.ServiceID == "" || config.Gateway.RevisionID == "" || config.Gateway.RunID == "" || config.Gateway.RepositoryRoot == "" || config.Gateway.WorkspaceRoot == "" || config.Gateway.DockerBinary == "" || config.Gateway.GatewayConfigSHA256 == "" || config.OpenShellBinary == "" || config.CredentialDirectory == "" || config.ActorID == "" || config.CorrelationID == "" {
		return errors.New("complete unique deployment pins are required")
	}
	receipt, err := operation(ctx, args[0], config)
	if err != nil {
		return err
	}
	content, err := json.Marshal(receipt)
	if err != nil {
		return errors.New("provider result could not be encoded")
	}
	content = append(content, '\n')
	n, err := output.Write(content)
	if err != nil || n != len(content) {
		return errors.New("provider result could not be written; inspect the exact deployment")
	}
	return nil
}
