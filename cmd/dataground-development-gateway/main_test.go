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

func gatewayArguments() []string {
	return []string{"up", "--isolation-domain", "iso_00000000000000000001", "--service", "svc_00000000000000000001", "--revision", "rev_00000000000000000001", "--run-id", strings.Repeat("a", 32), "--repository-root", "/repository", "--workspace-root", "/private/deployment", "--docker-binary", "/usr/bin/docker", "--gateway-config-sha256", strings.Repeat("b", 64)}
}
func TestGatewayCommandRequiresCompleteUniquePins(t *testing.T) {
	for _, args := range [][]string{nil, {"restart"}, gatewayArguments()[:len(gatewayArguments())-2], append(gatewayArguments(), "--run-id", "different"), append(gatewayArguments(), "unexpected")} {
		if err := run(context.Background(), args, io.Discard, func(context.Context, string, runtimeevidence.DevelopmentGatewayConfig) (runtimeevidence.DevelopmentGatewayReceipt, error) {
			t.Fatal("invalid request reached deployment")
			return runtimeevidence.DevelopmentGatewayReceipt{}, nil
		}); err == nil {
			t.Fatal("invalid flags accepted")
		}
	}
}
func TestGatewayCommandWritesOnlyReceiptAndPreservesAmbiguousFailure(t *testing.T) {
	var output bytes.Buffer
	err := run(context.Background(), gatewayArguments(), &output, func(_ context.Context, action string, c runtimeevidence.DevelopmentGatewayConfig) (runtimeevidence.DevelopmentGatewayReceipt, error) {
		if action != "up" || c.WorkspaceRoot != "/private/deployment" {
			t.Fatal("pins changed")
		}
		return runtimeevidence.DevelopmentGatewayReceipt{State: "running"}, nil
	})
	if err != nil || !strings.Contains(output.String(), `"state":"running"`) || strings.Contains(output.String(), "/private") {
		t.Fatal(output.String(), err)
	}
	output.Reset()
	err = run(context.Background(), gatewayArguments(), &output, func(context.Context, string, runtimeevidence.DevelopmentGatewayConfig) (runtimeevidence.DevelopmentGatewayReceipt, error) {
		return runtimeevidence.DevelopmentGatewayReceipt{}, runtimeevidence.ErrDevelopmentGateway
	})
	if !errors.Is(err, runtimeevidence.ErrDevelopmentGateway) || output.Len() != 0 {
		t.Fatal("failed operation emitted receipt")
	}
}
