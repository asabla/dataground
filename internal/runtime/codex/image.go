package codex

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

// Generation results contain inline base64 bytes in the pinned protocol. The
// saved path and revised prompt are not needed to retain those bytes.
func (client *Client) handleCompletedImage(item json.RawMessage) {
	var native struct {
		ID     string  `json:"id"`
		Status string  `json:"status"`
		Result *string `json:"result"`
	}
	reject := func() { client.fail(fmt.Errorf("%w: completed generated image is invalid", dgruntime.ErrProtocol)) }
	if !utf8.Valid(item) || json.Unmarshal(item, &native) != nil || native.ID == "" || len(native.ID) > 1024 || native.Status != "completed" || native.Result == nil {
		reject()
		return
	}
	snapshot := dgruntime.CompletedImage{Data: strings.TrimSpace(*native.Result)}
	content, err := dgruntime.ParseCompletedImage(snapshot.Payload())
	if err != nil {
		reject()
		return
	}
	digest := sha256.Sum256(content.Bytes)
	if previous, found := client.completedImages[native.ID]; found {
		if previous != digest {
			reject()
		}
		return
	}
	// Bound the total image content admitted by one turn, including queued output.
	if len(client.completedImages) >= 256 || client.completedImageBytes+len(content.Bytes) > 16<<20 {
		reject()
		return
	}
	client.completedImages[native.ID] = digest
	client.completedImageBytes += len(content.Bytes)
	client.emit(dgruntime.ImageCompletedEvent, snapshot.Payload())
}
