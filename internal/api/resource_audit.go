package api

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/asabla/dataground/internal/persistence"
)

var resourceAuditCursorPattern = regexp.MustCompile(`^arr_[0-9a-z]{20,32}$`)

func parseResourceAuditRequest(request *http.Request) (scope, kind, id, cursor string, limit int, err error) {
	invalid := errors.New("invalid resource audit query")
	scope = request.PathValue("isolationDomainId")
	kind, id = "service-revision", request.PathValue("revisionId")
	if id == "" {
		kind, id = "invocation", request.PathValue("invocationId")
	}
	if (kind == "service-revision" && !revisionIDPattern.MatchString(id)) || (kind == "invocation" && !invocationIDPattern.MatchString(id)) {
		err = invalid
		return
	}
	query, parseErr := url.ParseQuery(request.URL.RawQuery)
	if parseErr != nil {
		err = invalid
		return
	}
	for name, values := range query {
		if (name != "cursor" && name != "limit") || len(values) != 1 {
			err = invalid
			return
		}
	}
	limit = defaultServiceListLimit
	if query.Has("limit") {
		value, parseErr := strconv.Atoi(query.Get("limit"))
		if parseErr != nil || value < 1 || value > maximumServiceListLimit || strconv.Itoa(value) != query.Get("limit") {
			err = invalid
			return
		}
		limit = value
	}
	if query.Has("cursor") {
		cursor = query.Get("cursor")
		if !resourceAuditCursorPattern.MatchString(cursor) {
			err = invalid
		}
	}
	return
}

func writeResourceAuditError(response http.ResponseWriter, request *http.Request, err error) {
	status, code, message, retry := http.StatusServiceUnavailable, "RESOURCE_AUDIT_UNAVAILABLE", "Resource audit is temporarily unavailable.", true
	var problem *persistence.DomainError
	if errors.Is(err, persistence.ErrResourceAuditInvalid) {
		status, code, message, retry = http.StatusBadRequest, "INVALID_REQUEST", "Resource audit query is invalid.", false
	} else if errors.As(err, &problem) && problem.Code == "RESOURCE_NOT_FOUND" {
		status, code, message, retry = http.StatusNotFound, problem.Code, problem.Message, false
	}
	writeJSON(response, status, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), code, message, retry)})
}

func (server *DurableServer) readResourceAudit(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	scope, kind, id, cursor, limit, err := parseResourceAuditRequest(request)
	if err != nil {
		writeResourceAuditError(response, request, persistence.ErrResourceAuditInvalid)
		return
	}
	principal, ok := authenticatedPrincipal(request)
	if !ok {
		writeResourceAuditError(response, request, errors.New("principal unavailable"))
		return
	}
	page, err := server.repository.ReadResourceAudit(request.Context(), principal, scope, kind, id, authenticatedCorrelationID(request), cursor, limit)
	if err != nil {
		writeResourceAuditError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, page)
}

func (server *Server) readResourceAudit(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	scope, kind, id, _, _, err := parseResourceAuditRequest(request)
	if err != nil {
		writeResourceAuditError(response, request, persistence.ErrResourceAuditInvalid)
		return
	}
	server.mu.RLock()
	_, exists := server.revisions[resourceKey(scope, id)]
	if kind == "invocation" {
		_, exists = server.invocations[resourceKey(scope, id)]
	}
	server.mu.RUnlock()
	if !exists {
		writeResourceAuditError(response, request, &persistence.DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Audit resource was not found."})
		return
	}
	writeResourceAuditError(response, request, errors.New("durable audit unavailable in reference mode"))
}
