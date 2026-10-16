// dataground-native-owner is an internal dedicated host for the checked Codex
// candidate. Installing it does not activate a worker profile or acceptance.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/asabla/dataground/internal/runtime/nativehost"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	const executable = "/usr/bin/codex"
	if len(os.Args) != 1 || nativehost.Serve(ctx, os.Stdin, os.Stdout, executable, []string{executable, "app-server"}, os.Environ()) != nil {
		fmt.Fprintln(os.Stderr, "NATIVE_OWNER_UNCONFIRMED")
		os.Exit(1)
	}
}
