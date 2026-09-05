package api

import (
	"net/http"

	"github.com/asabla/dataground/internal/persistence"
)

type withdrawServiceAliasRequest struct {
	ExpectedVersion int `json:"expectedVersion"`
}

func aliasWithdrawalRequest(body []byte, name, correlationID string) (withdrawServiceAliasRequest, *APIError) {
	input, problem := decodeBody[withdrawServiceAliasRequest](body)
	if problem == nil && (input.ExpectedVersion < 1 || len(name) > 63 || !aliasPattern.MatchString(name)) {
		value := safeErrorWithCorrelation(correlationID, "INVALID_REQUEST", "A valid alias and positive expected version are required.", false)
		problem = &value
	}
	if problem != nil {
		problem.CorrelationID = correlationID
	}
	return input, problem
}
func (server *Server) withdrawServiceAlias(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(_ string, body []byte) (int, any) {
		name, correlationID := request.PathValue("alias"), authenticatedCorrelationID(request)
		input, problem := aliasWithdrawalRequest(body, name, correlationID)
		if problem != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *problem}
		}
		domainID, problem := isolationDomain(request)
		if problem != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *problem}
		}
		key := aliasKey(domainID, request.PathValue("serviceId"), name)
		alias, exists := server.aliases[key]
		if !exists || alias.WithdrawnAt != nil {
			return notFound("Service alias was not found.")
		}
		if alias.Metadata.Version != input.ExpectedVersion {
			return conflict("VERSION_CONFLICT", "Alias version did not match.")
		}
		now := server.now()
		alias.WithdrawnAt = &now
		alias.Metadata.Version++
		alias.Metadata.Generation++
		alias.Metadata.UpdatedAt = now
		server.aliases[key] = alias
		return http.StatusOK, alias
	})
}
func (server *DurableServer) withdrawServiceAlias(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(domainID, actorID, correlationID string, body []byte) (persistence.CommandResult, error) {
		name := request.PathValue("alias")
		input, problem := aliasWithdrawalRequest(body, name, correlationID)
		if problem != nil {
			return encodedError(http.StatusBadRequest, *problem)
		}
		return server.repository.WithdrawAlias(request.Context(), commandIdempotency(request, domainID, actorID, body), persistence.WithdrawAliasInput{ServiceID: request.PathValue("serviceId"), Name: name, ExpectedVersion: input.ExpectedVersion, ActorID: actorID, CorrelationID: correlationID})
	})
}
