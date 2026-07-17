package api

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reference"
	"github.com/jackc/pgx/v5"
)

const durableOperationDeadline = 15 * time.Minute

type DurableServer struct {
	repository *persistence.Repository
}

func NewDurableHandler(repository *persistence.Repository) http.Handler {
	server := &DurableServer{repository: repository}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", healthHandler)
	mux.HandleFunc("GET /readyz", server.ready)
	mux.HandleFunc("POST /v1/isolation-domains/{isolationDomainId}/agent-services", server.createAgentService)
	mux.HandleFunc("POST /v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/revisions", server.createServiceRevision)
	mux.HandleFunc("POST /v1/isolation-domains/{isolationDomainId}/service-revisions/{revisionId}/actions/publish", server.publishServiceRevision)
	mux.HandleFunc("PUT /v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/aliases/{alias}", server.assignServiceAlias)
	mux.HandleFunc("POST /v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/invocations", server.invokeAgentService)
	mux.HandleFunc("GET /v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}", server.getInvocation)
	mux.HandleFunc("POST /v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/actions/cancel", server.cancelInvocation)
	mux.HandleFunc("GET /v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/events", server.streamInvocationEvents)
	mux.HandleFunc("GET /v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/artifacts/{artifactId}", server.getInvocationArtifact)
	mux.HandleFunc("GET /v1/isolation-domains/{isolationDomainId}/operations/{operationId}", server.getOperation)
	return mux
}

func (server *DurableServer) ready(response http.ResponseWriter, request *http.Request) {
	if err := server.repository.Ready(request.Context()); err != nil {
		writeJSON(response, http.StatusServiceUnavailable, healthResponse{Status: "unavailable"})
		return
	}
	writeJSON(response, http.StatusOK, healthResponse{Status: "ok"})
}

func (server *DurableServer) createAgentService(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(domainID, actorID, correlationID string, body []byte) (persistence.CommandResult, error) {
		input, apiError := decodeBody[createAgentServiceRequest](body)
		if apiError != nil {
			return encodedError(http.StatusBadRequest, *apiError)
		}
		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" || len(input.Name) > 128 {
			return encodedResult(invalidField("name", "Name must contain between 1 and 128 characters."))
		}
		return server.repository.CreateService(request.Context(), commandIdempotency(request, domainID, body), persistence.CreateServiceInput{
			ID: identity.New("svc"), Name: input.Name, Description: input.Description,
			ActorID: actorID, CorrelationID: correlationID,
		})
	})
}

func (server *DurableServer) createServiceRevision(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(domainID, actorID, correlationID string, body []byte) (persistence.CommandResult, error) {
		input, apiError := decodeBody[createServiceRevisionRequest](body)
		if apiError != nil {
			return encodedError(http.StatusBadRequest, *apiError)
		}
		if strings.TrimSpace(input.RuntimeProfile) == "" || duplicateString(input.RequiredCapabilities) {
			return encodedResult(invalidField("runtimeProfile", "Runtime profile is required and capabilities must be unique."))
		}
		return server.repository.CreateRevision(request.Context(), commandIdempotency(request, domainID, body), persistence.CreateRevisionInput{
			ID: identity.New("rev"), ServiceID: request.PathValue("serviceId"),
			RuntimeProfile: input.RuntimeProfile, RequiredCapabilities: input.RequiredCapabilities,
			InputSchema: input.InputSchema, OutputSchema: input.OutputSchema,
			ActorID: actorID, CorrelationID: correlationID,
		})
	})
}

func (server *DurableServer) publishServiceRevision(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(domainID, actorID, correlationID string, body []byte) (persistence.CommandResult, error) {
		input, apiError := decodeBody[publishServiceRevisionRequest](body)
		if apiError != nil {
			return encodedError(http.StatusBadRequest, *apiError)
		}
		return server.repository.AcceptPublication(request.Context(), commandIdempotency(request, domainID, body), persistence.AcceptPublicationInput{
			RevisionID: request.PathValue("revisionId"), ExpectedVersion: input.ExpectedVersion,
			ActorID: actorID, CorrelationID: correlationID, Deadline: time.Now().UTC().Add(durableOperationDeadline),
		}, reference.Capabilities())
	})
}

func (server *DurableServer) assignServiceAlias(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(domainID, actorID, correlationID string, body []byte) (persistence.CommandResult, error) {
		input, apiError := decodeBody[assignServiceAliasRequest](body)
		if apiError != nil {
			return encodedError(http.StatusBadRequest, *apiError)
		}
		alias := request.PathValue("alias")
		if len(alias) > 63 || !aliasPattern.MatchString(alias) {
			return encodedResult(invalidField("alias", "Alias is not valid."))
		}
		return server.repository.AssignAlias(request.Context(), commandIdempotency(request, domainID, body), persistence.AssignAliasInput{
			ID: identity.New("als"), ServiceID: request.PathValue("serviceId"), Name: alias,
			RevisionID: input.RevisionID, ExpectedVersion: input.ExpectedVersion,
			ActorID: actorID, CorrelationID: correlationID,
		})
	})
}

func (server *DurableServer) invokeAgentService(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(domainID, actorID, correlationID string, body []byte) (persistence.CommandResult, error) {
		input, apiError := decodeBody[invokeAgentServiceRequest](body)
		if apiError != nil {
			return encodedError(http.StatusBadRequest, *apiError)
		}
		if !aliasPattern.MatchString(input.Alias) || input.Input == nil {
			return encodedResult(invalidField("alias", "Alias and input are required."))
		}
		return server.repository.AcceptInvocation(request.Context(), commandIdempotency(request, domainID, body), persistence.AcceptInvocationInput{
			ID: identity.New("inv"), ServiceID: request.PathValue("serviceId"), Alias: input.Alias,
			Input: input.Input, ActorID: actorID, CorrelationID: correlationID,
			Deadline: time.Now().UTC().Add(durableOperationDeadline),
		})
	})
}

func (server *DurableServer) cancelInvocation(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(domainID, actorID, correlationID string, body []byte) (persistence.CommandResult, error) {
		input, apiError := decodeBody[cancelInvocationRequest](body)
		if apiError != nil {
			return encodedError(http.StatusBadRequest, *apiError)
		}
		if len(input.Reason) > 512 {
			return encodedResult(invalidField("reason", "Cancellation reason must not exceed 512 characters."))
		}
		return server.repository.AcceptCancellation(request.Context(), commandIdempotency(request, domainID, body), persistence.AcceptCancellationInput{
			InvocationID: request.PathValue("invocationId"), ActorID: actorID, CorrelationID: correlationID,
		})
	})
}

func (server *DurableServer) getInvocation(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	value, err := server.repository.GetInvocation(request.Context(), domainID, request.PathValue("invocationId"))
	if err != nil {
		server.writeReadError(response, err, "Invocation was not found.")
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func (server *DurableServer) getOperation(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	value, err := server.repository.GetOperation(request.Context(), domainID, request.PathValue("operationId"))
	if err != nil {
		server.writeReadError(response, err, "Operation was not found.")
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func (server *DurableServer) getInvocationArtifact(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	value, err := server.repository.GetArtifact(
		request.Context(), domainID, request.PathValue("invocationId"), request.PathValue("artifactId"),
	)
	if err != nil {
		server.writeReadError(response, err, "Artifact was not found.")
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func (server *DurableServer) streamInvocationEvents(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	cursor, err := parseCursor(request.Header.Get("Last-Event-ID"))
	if err != nil {
		status, value := invalidField("Last-Event-ID", "Event cursor must be a non-negative integer.")
		writeJSON(response, status, value)
		return
	}
	events, err := server.repository.ListEvents(request.Context(), domainID, request.PathValue("invocationId"), cursor)
	if err != nil {
		server.writeReadError(response, err, "Invocation was not found.")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("X-Accel-Buffering", "no")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(http.StatusOK)
	for _, event := range events {
		encoded, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			return
		}
		_, _ = fmt.Fprintf(response, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, encoded)
	}
}

func (server *DurableServer) mutate(
	response http.ResponseWriter,
	request *http.Request,
	command func(string, string, string, []byte) (persistence.CommandResult, error),
) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(response, http.StatusUnsupportedMediaType, ErrorEnvelope{Error: safeError("UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json.", false)})
		return
	}
	if !idempotencyKeyPattern.MatchString(request.Header.Get("Idempotency-Key")) {
		status, value := invalidField("Idempotency-Key", "A valid idempotency key is required.")
		writeJSON(response, status, value)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maximumRequestBytes))
	if err != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: safeError("INVALID_REQUEST", "Request body is invalid or too large.", false)})
		return
	}
	// Authentication is introduced at the policy boundary. Until then, do not
	// accept caller-supplied audit identities that could forge attribution.
	actorID := "reference-api"
	correlationID := identity.New("cor")
	result, err := command(domainID, actorID, correlationID, body)
	if err != nil {
		server.writeCommandError(response, err, correlationID)
		return
	}
	writeRawJSON(response, result.Status, result.Body)
}

func commandIdempotency(request *http.Request, domainID string, body []byte) persistence.Idempotency {
	return persistence.Idempotency{
		IsolationDomainID: domainID, Method: request.Method, Path: request.URL.EscapedPath(),
		Key: request.Header.Get("Idempotency-Key"), RequestDigest: sha256.Sum256(body),
	}
}

func (server *DurableServer) writeCommandError(response http.ResponseWriter, err error, correlationID string) {
	var problem *persistence.DomainError
	if errors.As(err, &problem) {
		status := http.StatusConflict
		if problem.Code == "RESOURCE_NOT_FOUND" {
			status = http.StatusNotFound
		} else if problem.Code == "COMMAND_IN_PROGRESS" {
			status = http.StatusServiceUnavailable
		}
		writeJSON(response, status, ErrorEnvelope{Error: APIError{
			Code: problem.Code, Message: problem.Message, CorrelationID: correlationID, Retryable: problem.Retryable,
		}})
		return
	}
	writeJSON(response, http.StatusInternalServerError, ErrorEnvelope{Error: APIError{
		Code: "INTERNAL_ERROR", Message: "The command could not be committed.", CorrelationID: correlationID, Retryable: true,
	}})
}

func (server *DurableServer) writeReadError(response http.ResponseWriter, err error, message string) {
	if errors.Is(err, pgx.ErrNoRows) {
		status, value := notFound(message)
		writeJSON(response, status, value)
		return
	}
	writeJSON(response, http.StatusInternalServerError, ErrorEnvelope{Error: safeError("INTERNAL_ERROR", "The resource could not be read.", true)})
}

func encodedError(status int, apiError APIError) (persistence.CommandResult, error) {
	body, err := json.Marshal(ErrorEnvelope{Error: apiError})
	return persistence.CommandResult{Status: status, Body: body}, err
}

func encodedResult(status int, value any) (persistence.CommandResult, error) {
	body, err := json.Marshal(value)
	return persistence.CommandResult{Status: status, Body: body}, err
}
