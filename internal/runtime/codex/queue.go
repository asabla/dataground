package codex

import (
	"encoding/json"
	"fmt"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

const inboundByteLimit = 16 << 20
const eventByteLimit = 16 << 20

func (client *Client) reserveInbound(size int) bool {
	if size <= 0 || size > maxFrameBytes {
		return false
	}
	for {
		current := client.inboundBytes.Load()
		if int64(size) > inboundByteLimit-current {
			return false
		}
		if client.inboundBytes.CompareAndSwap(current, current+int64(size)) {
			return true
		}
	}
}

func (client *Client) handleInbound(message wireMessage) {
	// Keep the active handler within the same budget as queued native messages.
	defer client.inboundBytes.Add(-int64(message.frameBytes))
	if len(message.ID) > 0 {
		client.handleServerRequest(message)
	} else {
		client.handleNotification(message)
	}
}

func (client *Client) emit(eventType string, payload map[string]any) bool {
	if err := client.queueEvent(eventType, payload); err != nil {
		client.fail(err)
		return false
	}
	return true
}

func (client *Client) queueEvent(eventType string, payload map[string]any) error {
	client.eventMu.Lock()
	defer client.eventMu.Unlock()
	select {
	case <-client.done:
		return client.failure()
	case <-client.closed:
		return dgruntime.ErrClosed
	default:
	}
	// Only this producer writes the event channel. Its length tells us how many
	// entries the consumer has removed since the previous publication. Concurrent
	// reads can only make this accounting conservative until the next call.
	consumed := len(client.eventSizes) - len(client.events)
	if consumed < 0 {
		return fmt.Errorf("%w: event queue accounting is invalid", dgruntime.ErrProtocol)
	}
	for _, size := range client.eventSizes[:consumed] {
		client.eventBytes -= size
	}
	client.eventSizes = client.eventSizes[consumed:]
	client.stateMu.Lock()
	sequence := client.nextSequence + 1
	client.stateMu.Unlock()
	event := dgruntime.Event{Sequence: sequence, Type: eventType, Payload: payload}
	encoded, err := json.Marshal(event)
	if err != nil || len(encoded) > maxFrameBytes || len(encoded) > eventByteLimit-client.eventBytes {
		return fmt.Errorf("%w: event byte budget exhausted", dgruntime.ErrProtocol)
	}
	select {
	case client.events <- event:
		client.eventSizes = append(client.eventSizes, len(encoded))
		client.eventBytes += len(encoded)
		client.stateMu.Lock()
		client.nextSequence = sequence
		client.stateMu.Unlock()
		return nil
	case <-client.done:
		return client.failure()
	case <-client.closed:
		return dgruntime.ErrClosed
	default:
		return fmt.Errorf("%w: event buffer exhausted", dgruntime.ErrProtocol)
	}
}
