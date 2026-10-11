package runtime

import (
	"encoding/json"
	"unicode/utf8"
)

const ToolCompletedEvent = "output.tool.completed"
const ToolArtifactEvent = "output.tool.artifact"
const ToolOutputSchemaV1 = "dataground.runtime-tool-output/v1"
const MaximumToolOutputBytes = 1 << 20
const MaximumToolContentItems = 256
const MaximumToolJSONDepth = 64
const ToolOutputPreview = "Tool output retained."

// Tool output is data, not an executable tool request or a native event frame.
// Content values remain untrusted and are only exposed through artifact reads.
type CompletedToolOutput struct {
	SchemaVersion     string  `json:"schemaVersion"`
	Status            string  `json:"status"`
	Content           []any   `json:"content"`
	StructuredContent any     `json:"structuredContent"`
	Error             *string `json:"error"`
}

func (value CompletedToolOutput) Payload() map[string]any {
	var failure any
	if value.Error != nil {
		failure = *value.Error
	}
	return map[string]any{"schemaVersion": value.SchemaVersion, "status": value.Status, "content": value.Content, "structuredContent": value.StructuredContent, "error": failure}
}
func ParseCompletedToolOutput(payload map[string]any) (CompletedToolOutput, error) {
	value := CompletedToolOutput{}
	if len(payload) != 5 || payload["schemaVersion"] != ToolOutputSchemaV1 {
		return value, ErrProtocol
	}
	status, ok := payload["status"].(string)
	if !ok || (status != "completed" && status != "failed") {
		return value, ErrProtocol
	}
	content, ok := payload["content"].([]any)
	if !ok || content == nil || len(content) > MaximumToolContentItems {
		return value, ErrProtocol
	}
	if _, ok := payload["structuredContent"]; !ok {
		return value, ErrProtocol
	}
	failure, ok := payload["error"]
	if !ok {
		return value, ErrProtocol
	}
	if failure != nil {
		text, ok := failure.(string)
		if !ok || !utf8.ValidString(text) || len(text) > MaximumToolOutputBytes {
			return value, ErrProtocol
		}
		if status != "failed" {
			return CompletedToolOutput{}, ErrProtocol
		}
		value.Error = &text
	}
	budget := MaximumToolOutputBytes
	if !validToolJSON(content, 0, &budget) || !validToolJSON(payload["structuredContent"], 0, &budget) {
		return CompletedToolOutput{}, ErrProtocol
	}
	value.SchemaVersion = ToolOutputSchemaV1
	value.Status = status
	value.Content = content
	value.StructuredContent = payload["structuredContent"]
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > MaximumToolOutputBytes {
		return CompletedToolOutput{}, ErrProtocol
	}
	return value, nil
}

// Bound traversal before encoding, including cyclic values from an adapter.
func validToolJSON(value any, depth int, budget *int) bool {
	*budget -= 1
	if depth > MaximumToolJSONDepth || *budget < 0 {
		return false
	}
	switch value := value.(type) {
	case string:
		*budget -= len(value)
		return *budget >= 0 && utf8.ValidString(value)
	case []any:
		for _, item := range value {
			if !validToolJSON(item, depth+1, budget) {
				return false
			}
		}
	case map[string]any:
		for key, item := range value {
			*budget -= len(key)
			if *budget < 0 || !utf8.ValidString(key) || !validToolJSON(item, depth+1, budget) {
				return false
			}
		}
	case json.Number:
		*budget -= len(value)
		return *budget >= 0
	case nil, bool, float64, int, int64:
		return true
	default:
		return false
	}
	return true
}

type ToolArtifact struct {
	ArtifactID   string `json:"artifactId"`
	Digest       string `json:"digest"`
	SizeBytes    int64  `json:"sizeBytes"`
	Status       string `json:"status"`
	ContentCount int    `json:"contentCount"`
	Preview      string `json:"preview"`
}

func (value ToolArtifact) Payload() map[string]any {
	return map[string]any{"artifactId": value.ArtifactID, "digest": value.Digest, "sizeBytes": value.SizeBytes, "status": value.Status, "contentCount": value.ContentCount, "preview": value.Preview}
}
func ParseToolArtifact(payload map[string]any) (ToolArtifact, error) {
	var value ToolArtifact
	if len(payload) != 6 {
		return value, ErrProtocol
	}
	for _, key := range []string{"artifactId", "digest", "status", "preview"} {
		if _, ok := payload[key].(string); !ok {
			return value, ErrProtocol
		}
	}
	for _, key := range []string{"sizeBytes", "contentCount"} {
		if v, ok := payload[key]; !ok || v == nil {
			return value, ErrProtocol
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil || json.Unmarshal(encoded, &value) != nil || !runtimeArtifactID.MatchString(value.ArtifactID) || !runtimeArtifactDigest.MatchString(value.Digest) || value.SizeBytes <= 0 || value.SizeBytes > MaximumToolOutputBytes || (value.Status != "completed" && value.Status != "failed") || value.ContentCount < 0 || value.ContentCount > MaximumToolContentItems || value.Preview != ToolOutputPreview {
		return ToolArtifact{}, ErrProtocol
	}
	return value, nil
}
