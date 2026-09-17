package domain

import (
	"encoding/json"
	"errors"
)

var ErrUsageInvalid = errors.New("runtime usage snapshot is invalid")

// MaximumUsageTokens preserves exact integer counts in JSON clients.
const MaximumUsageTokens = 1<<53 - 1

// ParseUsageSnapshot accepts the closed runtime payload. Counts are cumulative
// reported snapshots, not deltas or billing amounts. A later snapshot may
// correct an earlier estimate, so arithmetic and monotonicity are not inferred.
func ParseUsageSnapshot(encoded []byte) (Usage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) != nil || len(fields) != 3 {
		return Usage{}, ErrUsageInvalid
	}
	var result Usage
	for name, target := range map[string]*int{"inputTokens": &result.InputTokens, "outputTokens": &result.OutputTokens, "totalTokens": &result.TotalTokens} {
		var count *int
		if json.Unmarshal(fields[name], &count) != nil || count == nil || *count < 0 || int64(*count) > MaximumUsageTokens {
			return Usage{}, ErrUsageInvalid
		}
		*target = *count
	}
	return result, nil
}

func (usage Usage) SnapshotPayload() map[string]any {
	return map[string]any{"inputTokens": usage.InputTokens, "outputTokens": usage.OutputTokens, "totalTokens": usage.TotalTokens}
}
