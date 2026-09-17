package runtime

import "unicode/utf8"

const MessageCompletedEvent = "output.message.completed"
const MaximumMessageTextBytes = 64 << 10

// CompletedMessage is an authoritative message snapshot, not an accumulation
// of streamed previews. Unspecified phase preserves a runtime's legacy answer
// semantics without classifying its text as explicitly final.
type CompletedMessage struct {
	Text  string
	Phase string
}

func (message CompletedMessage) Payload() map[string]any {
	return map[string]any{"text": message.Text, "phase": message.Phase}
}

func ParseCompletedMessage(payload map[string]any) (CompletedMessage, error) {
	text, textOK := payload["text"].(string)
	phase, phaseOK := payload["phase"].(string)
	if len(payload) != 2 || !textOK || !phaseOK || len(text) > MaximumMessageTextBytes || !utf8.ValidString(text) {
		return CompletedMessage{}, ErrProtocol
	}
	switch phase {
	case "commentary", "final", "unspecified":
		return CompletedMessage{Text: text, Phase: phase}, nil
	default:
		return CompletedMessage{}, ErrProtocol
	}
}
