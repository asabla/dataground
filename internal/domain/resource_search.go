package domain

import (
	"regexp"
	"strings"
	"time"
)

// SearchMetadata contains navigation identity, not execution readiness or ownership.
type SearchMetadata struct {
	ID                string    `json:"id"`
	IsolationDomainID string    `json:"isolationDomainId"`
	CreatedAt         time.Time `json:"createdAt"`
}

type ResourceSearchResult struct {
	Metadata     SearchMetadata `json:"metadata"`
	Kind         string         `json:"kind"`
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	WorkspaceID  string         `json:"workspaceId,omitempty"`
	ServiceID    string         `json:"serviceId,omitempty"`
	InvocationID string         `json:"invocationId,omitempty"`
	State        string         `json:"state,omitempty"`
}

func SearchIdentity(metadata ResourceMetadata) SearchMetadata {
	return SearchMetadata{ID: metadata.ID, IsolationDomainID: metadata.IsolationDomainID, CreatedAt: metadata.CreatedAt}
}

func ValidResourceSearchKind(kind string) bool {
	switch kind {
	case "", "workspace", "agent-service", "invocation", "artifact":
		return true
	default:
		return false
	}
}

var searchID = regexp.MustCompile(`^(wsp|svc|inv|art)_[0-9a-z]{20,32}$`)

func ValidResourceSearchID(id string) bool { return searchID.MatchString(id) }

func MatchesResourceSearch(item ResourceSearchResult, query string) bool {
	query = FoldDiscoverySearch(query)
	return strings.Contains(FoldDiscoverySearch(item.Name), query) ||
		strings.Contains(FoldDiscoverySearch(item.Description), query) || strings.Contains(item.Metadata.ID, query)
}
