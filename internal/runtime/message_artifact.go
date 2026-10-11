package runtime

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

const MessageArtifactEvent = "output.message.artifact"
const MaximumMessagePreviewBytes = 1024
const MaximumInlineMessageTextBytes = 64 << 10

var runtimeArtifactID = regexp.MustCompile(`^art_[0-9a-z]{20,32}$`)
var runtimeArtifactDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// MessageArtifact is a bounded public reference to an authoritative UTF-8
// snapshot. The artifact catalog must separately prove its invocation scope.
type MessageArtifact struct {
	ArtifactID string `json:"artifactId"`
	Digest     string `json:"digest"`
	SizeBytes  int64  `json:"sizeBytes"`
	Phase      string `json:"phase"`
	Preview    string `json:"preview"`
}

func (message MessageArtifact) Payload() map[string]any {
	return map[string]any{"artifactId": message.ArtifactID, "digest": message.Digest, "sizeBytes": message.SizeBytes, "phase": message.Phase, "preview": message.Preview}
}

func ParseMessageArtifact(payload map[string]any) (MessageArtifact, error) {
	for _, key := range []string{"artifactId", "digest", "phase", "preview"} {
		value, ok := payload[key].(string)
		if !ok || !utf8.ValidString(value) {
			return MessageArtifact{}, ErrProtocol
		}
	}
	encoded, err := json.Marshal(payload)
	var message MessageArtifact
	if err != nil || len(payload) != 5 || json.Unmarshal(encoded, &message) != nil ||
		!runtimeArtifactID.MatchString(message.ArtifactID) || !runtimeArtifactDigest.MatchString(message.Digest) ||
		message.SizeBytes <= 0 || message.SizeBytes > MaximumMessageTextBytes ||
		len(message.Preview) > MaximumMessagePreviewBytes || !utf8.ValidString(message.Preview) || strings.ContainsRune(message.Preview, 0) {
		return MessageArtifact{}, ErrProtocol
	}
	for _, key := range []string{"artifactId", "digest", "sizeBytes", "phase", "preview"} {
		if value, ok := payload[key]; !ok || value == nil {
			return MessageArtifact{}, ErrProtocol
		}
	}
	switch message.Phase {
	case "commentary", "final", "unspecified":
		return message, nil
	}
	return MessageArtifact{}, ErrProtocol
}

// PostgreSQL JSONB cannot retain NUL. Escape it only in the bounded preview;
// the governed artifact retains the original bytes.
func MessagePreview(text string) string {
	return runtimeTextPreview(text, MaximumMessagePreviewBytes)
}

// Streamed deltas are display previews, not the authoritative completed text.
func MessageDeltaPreview(text string) string {
	return runtimeTextPreview(text, MaximumInlineMessageTextBytes)
}

func runtimeTextPreview(text string, maximum int) string {
	return truncateTextPreview(strings.ReplaceAll(truncateTextPreview(text, maximum), "\x00", `\u0000`), maximum)
}

func truncateTextPreview(text string, maximum int) string {
	if len(text) <= maximum {
		return text
	}
	preview := text[:maximum]
	for !utf8.ValidString(preview) {
		preview = preview[:len(preview)-1]
	}
	return preview
}
