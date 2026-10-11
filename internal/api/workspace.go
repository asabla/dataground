package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

type createWorkspaceRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (server *Server) createWorkspace(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(actorID string, body []byte) (int, any) {
		scope, problem := isolationDomain(request)
		if problem != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *problem}
		}
		if !utf8.Valid(body) {
			return invalidField("workspace", "Workspace content must be valid UTF-8.")
		}
		input, problem := decodeWorkspaceRequest(body)
		if problem != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *problem}
		}
		input.Name = strings.TrimSpace(input.Name)
		if !domain.ValidWorkspaceMetadata(input.Name, input.Description) {
			return invalidField("workspace", "Workspace name or description is invalid.")
		}
		value := domain.Workspace{Metadata: newMetadata(newID("wsp"), scope, actorID, server.now()), Name: input.Name, Description: input.Description}
		server.workspaces[resourceKey(scope, value.Metadata.ID)] = value
		return http.StatusCreated, value
	})
}
func (server *DurableServer) createWorkspace(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(scope, actor, correlation string, body []byte) (persistence.CommandResult, error) {
		if !utf8.Valid(body) {
			return encodedResult(invalidField("workspace", "Workspace content must be valid UTF-8."))
		}
		input, problem := decodeWorkspaceRequest(body)
		if problem != nil {
			return encodedError(http.StatusBadRequest, *problem)
		}
		input.Name = strings.TrimSpace(input.Name)
		if !domain.ValidWorkspaceMetadata(input.Name, input.Description) {
			return encodedResult(invalidField("workspace", "Workspace name or description is invalid."))
		}
		return server.repository.CreateWorkspace(request.Context(), commandIdempotency(request, scope, actor, body), persistence.CreateWorkspaceInput{ID: identity.New("wsp"), Name: input.Name, Description: input.Description, ActorID: actor, CorrelationID: correlation})
	})
}
func (server *Server) getWorkspace(response http.ResponseWriter, request *http.Request) {
	scope, problem := isolationDomain(request)
	if problem != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *problem})
		return
	}
	server.mu.RLock()
	value, found := server.workspaces[resourceKey(scope, request.PathValue("workspaceId"))]
	server.mu.RUnlock()
	if !found {
		writeWorkspaceNotFound(response, request)
		return
	}
	writeJSON(response, http.StatusOK, value)
}
func (server *DurableServer) getWorkspace(response http.ResponseWriter, request *http.Request) {
	scope, problem := isolationDomain(request)
	if problem != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *problem})
		return
	}
	value, err := server.repository.GetWorkspace(request.Context(), scope, request.PathValue("workspaceId"))
	if err != nil {
		var missing *persistence.DomainError
		if errors.As(err, &missing) && missing.Code == "RESOURCE_NOT_FOUND" {
			writeWorkspaceNotFound(response, request)
			return
		}
		writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), "WORKSPACE_READ_UNAVAILABLE", "Workspace is temporarily unavailable.", true)})
		return
	}
	writeJSON(response, http.StatusOK, value)
}
func writeWorkspaceNotFound(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusNotFound, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), "RESOURCE_NOT_FOUND", "Workspace was not found.", false)})
}
func (server *Server) listWorkspaces(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	limit, cursor, err := parseWorkspaceListQuery(request.URL.Query(), domainID)
	if err != nil {
		status, body := invalidField("query", "Workspace-list limit or cursor is invalid.")
		writeJSON(response, status, body)
		return
	}
	server.mu.RLock()
	workspaces := make([]domain.Workspace, 0, len(server.workspaces))
	for _, workspace := range server.workspaces {
		if workspace.Metadata.IsolationDomainID == domainID {
			workspaces = append(workspaces, workspace)
		}
	}
	server.mu.RUnlock()
	sort.Slice(workspaces, func(left, right int) bool {
		if workspaces[left].Metadata.CreatedAt.Equal(workspaces[right].Metadata.CreatedAt) {
			return workspaces[left].Metadata.ID > workspaces[right].Metadata.ID
		}
		return workspaces[left].Metadata.CreatedAt.After(workspaces[right].Metadata.CreatedAt)
	})
	if cursor != nil {
		workspaces = slicesAfterWorkspaceCursor(workspaces, *cursor)
	}
	hasMore := len(workspaces) > limit
	if hasMore {
		workspaces = workspaces[:limit]
	}
	page := workspacePage{Items: workspaces}
	if hasMore {
		page.NextCursor, err = encodeWorkspaceListCursor(workspaces[len(workspaces)-1])
		if err != nil {
			writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(
				authenticatedCorrelationID(request), "WORKSPACE_LIST_UNAVAILABLE", "Workspaces are temporarily unavailable.", true,
			)})
			return
		}
	}
	writeJSON(response, http.StatusOK, page)
}

func slicesAfterWorkspaceCursor(workspaces []domain.Workspace, cursor workspaceListCursor) []domain.Workspace {
	for index, workspace := range workspaces {
		createdAt := workspace.Metadata.CreatedAt
		if createdAt.Before(cursor.CreatedAt) ||
			(createdAt.Equal(cursor.CreatedAt) && workspace.Metadata.ID < cursor.ID) {
			return workspaces[index:]
		}
	}
	return workspaces[:0]
}

func (server *DurableServer) listWorkspaces(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	limit, cursor, err := parseWorkspaceListQuery(request.URL.Query(), domainID)
	if err != nil {
		status, body := invalidField("query", "Workspace-list limit or cursor is invalid.")
		writeJSON(response, status, body)
		return
	}
	var beforeCreatedAt *time.Time
	var beforeID string
	if cursor != nil {
		createdAt := cursor.CreatedAt
		beforeCreatedAt = &createdAt
		beforeID = cursor.ID
	}
	listed, err := server.repository.ListWorkspaces(
		request.Context(), domainID, beforeCreatedAt, beforeID, limit,
	)
	if err != nil {
		writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(
			authenticatedCorrelationID(request), "WORKSPACE_LIST_UNAVAILABLE", "Workspaces are temporarily unavailable.", true,
		)})
		return
	}
	page := workspacePage{Items: listed.Items}
	if listed.HasMore {
		page.NextCursor, err = encodeWorkspaceListCursor(listed.Items[len(listed.Items)-1])
		if err != nil {
			writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(
				authenticatedCorrelationID(request), "WORKSPACE_LIST_UNAVAILABLE", "Workspaces are temporarily unavailable.", true,
			)})
			return
		}
	}
	writeJSON(response, http.StatusOK, page)
}

func decodeWorkspaceRequest(body []byte) (createWorkspaceRequest, *APIError) {
	input, problem := decodeBody[createWorkspaceRequest](body)
	if problem != nil {
		return input, problem
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil || bytes.Equal(bytes.TrimSpace(raw["description"]), []byte("null")) {
		problem := safeError("INVALID_REQUEST", "Workspace description must be a string when present.", false)
		return createWorkspaceRequest{}, &problem
	}
	for key := range raw {
		if key != "name" && key != "description" {
			problem := safeError("INVALID_REQUEST", "Workspace contains an unknown field.", false)
			return createWorkspaceRequest{}, &problem
		}
	}
	return input, nil
}
