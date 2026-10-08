package runtime

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const CommandCompletedEvent = "output.command.completed"
const CommandArtifactEvent = "output.command.artifact"
const MaximumCommandOutputBytes = 1 << 20

// CompletedCommand carries the exact final stdout/stderr snapshot to the
// governed sink. It must never be written directly to the public journal.
type CompletedCommand struct {
	Text     string `json:"text"`
	Status   string `json:"status"`
	ExitCode *int32 `json:"exitCode"`
}

func (command CompletedCommand) Payload() map[string]any {
	var exit any
	if command.ExitCode != nil {
		exit = *command.ExitCode
	}
	return map[string]any{"text": command.Text, "status": command.Status, "exitCode": exit}
}

func validCommandStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "denied"
}

func ParseCompletedCommand(payload map[string]any) (CompletedCommand, error) {
	var command CompletedCommand
	text, ok := payload["text"].(string)
	encoded, err := json.Marshal(payload)
	if err != nil || len(payload) != 3 || !ok || !utf8.ValidString(text) || len(text) > MaximumCommandOutputBytes || json.Unmarshal(encoded, &command) != nil || !validCommandStatus(command.Status) {
		return CompletedCommand{}, ErrProtocol
	}
	if _, ok := payload["exitCode"]; !ok {
		return CompletedCommand{}, ErrProtocol
	}
	return command, nil
}

// CommandArtifact refers to a sensitive snapshot. The journal separately
// verifies its exact invocation, operation, effect and source-sequence binding.
type CommandArtifact struct {
	ArtifactID string `json:"artifactId"`
	Digest     string `json:"digest"`
	SizeBytes  int64  `json:"sizeBytes"`
	Status     string `json:"status"`
	ExitCode   *int32 `json:"exitCode"`
	Preview    string `json:"preview"`
}

func (command CommandArtifact) Payload() map[string]any {
	var exit any
	if command.ExitCode != nil {
		exit = *command.ExitCode
	}
	return map[string]any{"artifactId": command.ArtifactID, "digest": command.Digest, "sizeBytes": command.SizeBytes, "status": command.Status, "exitCode": exit, "preview": command.Preview}
}

func ParseCommandArtifact(payload map[string]any) (CommandArtifact, error) {
	var command CommandArtifact
	for _, key := range []string{"artifactId", "digest", "status", "preview"} {
		value, ok := payload[key].(string)
		if !ok || !utf8.ValidString(value) {
			return CommandArtifact{}, ErrProtocol
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(payload) != 6 || json.Unmarshal(encoded, &command) != nil || !messageArtifactID.MatchString(command.ArtifactID) || !messageArtifactDigest.MatchString(command.Digest) || command.SizeBytes < 0 || command.SizeBytes > MaximumCommandOutputBytes || len(command.Preview) > MaximumMessagePreviewBytes || !validCommandStatus(command.Status) || strings.ContainsRune(command.Preview, 0) {
		return CommandArtifact{}, ErrProtocol
	}
	for _, key := range []string{"sizeBytes", "exitCode"} {
		if value, ok := payload[key]; !ok || (key == "sizeBytes" && value == nil) {
			return CommandArtifact{}, ErrProtocol
		}
	}
	return command, nil
}

// PostgreSQL JSONB cannot retain a NUL character. Escape it only in the bounded
// preview; the governed object retains every original snapshot byte.
func CommandPreview(text string) string {
	return MessagePreview(strings.ReplaceAll(MessagePreview(text), "\x00", `\u0000`))
}
