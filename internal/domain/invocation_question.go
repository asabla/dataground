package domain

import "time"

const InvocationQuestionSchemaV1 = "dataground.invocation-question/v1"

// InvocationQuestion exposes the bounded request and its durable state. Stored
// answers and native runtime bookkeeping require separate access boundaries.
type InvocationQuestion struct {
	SchemaVersion     string           `json:"schemaVersion"`
	ID                string           `json:"id"`
	IsolationDomainID string           `json:"isolationDomainId"`
	InvocationID      string           `json:"invocationId"`
	ServiceID         string           `json:"serviceId"`
	RevisionID        string           `json:"revisionId"`
	Questions         []QuestionPrompt `json:"questions"`
	State             string           `json:"state"`
	Version           int64            `json:"version"`
	ExpiresAt         time.Time        `json:"expiresAt"`
	AnsweredBy        string           `json:"answeredBy,omitempty"`
	AnsweredAt        *time.Time       `json:"answeredAt,omitempty"`
	ClosedAt          *time.Time       `json:"closedAt,omitempty"`
	CloseReason       string           `json:"closeReason,omitempty"`
	CreatedAt         time.Time        `json:"createdAt"`
	UpdatedAt         time.Time        `json:"updatedAt"`
}

const InvocationQuestionSummarySchemaV1 = "dataground.invocation-question-summary/v1"

// InvocationQuestionSummary supports discovery without disclosing prompts,
// answers, responder identity, or runtime routing. Content needs an exact read.
type InvocationQuestionSummary struct {
	SchemaVersion     string    `json:"schemaVersion"`
	ID                string    `json:"id"`
	IsolationDomainID string    `json:"isolationDomainId"`
	InvocationID      string    `json:"invocationId"`
	State             string    `json:"state"`
	Version           int64     `json:"version"`
	ExpiresAt         time.Time `json:"expiresAt"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}
