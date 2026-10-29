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
	"github.com/asabla/dataground/internal/persistence"
)

type scopeArtifactCursor struct {
	Kind              string    `json:"kind"`
	Version           int       `json:"version"`
	IsolationDomainID string    `json:"isolationDomainId"`
	State             string    `json:"state"`
	CreatedAt         time.Time `json:"createdAt"`
	ID                string    `json:"id"`
}
type scopeArtifactPage struct {
	Items      []domain.ScopeArtifactSummary `json:"items"`
	NextCursor string                        `json:"nextCursor,omitempty"`
}

func parseScopeArtifactQuery(raw, scope string) (int, string, *scopeArtifactCursor, error) {
	invalid := errors.New("scope artifact query is invalid")
	query, err := url.ParseQuery(raw)
	if err != nil {
		return 0, "", nil, invalid
	}
	for name, values := range query {
		if (name != "state" && name != "limit" && name != "cursor") || len(values) != 1 {
			return 0, "", nil, invalid
		}
	}
	state := query.Get("state")
	if !persistence.ValidArtifactDiscoveryState(state) || (query.Has("state") && state == "") {
		return 0, "", nil, invalid
	}
	limit := defaultServiceListLimit
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > maximumServiceListLimit || strconv.Itoa(limit) != query.Get("limit") {
			return 0, "", nil, invalid
		}
	}
	if !query.Has("cursor") {
		return limit, state, nil, nil
	}
	encoded := query.Get("cursor")
	if encoded == "" || len(encoded) > maximumServiceCursorLen {
		return 0, "", nil, invalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return 0, "", nil, invalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	var cursor scopeArtifactCursor
	if decoder.Decode(&cursor) != nil || decoder.Decode(&struct{}{}) != io.EOF || cursor.Kind != "scope-artifacts" || cursor.Version != 1 || cursor.IsolationDomainID != scope || cursor.State != state || cursor.CreatedAt.IsZero() || !artifactListIDPattern.MatchString(cursor.ID) {
		return 0, "", nil, invalid
	}
	cursor.CreatedAt = cursor.CreatedAt.UTC()
	canonical, err := json.Marshal(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(canonical) != encoded {
		return 0, "", nil, invalid
	}
	return limit, state, &cursor, nil
}

func writeScopeArtifactPage(response http.ResponseWriter, request *http.Request, state string, items []domain.ScopeArtifactSummary, more bool) {
	page := scopeArtifactPage{Items: items}
	if more {
		if len(items) == 0 {
			writeScopeArtifactListUnavailable(response, request)
			return
		}
		last := items[len(items)-1]
		encoded, err := json.Marshal(scopeArtifactCursor{Kind: "scope-artifacts", Version: 1, IsolationDomainID: last.Metadata.IsolationDomainID, State: state, CreatedAt: last.Metadata.CreatedAt.UTC(), ID: last.Metadata.ID})
		if err != nil || last.Metadata.CreatedAt.IsZero() || !artifactListIDPattern.MatchString(last.Metadata.ID) {
			writeScopeArtifactListUnavailable(response, request)
			return
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, page)
}

func scopeArtifactQueryError(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), "INVALID_REQUEST", "Artifact-list limit, state or cursor is invalid.", false)})
}

func (server *Server) listScopeArtifacts(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	scope, problem := isolationDomain(request)
	if problem != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *problem})
		return
	}
	limit, state, cursor, err := parseScopeArtifactQuery(request.URL.RawQuery, scope)
	if err != nil {
		scopeArtifactQueryError(response, request)
		return
	}
	items := make([]domain.ScopeArtifactSummary, 0)
	server.mu.RLock()
	for _, artifact := range server.artifacts {
		if artifact.Metadata.IsolationDomainID != scope || (state != "" && artifact.State != state) {
			continue
		}
		created := artifact.Metadata.CreatedAt
		if cursor != nil && !(created.Before(cursor.CreatedAt) || (created.Equal(cursor.CreatedAt) && artifact.Metadata.ID < cursor.ID)) {
			continue
		}
		invocation, exists := server.invocations[resourceKey(scope, artifact.InvocationID)]
		if !exists {
			continue
		}
		service, exists := server.services[resourceKey(scope, invocation.ServiceID)]
		if !exists {
			continue
		}
		items = append(items, domain.ScopeArtifactSummary{ArtifactDescriptor: artifact, ServiceID: invocation.ServiceID, ServiceName: service.Name, RevisionID: invocation.RevisionID})
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
	writeScopeArtifactPage(response, request, state, items, more)
}

func (server *DurableServer) listScopeArtifacts(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	scope, problem := isolationDomain(request)
	if problem != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *problem})
		return
	}
	limit, state, cursor, err := parseScopeArtifactQuery(request.URL.RawQuery, scope)
	if err != nil {
		scopeArtifactQueryError(response, request)
		return
	}
	var before *time.Time
	var id string
	if cursor != nil {
		before = &cursor.CreatedAt
		id = cursor.ID
	}
	page, err := server.repository.ListScopeArtifacts(request.Context(), scope, state, before, id, limit)
	if err != nil {
		writeScopeArtifactListUnavailable(response, request)
		return
	}
	writeScopeArtifactPage(response, request, state, page.Items, page.HasMore)
}

func writeScopeArtifactListUnavailable(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), "SCOPE_ARTIFACT_LIST_UNAVAILABLE", "Artifacts are temporarily unavailable.", true)})
}
