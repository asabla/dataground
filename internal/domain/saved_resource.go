package domain

import "time"

// SavedResource retains a navigation reference, never a copy of resource content.
// A missing Resource means the referenced metadata is no longer available.
type SavedResource struct {
	IsolationDomainID string                `json:"isolationDomainId"`
	Kind              string                `json:"kind"`
	ResourceID        string                `json:"resourceId"`
	SavedAt           time.Time             `json:"savedAt"`
	Resource          *ResourceSearchResult `json:"resource,omitempty"`
}

type SavedResourceState struct {
	IsolationDomainID string    `json:"isolationDomainId"`
	Kind              string    `json:"kind"`
	ResourceID        string    `json:"resourceId"`
	Saved             bool      `json:"saved"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

func ValidSavedResource(kind, id string) bool {
	if !ValidResourceSearchID(id) {
		return false
	}
	prefix := map[string]string{"workspace": "wsp", "agent-service": "svc", "invocation": "inv", "artifact": "art"}[kind]
	return prefix != "" && id[:3] == prefix
}
