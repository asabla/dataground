package runtime

import (
	"encoding/json"
	"unicode/utf8"
)

const PlanUpdatedEvent = "output.plan.updated"
const PlanArtifactEvent = "output.plan.artifact"
const PlanSchemaV1 = "dataground.runtime-plan/v1"
const MaximumPlanBytes = 1 << 20
const MaximumPlanSteps = 256
const PlanPreview = "Plan update retained."

type PlanStep struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

// A plan is untrusted progress content. It grants no execution authority and
// completed steps do not prove successful effects or invocation completion.
type PlanSnapshot struct {
	SchemaVersion string     `json:"schemaVersion"`
	Explanation   *string    `json:"explanation"`
	Steps         []PlanStep `json:"steps"`
}

func (value PlanSnapshot) Payload() map[string]any {
	var explanation any
	if value.Explanation != nil {
		explanation = *value.Explanation
	}
	steps := make([]any, 0, len(value.Steps))
	for _, step := range value.Steps {
		steps = append(steps, map[string]any{"text": step.Text, "status": step.Status})
	}
	return map[string]any{"schemaVersion": value.SchemaVersion, "explanation": explanation, "steps": steps}
}
func ParsePlanSnapshot(payload map[string]any) (PlanSnapshot, error) {
	value := PlanSnapshot{SchemaVersion: PlanSchemaV1, Steps: []PlanStep{}}
	if len(payload) != 3 || payload["schemaVersion"] != PlanSchemaV1 {
		return PlanSnapshot{}, ErrProtocol
	}
	explanation, ok := payload["explanation"]
	if !ok {
		return PlanSnapshot{}, ErrProtocol
	}
	budget := MaximumPlanBytes
	if explanation != nil {
		text, ok := explanation.(string)
		if !ok || !utf8.ValidString(text) || len(text) > budget {
			return PlanSnapshot{}, ErrProtocol
		}
		value.Explanation = &text
		budget -= len(text)
	}
	steps, ok := payload["steps"].([]any)
	if !ok || steps == nil || len(steps) > MaximumPlanSteps {
		return PlanSnapshot{}, ErrProtocol
	}
	for _, entry := range steps {
		step, ok := entry.(map[string]any)
		if !ok || len(step) != 2 {
			return PlanSnapshot{}, ErrProtocol
		}
		text, ok := step["text"].(string)
		if !ok || !utf8.ValidString(text) || len(text) > budget {
			return PlanSnapshot{}, ErrProtocol
		}
		status, ok := step["status"].(string)
		if !ok || (status != "pending" && status != "in_progress" && status != "completed") {
			return PlanSnapshot{}, ErrProtocol
		}
		budget -= len(text)
		value.Steps = append(value.Steps, PlanStep{Text: text, Status: status})
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > MaximumPlanBytes {
		return PlanSnapshot{}, ErrProtocol
	}
	return value, nil
}
func (value PlanSnapshot) Counts() (completed, inProgress int) {
	for _, step := range value.Steps {
		if step.Status == "completed" {
			completed++
		}
		if step.Status == "in_progress" {
			inProgress++
		}
	}
	return
}

type PlanArtifact struct {
	ArtifactID      string `json:"artifactId"`
	Digest          string `json:"digest"`
	SizeBytes       int64  `json:"sizeBytes"`
	StepCount       int    `json:"stepCount"`
	CompletedCount  int    `json:"completedCount"`
	InProgressCount int    `json:"inProgressCount"`
	Preview         string `json:"preview"`
}

func (value PlanArtifact) Payload() map[string]any {
	return map[string]any{"artifactId": value.ArtifactID, "digest": value.Digest, "sizeBytes": value.SizeBytes, "stepCount": value.StepCount, "completedCount": value.CompletedCount, "inProgressCount": value.InProgressCount, "preview": value.Preview}
}
func ParsePlanArtifact(payload map[string]any) (PlanArtifact, error) {
	var value PlanArtifact
	if len(payload) != 7 {
		return value, ErrProtocol
	}
	for _, key := range []string{"artifactId", "digest", "preview"} {
		if _, ok := payload[key].(string); !ok {
			return value, ErrProtocol
		}
	}
	for _, key := range []string{"sizeBytes", "stepCount", "completedCount", "inProgressCount"} {
		if v, ok := payload[key]; !ok || v == nil {
			return value, ErrProtocol
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil || json.Unmarshal(encoded, &value) != nil || !runtimeArtifactID.MatchString(value.ArtifactID) || !runtimeArtifactDigest.MatchString(value.Digest) || value.SizeBytes <= 0 || value.SizeBytes > MaximumPlanBytes || value.StepCount < 0 || value.StepCount > MaximumPlanSteps || value.CompletedCount < 0 || value.CompletedCount > value.StepCount || value.InProgressCount < 0 || value.InProgressCount > value.StepCount-value.CompletedCount || value.Preview != PlanPreview {
		return PlanArtifact{}, ErrProtocol
	}
	return value, nil
}
