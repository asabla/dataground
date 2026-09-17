package codex

import (
	"encoding/json"
	"testing"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestLateUsageCannotMutateTerminalTurn(t *testing.T) {
	terminal := make(chan struct{})
	close(terminal)
	client := &Client{terminalDone: terminal, events: make(chan dgruntime.Event, 1), terminalErr: dgruntime.ErrTurnFailed}
	// Even malformed late input must leave the already chosen result alone.
	client.handleTokenUsage(wireMessage{Params: json.RawMessage(`{"private":"late invalid usage"}`)})
	if len(client.events) != 0 || client.err != nil || client.terminalErr != dgruntime.ErrTurnFailed {
		t.Fatal("late usage mutated terminal state")
	}
}
