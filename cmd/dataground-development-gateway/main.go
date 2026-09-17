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
	if err := run(ctx, os.Args[1:], os.Stdout, runtimeevidence.RunDevelopmentGateway); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type gatewayOperation func(context.Context, string, runtimeevidence.DevelopmentGatewayConfig) (runtimeevidence.DevelopmentGatewayReceipt, error)

func run(ctx context.Context, args []string, output io.Writer, operation gatewayOperation) error {
	if len(args) == 0 || (args[0] != "up" && args[0] != "inspect" && args[0] != "stop") {
		return errors.New("gateway action must be up, inspect, or stop")
	}
	flags := flag.NewFlagSet("dataground-development-gateway", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var config runtimeevidence.DevelopmentGatewayConfig
	for _, input := range []struct {
		name  string
		value *string
	}{
		{"isolation-domain", &config.IsolationDomainID}, {"service", &config.ServiceID}, {"revision", &config.RevisionID}, {"run-id", &config.RunID}, {"repository-root", &config.RepositoryRoot}, {"workspace-root", &config.WorkspaceRoot}, {"docker-binary", &config.DockerBinary}, {"supervisor-local-image-id", &config.SupervisorLocalImageID}, {"gateway-config-sha256", &config.GatewayConfigSHA256},
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
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || flags.NFlag() < 8 || config.IsolationDomainID == "" || config.ServiceID == "" || config.RevisionID == "" || config.RunID == "" || config.RepositoryRoot == "" || config.WorkspaceRoot == "" || config.DockerBinary == "" || config.GatewayConfigSHA256 == "" {
		return errors.New("complete unique deployment pins are required")
	}
	receipt, err := operation(ctx, args[0], config)
	if err != nil {
		return err
	}
	if json.NewEncoder(output).Encode(receipt) != nil {
		return errors.New("gateway result could not be written; inspect the exact deployment")
	}
	return nil
}
