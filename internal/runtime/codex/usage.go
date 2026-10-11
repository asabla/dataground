package codex

import (
	"encoding/json"
	"fmt"

	"github.com/asabla/dataground/internal/domain"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

type nativeUsageBreakdown struct {
	Input     *int `json:"inputTokens"`
	Output    *int `json:"outputTokens"`
	Total     *int `json:"totalTokens"`
	Cached    *int `json:"cachedInputTokens"`
	Reasoning *int `json:"reasoningOutputTokens"`
}

func (usage nativeUsageBreakdown) valid() bool {
	for _, count := range []*int{usage.Input, usage.Output, usage.Total, usage.Cached, usage.Reasoning} {
		if count == nil || *count < 0 || int64(*count) > domain.MaximumUsageTokens {
			return false
		}
	}
	return true
}

func (client *Client) handleTokenUsage(message wireMessage) {
	// A terminal turn has frozen its observable result. Late native snapshots
	// cannot update its journal or turn a completed result into a protocol error.
	select {
	case <-client.terminalDone:
		return
	default:
	}
	var params struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Usage    struct {
			Total nativeUsageBreakdown `json:"total"`
			Last  nativeUsageBreakdown `json:"last"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(message.Params, &params) != nil || !client.matchesActiveTurn(params.ThreadID, params.TurnID) || !params.Usage.Total.valid() || !params.Usage.Last.valid() {
		client.fail(fmt.Errorf("%w: runtime usage snapshot is invalid", dgruntime.ErrProtocol))
		return
	}
	// Native totals belong to the thread. A single-use client maps that thread
	// to one invocation; a sequential session must retain session scope.
	usage := domain.Usage{InputTokens: *params.Usage.Total.Input, OutputTokens: *params.Usage.Total.Output, TotalTokens: *params.Usage.Total.Total}
	eventType := "usage.recorded"
	if client.sequential {
		eventType = "usage.session.recorded"
	}
	client.emit(eventType, usage.SnapshotPayload())
}
