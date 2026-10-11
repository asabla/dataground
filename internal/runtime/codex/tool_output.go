package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func (client *Client) handleCompletedTool(item json.RawMessage) {
	var native struct {
		ID           string          `json:"id"`
		Type         string          `json:"type"`
		Status       string          `json:"status"`
		Result       json.RawMessage `json:"result"`
		Error        json.RawMessage `json:"error"`
		ContentItems json.RawMessage `json:"contentItems"`
		Success      *bool           `json:"success"`
	}
	reject := func() { client.fail(fmt.Errorf("%w: completed tool output is invalid", dgruntime.ErrProtocol)) }
	if !utf8.Valid(item) || json.Unmarshal(item, &native) != nil || native.ID == "" || len(native.ID) > 1024 || (native.Status != "completed" && native.Status != "failed") {
		reject()
		return
	}
	output := dgruntime.CompletedToolOutput{SchemaVersion: dgruntime.ToolOutputSchemaV1, Status: native.Status, Content: []any{}}
	available := false
	switch native.Type {
	case "mcpToolCall":
		if presentToolJSON(native.Result) {
			var result struct {
				Content           *[]any `json:"content"`
				StructuredContent any    `json:"structuredContent"`
			}
			decoder := json.NewDecoder(bytes.NewReader(native.Result))
			decoder.UseNumber()
			if decoder.Decode(&result) != nil || result.Content == nil {
				reject()
				return
			}
			output.Content = *result.Content
			output.StructuredContent = result.StructuredContent
			available = true
		}
		if presentToolJSON(native.Error) {
			var failure struct {
				Message *string `json:"message"`
			}
			if json.Unmarshal(native.Error, &failure) != nil || failure.Message == nil || native.Status != "failed" {
				reject()
				return
			}
			output.Error = failure.Message
			available = true
		}
	case "dynamicToolCall":
		if native.Success != nil {
			if native.Status == "failed" && *native.Success {
				reject()
				return
			}
			if !*native.Success {
				output.Status = "failed"
			}
		}
		if presentToolJSON(native.ContentItems) {
			var items []struct {
				Type     string  `json:"type"`
				Text     *string `json:"text"`
				ImageURL *string `json:"imageUrl"`
			}
			if json.Unmarshal(native.ContentItems, &items) != nil || len(items) > dgruntime.MaximumToolContentItems {
				reject()
				return
			}
			for _, item := range items {
				switch item.Type {
				case "inputText":
					if item.Text == nil {
						reject()
						return
					}
					output.Content = append(output.Content, map[string]any{"type": "text", "text": *item.Text})
				case "inputImage":
					if item.ImageURL == nil {
						reject()
						return
					}
					output.Content = append(output.Content, map[string]any{"type": "image", "url": *item.ImageURL})
				default:
					reject()
					return
				}
			}
			available = true
		}
	default:
		reject()
		return
	}
	if _, err := dgruntime.ParseCompletedToolOutput(output.Payload()); err != nil {
		reject()
		return
	}
	encoded, _ := json.Marshal(struct {
		Type      string
		Output    dgruntime.CompletedToolOutput
		Available bool
	}{native.Type, output, available})
	digest := sha256.Sum256(encoded)
	key := native.ID
	if previous, found := client.completedTools[key]; found {
		if previous != digest {
			reject()
		}
		return
	}
	if len(client.completedTools) >= maximumCompletedMessages {
		reject()
		return
	}
	client.completedTools[key] = digest
	client.emit("activity.tool.completed", map[string]any{"kind": "tool", "status": output.Status, "outputAvailable": available})
	if available {
		client.emit(dgruntime.ToolCompletedEvent, output.Payload())
	}
}
func presentToolJSON(value json.RawMessage) bool {
	return len(value) > 0 && !bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}
