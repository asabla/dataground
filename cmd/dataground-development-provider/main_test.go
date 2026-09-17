package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/execution/runtimeevidence"
)

func providerArguments() []string {
	return []string{"install", "--isolation-domain", "iso_00000000000000000001", "--service", "svc_00000000000000000001", "--revision", "rev_00000000000000000001", "--run-id", strings.Repeat("a", 32), "--repository-root", "/repository", "--workspace-root", "/private/deployment", "--docker-binary", "/usr/bin/docker", "--gateway-config-sha256", strings.Repeat("b", 64), "--openshell-binary", "/usr/bin/openshell", "--credential-directory", "/private/source", "--actor", "operator", "--correlation-id", "cor_00000000000000000001"}
}
func TestProviderCommandRequiresUniqueCompletePins(t *testing.T) {
	for _, args := range [][]string{nil, {"rotate"}, providerArguments()[:len(providerArguments())-2], append(providerArguments(), "--actor", "changed"), append(providerArguments(), "unexpected")} {
		if err := run(context.Background(), args, io.Discard, func(context.Context, string, runtimeevidence.DevelopmentProviderConfig) (runtimeevidence.DevelopmentProviderReceipt, error) {
			t.Fatal("invalid flags reached effect")
			return runtimeevidence.DevelopmentProviderReceipt{}, nil
		}); err == nil {
			t.Fatal("invalid arguments accepted")
		}
	}
}
func TestProviderCommandWritesSafeReceiptAndReportsOutputFailure(t *testing.T) {
	operation := func(_ context.Context, action string, config runtimeevidence.DevelopmentProviderConfig) (runtimeevidence.DevelopmentProviderReceipt, error) {
		if action != "install" || config.Gateway.ServiceID != "svc_00000000000000000001" || config.CredentialDirectory != "/private/source" || config.ActorID != "operator" {
			t.Fatal("pins changed")
		}
		return runtimeevidence.DevelopmentProviderReceipt{State: "configured"}, nil
	}
	var output bytes.Buffer
	if err := run(context.Background(), providerArguments(), &output, operation); err != nil || !strings.Contains(output.String(), `"state":"configured"`) || strings.Contains(output.String(), "/private") {
		t.Fatal(output.String(), err)
	}
	if err := run(context.Background(), providerArguments(), shortWriter{}, operation); err == nil {
		t.Fatal("short output write accepted")
	}
	output.Reset()
	err := run(context.Background(), providerArguments(), &output, func(context.Context, string, runtimeevidence.DevelopmentProviderConfig) (runtimeevidence.DevelopmentProviderReceipt, error) {
		return runtimeevidence.DevelopmentProviderReceipt{}, runtimeevidence.ErrDevelopmentProvider
	})
	if !errors.Is(err, runtimeevidence.ErrDevelopmentProvider) || output.Len() != 0 {
		t.Fatal("failed effect emitted receipt")
	}
}

type shortWriter struct{}

func (shortWriter) Write(value []byte) (int, error) { return len(value) - 1, nil }
