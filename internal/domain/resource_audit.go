package domain

import "time"

const ResourceAuditPageSchemaV1 = "dataground.resource-audit-page/v1"

// ResourceAuditRecord is a safe projection. It contains no stored metadata,
// runtime payloads, policy entities, or provider routing information.
type ResourceAuditRecord struct {
	ID            string    `json:"id"`
	Source        string    `json:"source"`
	RecordedAt    time.Time `json:"recordedAt"`
	ActorID       string    `json:"actorId"`
	Action        string    `json:"action"`
	Outcome       string    `json:"outcome"`
	CorrelationID string    `json:"correlationId"`
	OperationID   string    `json:"operationId,omitempty"`
	PolicySetID   string    `json:"policySetId,omitempty"`
	PolicyDigest  string    `json:"policyDigest,omitempty"`
	Phase         string    `json:"phase,omitempty"`
}

type ResourceAuditPage struct {
	SchemaVersion     string                `json:"schemaVersion"`
	IsolationDomainID string                `json:"isolationDomainId"`
	ResourceType      string                `json:"resourceType"`
	ResourceID        string                `json:"resourceId"`
	ReceiptID         string                `json:"receiptId"`
	Items             []ResourceAuditRecord `json:"items"`
	NextCursor        string                `json:"nextCursor,omitempty"`
}
