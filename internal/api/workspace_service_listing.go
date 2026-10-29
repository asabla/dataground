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

type workspaceServiceCursor struct {
	Kind              string    `json:"kind"`
	Version           int       `json:"version"`
	IsolationDomainID string    `json:"isolationDomainId"`
	WorkspaceID       string    `json:"workspaceId"`
	CreatedAt         time.Time `json:"createdAt"`
	ID                string    `json:"id"`
}
type workspaceServicePage struct {
	Items      []domain.AgentService `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

func parseWorkspaceServiceQuery(raw, scope, workspaceID string) (int, *workspaceServiceCursor, error) {
	invalid := errors.New("workspace service query is invalid")
	query, err := url.ParseQuery(raw)
	if err != nil {
		return 0, nil, invalid
	}
	for name, values := range query {
		if (name != "limit" && name != "cursor") || len(values) != 1 {
			return 0, nil, invalid
		}
	}
	limit := defaultServiceListLimit
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > maximumServiceListLimit || strconv.Itoa(limit) != query.Get("limit") {
			return 0, nil, invalid
		}
	}
	if !query.Has("cursor") {
		return limit, nil, nil
	}
	encoded := query.Get("cursor")
	if encoded == "" || len(encoded) > maximumServiceCursorLen {
		return 0, nil, invalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return 0, nil, invalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	var cursor workspaceServiceCursor
	if decoder.Decode(&cursor) != nil || decoder.Decode(&struct{}{}) != io.EOF || cursor.Kind != "workspace-services" || cursor.Version != 1 || cursor.IsolationDomainID != scope || cursor.WorkspaceID != workspaceID || cursor.CreatedAt.IsZero() || !serviceIDPattern.MatchString(cursor.ID) {
		return 0, nil, invalid
	}
	cursor.CreatedAt = cursor.CreatedAt.UTC()
	canonical, err := json.Marshal(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(canonical) != encoded {
		return 0, nil, invalid
	}
	return limit, &cursor, nil
}

func writeWorkspaceServicePage(response http.ResponseWriter, request *http.Request, items []domain.AgentService, more bool) {
	page := workspaceServicePage{Items: items}
	if more {
		if len(items) == 0 {
			workspaceServiceFailure(response, request, false)
			return
		}
		last := items[len(items)-1]
		encoded, err := json.Marshal(workspaceServiceCursor{Kind: "workspace-services", Version: 1, IsolationDomainID: last.Metadata.IsolationDomainID, WorkspaceID: last.WorkspaceID, CreatedAt: last.Metadata.CreatedAt.UTC(), ID: last.Metadata.ID})
		if err != nil || last.Metadata.CreatedAt.IsZero() || !serviceIDPattern.MatchString(last.Metadata.ID) || last.WorkspaceID != request.PathValue("workspaceId") {
			workspaceServiceFailure(response, request, false)
			return
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	writeJSON(response, http.StatusOK, page)
}

func workspaceServiceQuery(response http.ResponseWriter, request *http.Request) (string, string, int, *workspaceServiceCursor, bool) {
	response.Header().Set("Cache-Control", "no-store")
	scope, problem := isolationDomain(request)
	if problem != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *problem})
		return "", "", 0, nil, false
	}
	workspaceID := request.PathValue("workspaceId")
	limit, cursor, err := parseWorkspaceServiceQuery(request.URL.RawQuery, scope, workspaceID)
	if err != nil || !workspaceIDPattern.MatchString(workspaceID) {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), "INVALID_REQUEST", "Workspace service scope, limit or cursor is invalid.", false)})
		return "", "", 0, nil, false
	}
	return scope, workspaceID, limit, cursor, true
}

func (server *Server) listWorkspaceAgentServices(response http.ResponseWriter, request *http.Request) {
	scope, workspaceID, limit, cursor, ok := workspaceServiceQuery(response, request)
	if !ok {
		return
	}
	items := make([]domain.AgentService, 0)
	server.mu.RLock()
	_, exists := server.workspaces[resourceKey(scope, workspaceID)]
	if exists {
		for _, service := range server.services {
			if service.Metadata.IsolationDomainID != scope || service.WorkspaceID != workspaceID {
				continue
			}
			created := service.Metadata.CreatedAt
			if cursor != nil && !(created.Before(cursor.CreatedAt) || (created.Equal(cursor.CreatedAt) && service.Metadata.ID < cursor.ID)) {
				continue
			}
			items = append(items, service)
		}
	}
	server.mu.RUnlock()
	if !exists {
		workspaceServiceFailure(response, request, true)
		return
	}
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
	writeWorkspaceServicePage(response, request, items, more)
}

func (server *DurableServer) listWorkspaceAgentServices(response http.ResponseWriter, request *http.Request) {
	scope, workspaceID, limit, cursor, ok := workspaceServiceQuery(response, request)
	if !ok {
		return
	}
	var before *time.Time
	var id string
	if cursor != nil {
		before = &cursor.CreatedAt
		id = cursor.ID
	}
	page, err := server.repository.ListWorkspaceServices(request.Context(), scope, workspaceID, before, id, limit)
	if err != nil {
		var problem *persistence.DomainError
		workspaceServiceFailure(response, request, errors.As(err, &problem) && problem.Code == "RESOURCE_NOT_FOUND")
		return
	}
	writeWorkspaceServicePage(response, request, page.Items, page.HasMore)
}

func workspaceServiceFailure(response http.ResponseWriter, request *http.Request, missing bool) {
	status, code, message, retryable := http.StatusServiceUnavailable, "WORKSPACE_SERVICE_LIST_UNAVAILABLE", "Workspace services are temporarily unavailable.", true
	if missing {
		status, code, message, retryable = http.StatusNotFound, "RESOURCE_NOT_FOUND", "Workspace was not found.", false
	}
	writeJSON(response, status, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), code, message, retryable)})
}
