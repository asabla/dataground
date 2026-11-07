package domain

import "time"

// WorkspaceRetentionRecoveryReceipt confirms request acceptance only. It is
// safe to replay after completion and does not contain the invocation payload.
type WorkspaceRetentionRecoveryReceipt struct {
	SchemaVersion     string    `json:"schemaVersion"`
	IsolationDomainID string    `json:"isolationDomainId"`
	InvocationID      string    `json:"invocationId"`
	State             string    `json:"state"`
	InvocationVersion int64     `json:"invocationVersion"`
	DeadlineAt        time.Time `json:"deadlineAt"`
	CorrelationID     string    `json:"correlationId"`
}
