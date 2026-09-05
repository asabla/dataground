package api

import (
	"github.com/asabla/dataground/internal/persistence"
	"net/http"
)

type retireServiceRevisionRequest struct {
	ExpectedVersion int `json:"expectedVersion"`
}

func retirementRequest(body []byte, correlationID string) (retireServiceRevisionRequest, *APIError) {
	input, problem := decodeBody[retireServiceRevisionRequest](body)
	if problem != nil {
		problem.CorrelationID = correlationID
		return input, problem
	}
	if input.ExpectedVersion < 1 {
		problem = safeRetirementError(correlationID, "INVALID_REQUEST", "A positive expected revision version is required.")
	}
	return input, problem
}

func safeRetirementError(correlationID, code, message string) *APIError {
	problem := safeErrorWithCorrelation(correlationID, code, message, false)
	return &problem
}

func (server *Server) retireServiceRevision(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(_ string, body []byte) (int, any) {
		correlationID := authenticatedCorrelationID(request)
		input, problem := retirementRequest(body, correlationID)
		if problem != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *problem}
		}
		domainID, problem := isolationDomain(request)
		if problem != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *problem}
		}
		reject := func(status int, code, message string) (int, any) {
			return status, ErrorEnvelope{Error: *safeRetirementError(correlationID, code, message)}
		}
		key := resourceKey(domainID, request.PathValue("revisionId"))
		revision, exists := server.revisions[key]
		if !exists {
			return reject(http.StatusNotFound, "RESOURCE_NOT_FOUND", "Service revision was not found.")
		}
		if revision.Metadata.Version != input.ExpectedVersion {
			return reject(http.StatusConflict, "VERSION_CONFLICT", "Revision version did not match.")
		}
		if revision.State != "published" {
			return reject(http.StatusConflict, "REVISION_NOT_PUBLISHED", "Only a published revision can be retired.")
		}
		for _, alias := range server.aliases {
			if alias.Metadata.IsolationDomainID == domainID && alias.RevisionID == revision.Metadata.ID && alias.WithdrawnAt == nil {
				return reject(http.StatusConflict, "REVISION_STILL_ROUTED", "Move or withdraw all aliases from this revision before retiring it.")
			}
		}
		for _, invocation := range server.invocations {
			if invocation.Metadata.IsolationDomainID == domainID && invocation.RevisionID == revision.Metadata.ID && invocation.State != "succeeded" && invocation.State != "failed" && invocation.State != "cancelled" {
				return reject(http.StatusConflict, "REVISION_STILL_ACTIVE", "Wait for all invocation activity to finish before retiring this revision.")
			}
		}
		revision.State = "retired"
		revision.Metadata.Generation++
		revision.Metadata.Version++
		revision.Metadata.UpdatedAt = server.now()
		server.revisions[key] = revision
		return http.StatusOK, revision
	})
}

func (server *DurableServer) retireServiceRevision(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(domainID, actorID, correlationID string, body []byte) (persistence.CommandResult, error) {
		input, problem := retirementRequest(body, correlationID)
		if problem != nil {
			return encodedError(http.StatusBadRequest, *problem)
		}
		return server.repository.RetireRevision(request.Context(), commandIdempotency(request, domainID, actorID, body), persistence.RetireRevisionInput{RevisionID: request.PathValue("revisionId"), ExpectedVersion: input.ExpectedVersion, ActorID: actorID, CorrelationID: correlationID})
	})
}
