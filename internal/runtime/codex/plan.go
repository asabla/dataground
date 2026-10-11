package codex

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

const maximumPlanUpdates = 4096

func (client *Client) handlePlanUpdated(message wireMessage) {
	select {
	case <-client.terminalDone:
		return
	default:
	}
	var native struct {
		ThreadID    string  `json:"threadId"`
		TurnID      string  `json:"turnId"`
		Explanation *string `json:"explanation"`
		Plan        *[]struct {
			Text   *string `json:"step"`
			Status string  `json:"status"`
		} `json:"plan"`
	}
	reject := func() { client.fail(fmt.Errorf("%w: plan update is invalid", dgruntime.ErrProtocol)) }
	if !utf8.Valid(message.Params) || json.Unmarshal(message.Params, &native) != nil || !client.matchesActiveTurn(native.ThreadID, native.TurnID) || native.Plan == nil || len(*native.Plan) > dgruntime.MaximumPlanSteps {
		reject()
		return
	}
	snapshot := dgruntime.PlanSnapshot{SchemaVersion: dgruntime.PlanSchemaV1, Explanation: native.Explanation, Steps: []dgruntime.PlanStep{}}
	for _, step := range *native.Plan {
		if step.Text == nil {
			reject()
			return
		}
		status := step.Status
		if status == "inProgress" {
			status = "in_progress"
		} else if status != "pending" && status != "completed" {
			reject()
			return
		}
		snapshot.Steps = append(snapshot.Steps, dgruntime.PlanStep{Text: *step.Text, Status: status})
	}
	if _, err := dgruntime.ParsePlanSnapshot(snapshot.Payload()); err != nil {
		reject()
		return
	}
	encoded, _ := json.Marshal(snapshot)
	digest := sha256.Sum256(encoded)
	// The native update has no stable item ID. Suppress only adjacent identical
	// snapshots; a return to an earlier plan after a change is a new update.
	if client.planUpdates > 0 && client.lastPlanDigest == digest {
		return
	}
	if client.planUpdates >= maximumPlanUpdates {
		reject()
		return
	}
	if client.emit(dgruntime.PlanUpdatedEvent, snapshot.Payload()) {
		client.lastPlanDigest = digest
		client.planUpdates++
	}
}
