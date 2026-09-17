package codex

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

const maximumCompletedMessages = 4096

func (client *Client) handleCompletedMessage(item json.RawMessage) {
	var native struct {
		ID    string  `json:"id"`
		Text  *string `json:"text"`
		Phase *string `json:"phase"`
	}
	if json.Unmarshal(item, &native) != nil || native.ID == "" || len(native.ID) > 1024 || native.Text == nil {
		client.fail(fmt.Errorf("%w: completed message is invalid", dgruntime.ErrProtocol))
		return
	}
	message := dgruntime.CompletedMessage{Text: *native.Text, Phase: "unspecified"}
	if native.Phase != nil {
		switch *native.Phase {
		case "commentary":
			message.Phase = "commentary"
		case "final_answer":
			message.Phase = "final"
		default:
			client.fail(fmt.Errorf("%w: completed message phase is invalid", dgruntime.ErrProtocol))
			return
		}
	}
	if _, err := dgruntime.ParseCompletedMessage(message.Payload()); err != nil {
		client.fail(fmt.Errorf("%w: completed message exceeds the inline contract", dgruntime.ErrProtocol))
		return
	}
	// Only the single inbound reader owns this bounded map. Native identifiers
	// remain private; exact duplicate completions cannot replace a later answer.
	digest := sha256.Sum256([]byte(message.Phase + "\x00" + message.Text))
	if previous, found := client.completedMessages[native.ID]; found {
		if previous != digest {
			client.fail(fmt.Errorf("%w: completed message changed", dgruntime.ErrProtocol))
		}
		return
	}
	if len(client.completedMessages) >= maximumCompletedMessages {
		client.fail(fmt.Errorf("%w: completed message limit exceeded", dgruntime.ErrProtocol))
		return
	}
	client.completedMessages[native.ID] = digest
	client.emit(dgruntime.MessageCompletedEvent, message.Payload())
}
