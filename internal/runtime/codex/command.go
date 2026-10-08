package codex

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func (client *Client) handleCompletedCommand(item json.RawMessage) {
	var native struct {
		ID       string  `json:"id"`
		Output   *string `json:"aggregatedOutput"`
		Status   string  `json:"status"`
		ExitCode *int32  `json:"exitCode"`
	}
	if !utf8.Valid(item) || json.Unmarshal(item, &native) != nil || native.ID == "" || len(native.ID) > 1024 {
		client.fail(fmt.Errorf("%w: completed command is invalid", dgruntime.ErrProtocol))
		return
	}
	status := native.Status
	if status == "declined" {
		status = "denied"
	}
	command := dgruntime.CompletedCommand{Status: status, ExitCode: native.ExitCode}
	if native.Output != nil {
		command.Text = *native.Output
	}
	if _, err := dgruntime.ParseCompletedCommand(command.Payload()); err != nil {
		client.fail(fmt.Errorf("%w: completed command snapshot is invalid", dgruntime.ErrProtocol))
		return
	}
	// Only normalized values and output availability affect replay. Command,
	// cwd, process IDs and other native routing metadata are never published.
	encoded, _ := json.Marshal(struct {
		Command   dgruntime.CompletedCommand
		Available bool
	}{command, native.Output != nil})
	digest := sha256.Sum256(encoded)
	if previous, found := client.completedCommands[native.ID]; found {
		if previous != digest {
			client.fail(fmt.Errorf("%w: completed command changed", dgruntime.ErrProtocol))
		}
		return
	}
	if len(client.completedCommands) >= maximumCompletedMessages {
		client.fail(fmt.Errorf("%w: completed command limit exceeded", dgruntime.ErrProtocol))
		return
	}
	client.completedCommands[native.ID] = digest
	client.emit("activity.process.completed", map[string]any{"kind": "command", "status": status, "exitCode": command.Payload()["exitCode"], "outputAvailable": native.Output != nil})
	if native.Output != nil {
		client.emit(dgruntime.CommandCompletedEvent, command.Payload())
	}
}
