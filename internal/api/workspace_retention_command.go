package api

import (
	"net/http"

	"github.com/asabla/dataground/internal/persistence"
)

type workspaceRetentionRecoveryRequest struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}

func decodeWorkspaceRetentionRecovery(request *http.Request, body []byte) (workspaceRetentionRecoveryRequest, *APIError) {
	value, problem := decodeBody[workspaceRetentionRecoveryRequest](body)
	if problem != nil {
		return value, problem
	}
	if request.URL.RawQuery != "" || !invocationIDPattern.MatchString(request.PathValue("invocationId")) || value.ExpectedVersion < 1 || value.ExpectedVersion > 9007199254740990 {
		err := safeErrorWithCorrelation(authenticatedCorrelationID(request), "WORKSPACE_RETENTION_RECOVERY_INVALID", "An exact invocation version is required for retention recovery.", false)
		return value, &err
	}
	return value, nil
}

func (server *Server) requestWorkspaceRetentionRecovery(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	server.mutate(response, request, func(_ string, body []byte) (int, any) {
		if _, problem := decodeWorkspaceRetentionRecovery(request, body); problem != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *problem}
		}
		return http.StatusConflict, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), "WORKSPACE_RETENTION_NOT_RECOVERABLE", "No stopped expired workspace is available for retention recovery.", false)}
	})
}

func (server *DurableServer) requestWorkspaceRetentionRecovery(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	server.mutate(response, request, func(scope, actor, correlation string, body []byte) (persistence.CommandResult, error) {
		value, problem := decodeWorkspaceRetentionRecovery(request, body)
		if problem != nil {
			return encodedError(http.StatusBadRequest, *problem)
		}
		return server.repository.RequestWorkspaceRetentionRecovery(request.Context(), commandIdempotency(request, scope, actor, body), persistence.RequestWorkspaceRetentionRecoveryInput{
			InvocationID: request.PathValue("invocationId"), ExpectedVersion: value.ExpectedVersion, ActorID: actor, CorrelationID: correlation,
		})
	})
}
