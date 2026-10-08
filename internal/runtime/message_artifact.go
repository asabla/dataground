package runtime

import (
	"encoding/json"
	"regexp"
	"unicode/utf8"
)

const MessageArtifactEvent = "output.message.artifact"
const MaximumMessagePreviewBytes = 1024
const MaximumInlineMessageTextBytes = 64 << 10

var messageArtifactID = regexp.MustCompile(`^art_[0-9a-z]{20,32}$`)
var messageArtifactDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

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
		!messageArtifactID.MatchString(message.ArtifactID) || !messageArtifactDigest.MatchString(message.Digest) ||
		message.SizeBytes <= 0 || message.SizeBytes > MaximumMessageTextBytes ||
		len(message.Preview) > MaximumMessagePreviewBytes || !utf8.ValidString(message.Preview) {
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

func MessagePreview(text string) string {
	if len(text) <= MaximumMessagePreviewBytes {
		return text
	}
	preview := text[:MaximumMessagePreviewBytes]
	for !utf8.ValidString(preview) {
		preview = preview[:len(preview)-1]
	}
	return preview
}
