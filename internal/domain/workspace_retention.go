package domain

import "time"

// WorkspaceRetention is receipt metadata, not permission to read changes or
// evidence that their object bytes remain available. Native identities stay internal.
type WorkspaceRetention struct {
	SchemaVersion     string     `json:"schemaVersion"`
	IsolationDomainID string     `json:"isolationDomainId"`
	InvocationID      string     `json:"invocationId"`
	State             string     `json:"state"`
	Recovery          string     `json:"recovery"`
	RecoveryAccess    string     `json:"recoveryAccess,omitempty"`
	ArtifactID        string     `json:"artifactId,omitempty"`
	RecordedAt        *time.Time `json:"recordedAt,omitempty"`
}
