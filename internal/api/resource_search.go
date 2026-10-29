package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/domain"
)

type resourceSearchCursor struct {
	Kind              string    `json:"kind"`
	Version           int       `json:"version"`
	IsolationDomainID string    `json:"isolationDomainId"`
	Search            string    `json:"search"`
	ResourceKind      string    `json:"resourceKind"`
	CreatedAt         time.Time `json:"createdAt"`
	ID                string    `json:"id"`
}
type resourceSearchPage struct {
	Items      []domain.ResourceSearchResult `json:"items"`
	NextCursor string                        `json:"nextCursor,omitempty"`
}

func parseResourceSearch(raw, scope string) (int, string, string, *resourceSearchCursor, error) {
	invalid := errors.New("resource search query is invalid")
	query, err := url.ParseQuery(raw)
	if err != nil {
		return 0, "", "", nil, invalid
	}
	for name, values := range query {
		if (name != "q" && name != "kind" && name != "limit" && name != "cursor") || len(values) != 1 {
			return 0, "", "", nil, invalid
		}
	}
	q, kind := query.Get("q"), query.Get("kind")
	if q == "" || !domain.ValidDiscoverySearch(q) || !domain.ValidResourceSearchKind(kind) || (query.Has("kind") && kind == "") {
		return 0, "", "", nil, invalid
	}
	limit := defaultServiceListLimit
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > maximumServiceListLimit || strconv.Itoa(limit) != query.Get("limit") {
			return 0, "", "", nil, invalid
		}
	}
	if !query.Has("cursor") {
		return limit, q, kind, nil, nil
	}
	encoded := query.Get("cursor")
	if encoded == "" || len(encoded) > maximumServiceCursorLen {
		return 0, "", "", nil, invalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return 0, "", "", nil, invalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	var cursor resourceSearchCursor
	if decoder.Decode(&cursor) != nil || decoder.Decode(&struct{}{}) != io.EOF || cursor.Kind != "resource-search" || cursor.Version != 1 || cursor.IsolationDomainID != scope || cursor.Search != discoverySearchDigest(q) || cursor.ResourceKind != kind || cursor.CreatedAt.IsZero() || !domain.ValidResourceSearchID(cursor.ID) {
		return 0, "", "", nil, invalid
	}
	cursor.CreatedAt = cursor.CreatedAt.UTC()
	canonical, err := json.Marshal(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(canonical) != encoded {
		return 0, "", "", nil, invalid
	}
	return limit, q, kind, &cursor, nil
}

func writeResourceSearchUnavailable(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), "RESOURCE_SEARCH_UNAVAILABLE", "Resource search is unavailable.", true)})
}
func writeResourceSearchPage(response http.ResponseWriter, request *http.Request, query, kind string, items []domain.ResourceSearchResult, more bool) {
	page := resourceSearchPage{Items: items}
	if more {
		if len(items) == 0 {
			writeResourceSearchUnavailable(response, request)
			return
		}
		last := items[len(items)-1]
		encoded, err := json.Marshal(resourceSearchCursor{Kind: "resource-search", Version: 1, IsolationDomainID: last.Metadata.IsolationDomainID, Search: discoverySearchDigest(query), ResourceKind: kind, CreatedAt: last.Metadata.CreatedAt.UTC(), ID: last.Metadata.ID})
		if err != nil || last.Metadata.CreatedAt.IsZero() || !domain.ValidResourceSearchID(last.Metadata.ID) {
			writeResourceSearchUnavailable(response, request)
			return
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	writeJSON(response, http.StatusOK, page)
}
func resourceSearchQueryError(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), "INVALID_REQUEST", "Search text, kind, limit or cursor is invalid.", false)})
}

func (server *Server) searchResources(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	scope, problem := isolationDomain(request)
	if problem != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *problem})
		return
	}
	limit, q, kind, cursor, err := parseResourceSearch(request.URL.RawQuery, scope)
	if err != nil {
		resourceSearchQueryError(response, request)
		return
	}
	items := make([]domain.ResourceSearchResult, 0)
	add := func(item domain.ResourceSearchResult) {
		if item.Metadata.IsolationDomainID != scope || (kind != "" && item.Kind != kind) || !domain.MatchesResourceSearch(item, q) {
			return
		}
		created := item.Metadata.CreatedAt
		if cursor != nil && !(created.Before(cursor.CreatedAt) || (created.Equal(cursor.CreatedAt) && item.Metadata.ID < cursor.ID)) {
			return
		}
		items = append(items, item)
	}
	server.mu.RLock()
	for _, w := range server.workspaces {
		add(domain.ResourceSearchResult{Metadata: domain.SearchIdentity(w.Metadata), Kind: "workspace", Name: w.Name, Description: w.Description})
	}
	for _, s := range server.services {
		add(domain.ResourceSearchResult{Metadata: domain.SearchIdentity(s.Metadata), Kind: "agent-service", Name: s.Name, Description: s.Description, WorkspaceID: s.WorkspaceID})
	}
	for _, i := range server.invocations {
		if i.Metadata.IsolationDomainID != scope {
			continue
		}
		s, exists := server.services[resourceKey(scope, i.ServiceID)]
		if exists {
			add(domain.ResourceSearchResult{Metadata: domain.SearchIdentity(i.Metadata), Kind: "invocation", Name: s.Name, WorkspaceID: s.WorkspaceID, ServiceID: i.ServiceID, State: i.State})
		}
	}
	for _, a := range server.artifacts {
		if a.Metadata.IsolationDomainID != scope {
			continue
		}
		i, exists := server.invocations[resourceKey(scope, a.InvocationID)]
		if !exists {
			continue
		}
		s, exists := server.services[resourceKey(scope, i.ServiceID)]
		if exists {
			add(domain.ResourceSearchResult{Metadata: domain.SearchIdentity(a.Metadata), Kind: "artifact", Name: a.Name, WorkspaceID: s.WorkspaceID, ServiceID: i.ServiceID, InvocationID: a.InvocationID, State: a.State})
		}
	}
	server.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].Metadata.CreatedAt.Equal(items[j].Metadata.CreatedAt) {
			return items[i].Metadata.ID > items[j].Metadata.ID
		}
		return items[i].Metadata.CreatedAt.After(items[j].Metadata.CreatedAt)
	})
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	writeResourceSearchPage(response, request, q, kind, items, more)
}

func (server *DurableServer) searchResources(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	scope, problem := isolationDomain(request)
	if problem != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *problem})
		return
	}
	limit, q, kind, cursor, err := parseResourceSearch(request.URL.RawQuery, scope)
	if err != nil {
		resourceSearchQueryError(response, request)
		return
	}
	var before *time.Time
	var id string
	if cursor != nil {
		before = &cursor.CreatedAt
		id = cursor.ID
	}
	page, err := server.repository.SearchResources(request.Context(), scope, q, kind, before, id, limit)
	if err != nil {
		writeResourceSearchUnavailable(response, request)
		return
	}
	writeResourceSearchPage(response, request, q, kind, page.Items, page.HasMore)
}
