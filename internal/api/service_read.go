package api

import (
	"errors"
	"net/http"

	"github.com/asabla/dataground/internal/persistence"
)

func (server *Server) getAgentService(response http.ResponseWriter, request *http.Request) {
	domainID, problem := isolationDomain(request)
	if problem != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *problem})
		return
	}
	server.mu.RLock()
	service, exists := server.services[resourceKey(domainID, request.PathValue("serviceId"))]
	server.mu.RUnlock()
	if !exists {
		writeServiceNotFound(response, request)
		return
	}
	writeJSON(response, http.StatusOK, service)
}

func (server *DurableServer) getAgentService(response http.ResponseWriter, request *http.Request) {
	domainID, problem := isolationDomain(request)
	if problem != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *problem})
		return
	}
	service, err := server.repository.GetService(request.Context(), domainID, request.PathValue("serviceId"))
	if err != nil {
		var missing *persistence.DomainError
		if errors.As(err, &missing) && missing.Code == "RESOURCE_NOT_FOUND" {
			writeServiceNotFound(response, request)
			return
		}
		writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(
			authenticatedCorrelationID(request), "SERVICE_READ_UNAVAILABLE", "Agent service is temporarily unavailable.", true,
		)})
		return
	}
	writeJSON(response, http.StatusOK, service)
}

func writeServiceNotFound(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusNotFound, ErrorEnvelope{Error: safeErrorWithCorrelation(
		authenticatedCorrelationID(request), "RESOURCE_NOT_FOUND", "Agent service was not found.", false,
	)})
}
