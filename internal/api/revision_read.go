package api

import (
	"errors"
	"net/http"

	"github.com/asabla/dataground/internal/persistence"
)

func (server *Server) getServiceRevision(response http.ResponseWriter, request *http.Request) {
	domainID, problem := isolationDomain(request)
	if problem != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *problem})
		return
	}
	server.mu.RLock()
	revision, exists := server.revisions[resourceKey(domainID, request.PathValue("revisionId"))]
	server.mu.RUnlock()
	if !exists {
		writeRevisionNotFound(response, request)
		return
	}
	writeJSON(response, http.StatusOK, revision)
}

func (server *DurableServer) getServiceRevision(response http.ResponseWriter, request *http.Request) {
	domainID, problem := isolationDomain(request)
	if problem != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *problem})
		return
	}
	revision, err := server.repository.GetServiceRevision(request.Context(), domainID, request.PathValue("revisionId"))
	if err != nil {
		var missing *persistence.DomainError
		if errors.As(err, &missing) && missing.Code == "RESOURCE_NOT_FOUND" {
			writeRevisionNotFound(response, request)
			return
		}
		writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(
			authenticatedCorrelationID(request), "REVISION_READ_UNAVAILABLE", "Service revision is temporarily unavailable.", true,
		)})
		return
	}
	writeJSON(response, http.StatusOK, revision)
}

func writeRevisionNotFound(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusNotFound, ErrorEnvelope{Error: safeErrorWithCorrelation(
		authenticatedCorrelationID(request), "RESOURCE_NOT_FOUND", "Service revision was not found.", false,
	)})
}
