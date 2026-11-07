package api

import (
	"errors"
	"net/http"

	"github.com/asabla/dataground/internal/authz"
	"github.com/jackc/pgx/v5"
)

func validWorkspaceRetentionRead(response http.ResponseWriter, request *http.Request) bool {
	response.Header().Set("Cache-Control", "no-store")
	if request.URL.RawQuery != "" || !invocationIDPattern.MatchString(request.PathValue("invocationId")) {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), "WORKSPACE_RETENTION_READ_INVALID", "Workspace retention read is invalid.", false)})
		return false
	}
	return true
}
func writeWorkspaceRetentionMissing(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusNotFound, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), "WORKSPACE_RETENTION_NOT_FOUND", "Workspace retention metadata was not found.", false)})
}
func (server *Server) readWorkspaceRetention(response http.ResponseWriter, request *http.Request) {
	if validWorkspaceRetentionRead(response, request) {
		writeWorkspaceRetentionMissing(response, request)
	}
}
func (server *DurableServer) readWorkspaceRetention(response http.ResponseWriter, request *http.Request) {
	if !validWorkspaceRetentionRead(response, request) {
		return
	}
	value, err := server.repository.ReadWorkspaceRetention(request.Context(), request.PathValue("isolationDomainId"), request.PathValue("invocationId"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeWorkspaceRetentionMissing(response, request)
		return
	}
	if err != nil {
		writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), "WORKSPACE_RETENTION_READ_UNAVAILABLE", "Workspace retention metadata is temporarily unavailable.", true)})
		return
	}
	value.RecoveryAccess = "unavailable"
	if principal, ok := authenticatedPrincipal(request); ok && server.retentionAuthorizer != nil {
		err := server.retentionAuthorizer.Authorize(request.Context(), authz.Request{Principal: principal, Action: authz.RequestWorkspaceRetentionRecovery, ResourceType: authz.Invocation, ResourceID: value.InvocationID, IsolationDomainID: value.IsolationDomainID, CorrelationID: authenticatedCorrelationID(request)})
		if err == nil {
			value.RecoveryAccess = "allowed"
		} else if errors.Is(err, authz.ErrDenied) {
			value.RecoveryAccess = "denied"
		}
	}
	writeJSON(response, http.StatusOK, value)
}
