package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/asabla/dataground/internal/reference"
	"github.com/jackc/pgx/v5"
)

const durableOperationDeadline = 15 * time.Minute

type DurableServer struct {
	repository     *persistence.Repository
	dispatchTarget *persistence.InvocationDispatchTarget
	approvals      durableInvocationApprovalResolver
}

type durableInvocationApprovalResolver interface {
	ResolveCommand(
		context.Context,
		persistence.Idempotency,
		persistence.InvocationRuntimeApprovalResolution,
	) (persistence.CommandResult, error)
}

func NewDurableHandler(
	repository *persistence.Repository,
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
) (http.Handler, error) {
	return newDurableHandler(repository, authenticator, authorizer, nil, nil, nil)
}

func NewGovernedDurableHandler(
	ctx context.Context,
	repository *persistence.Repository,
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	dispatchTarget persistence.InvocationDispatchTarget,
) (http.Handler, error) {
	if !dispatchTarget.Valid() {
		return nil, errors.New("governed invocation dispatch target is invalid")
	}
	if err := repository.RequireInvocationDispatchTarget(ctx, dispatchTarget); err != nil {
		return nil, err
	}
	return newDurableHandler(repository, authenticator, authorizer, nil, nil, &dispatchTarget)
}

func NewDurableDPoPBoundHandler(
	repository *persistence.Repository,
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	binder *DPoPRequestBinder,
) (http.Handler, error) {
	if binder == nil {
		return nil, errors.New("DPoP request binder is required")
	}
	return newDurableHandler(repository, authenticator, authorizer, binder, nil, nil)
}

func NewDurableRateLimitedDPoPHandler(
	repository *persistence.Repository,
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	binder *DPoPRequestBinder,
	rateLimiter AuthenticationRateLimiter,
) (http.Handler, error) {
	if binder == nil {
		return nil, errors.New("DPoP request binder is required")
	}
	if rateLimiter == nil || isNilInterface(rateLimiter) {
		return nil, errors.New("authentication rate limiter is required")
	}
	return newDurableHandler(repository, authenticator, authorizer, binder, rateLimiter, nil)
}

func newDurableHandler(
	repository *persistence.Repository,
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	binder *DPoPRequestBinder,
	rateLimiter AuthenticationRateLimiter,
	dispatchTarget *persistence.InvocationDispatchTarget,
) (http.Handler, error) {
	if repository == nil || !repository.Configured() {
		return nil, errors.New("durable repository is required")
	}
	protected, err := newProtectedRoute(authenticator, authorizer, binder, rateLimiter)
	if err != nil {
		return nil, err
	}
	if dispatchTarget != nil && !dispatchTarget.Valid() {
		return nil, errors.New("governed invocation dispatch target is invalid")
	}
	if dispatchTarget != nil {
		clonedTarget := *dispatchTarget
		dispatchTarget = &clonedTarget
	}
	policySource, err := reconcile.NewDurableInvocationAuthorizationPolicySource(repository)
	if err != nil {
		return nil, err
	}
	invocationAuthorizer, err := reconcile.NewAuditedCedarInvocationAuthorizer(
		policySource,
		repository,
	)
	if err != nil {
		return nil, err
	}
	approvalResolver, err := reconcile.NewInvocationApprovalResolver(
		repository,
		invocationAuthorizer,
	)
	if err != nil {
		return nil, err
	}
	server := &DurableServer{
		repository: repository, dispatchTarget: dispatchTarget, approvals: approvalResolver,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", healthHandler)
	mux.HandleFunc("GET /readyz", server.ready)
	mux.Handle("POST /v1/isolation-domains/{isolationDomainId}/agent-services", protected(
		authz.CreateAgentService, authz.IsolationDomain, "", server.createAgentService,
	))
	mux.Handle("POST /v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/revisions", protected(
		authz.CreateServiceRevision, authz.AgentService, "serviceId", server.createServiceRevision,
	))
	mux.Handle("POST /v1/isolation-domains/{isolationDomainId}/service-revisions/{revisionId}/actions/publish", protected(
		authz.PublishServiceRevision, authz.ServiceRevision, "revisionId", server.publishServiceRevision,
	))
	mux.Handle("PUT /v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/aliases/{alias}", protected(
		authz.AssignServiceAlias, authz.AgentService, "serviceId", server.assignServiceAlias,
	))
	mux.Handle("POST /v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/invocations", protected(
		authz.InvokeAgentService, authz.AgentService, "serviceId", server.invokeAgentService,
	))
	mux.Handle("GET /v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}", protected(
		authz.ReadInvocation, authz.Invocation, "invocationId", server.getInvocation,
	))
	mux.Handle("POST /v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/actions/cancel", protected(
		authz.CancelInvocation, authz.Invocation, "invocationId", server.cancelInvocation,
	))
	mux.Handle("POST /v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/approvals/{approvalId}", protected(
		authz.ResolveInvocationApproval, authz.InvocationApproval, "approvalId", server.resolveInvocationApproval,
	))
	mux.Handle("GET /v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/events", protected(
		authz.ReadInvocationEvents, authz.Invocation, "invocationId", server.streamInvocationEvents,
	))
	mux.Handle("GET /v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/artifacts/{artifactId}", protected(
		authz.ReadInvocationArtifact, authz.Artifact, "artifactId", server.getInvocationArtifact,
	))
	mux.Handle("GET /v1/isolation-domains/{isolationDomainId}/operations/{operationId}", protected(
		authz.ReadOperation, authz.Operation, "operationId", server.getOperation,
	))
	return mux, nil
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
		return server.repository.CreateService(request.Context(), commandIdempotency(request, domainID, actorID, body), persistence.CreateServiceInput{
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
		return server.repository.CreateRevision(request.Context(), commandIdempotency(request, domainID, actorID, body), persistence.CreateRevisionInput{
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
		return server.repository.AcceptPublication(request.Context(), commandIdempotency(request, domainID, actorID, body), persistence.AcceptPublicationInput{
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
		return server.repository.AssignAlias(request.Context(), commandIdempotency(request, domainID, actorID, body), persistence.AssignAliasInput{
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
		return server.repository.AcceptInvocation(request.Context(), invocationCommandIdempotency(
			request, domainID, actorID, body, server.dispatchTarget,
		), persistence.AcceptInvocationInput{
			ID: identity.New("inv"), ServiceID: request.PathValue("serviceId"), Alias: input.Alias,
			Input: input.Input, ActorID: actorID, CorrelationID: correlationID,
			Deadline:       time.Now().UTC().Add(durableOperationDeadline),
			DispatchTarget: server.dispatchTarget,
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
		return server.repository.AcceptCancellation(request.Context(), commandIdempotency(request, domainID, actorID, body), persistence.AcceptCancellationInput{
			InvocationID: request.PathValue("invocationId"), ActorID: actorID, CorrelationID: correlationID,
		})
	})
}

func (server *DurableServer) resolveInvocationApproval(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(domainID, actorID, correlationID string, body []byte) (persistence.CommandResult, error) {
		input, apiError := decodeBody[resolveInvocationApprovalRequest](body)
		if apiError != nil {
			return encodedError(http.StatusBadRequest, *apiError)
		}
		if input.ExpectedVersion != 1 || (input.Decision != "approve" && input.Decision != "deny") {
			return encodedResult(invalidField(
				"decision",
				"Decision must be approve or deny and expectedVersion must be 1.",
			))
		}
		result, err := server.approvals.ResolveCommand(
			request.Context(),
			commandIdempotency(request, domainID, actorID, body),
			persistence.InvocationRuntimeApprovalResolution{
				IsolationDomainID: domainID,
				InvocationID:      request.PathValue("invocationId"),
				ApprovalID:        request.PathValue("approvalId"),
				ExpectedVersion:   input.ExpectedVersion,
				Decision:          input.Decision,
				ActorID:           actorID,
				CorrelationID:     correlationID,
			},
		)
		if err != nil {
			return persistence.CommandResult{}, invocationApprovalCommandError(err)
		}
		return result, nil
	})
}

func invocationApprovalCommandError(err error) error {
	var problem *persistence.DomainError
	if errors.As(err, &problem) {
		return problem
	}
	switch {
	case errors.Is(err, persistence.ErrInvocationRuntimeApprovalMissing):
		return &persistence.DomainError{
			Code: "RESOURCE_NOT_FOUND", Message: "Invocation approval was not found.",
		}
	case errors.Is(err, persistence.ErrInvocationRuntimeApprovalConflict):
		return &persistence.DomainError{
			Code: "INVOCATION_APPROVAL_CONFLICT", Message: "Invocation approval cannot be resolved in its current state.",
		}
	case errors.Is(err, persistence.ErrInvocationRuntimeApprovalInvalid),
		errors.Is(err, reconcile.ErrInvocationApprovalInvalid),
		errors.Is(err, reconcile.ErrInvocationAuthorizationInvalid):
		return &persistence.DomainError{
			Code: "INVALID_INVOCATION_APPROVAL", Message: "Invocation approval resolution is invalid.",
		}
	case errors.Is(err, reconcile.ErrInvocationApprovalDenied):
		return &persistence.DomainError{
			Code: "INVOCATION_APPROVAL_FORBIDDEN", Message: "The invocation policy denied this approval resolution.",
		}
	default:
		return &persistence.DomainError{
			Code:      "INVOCATION_APPROVAL_UNAVAILABLE",
			Message:   "Invocation approval resolution is temporarily unavailable.",
			Retryable: true,
		}
	}
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
	actorID := authenticatedActorID(request)
	if actorID == "" {
		writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeError("AUTHENTICATION_UNAVAILABLE", "Authentication is temporarily unavailable.", true)})
		return
	}
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
	correlationID := authenticatedCorrelationID(request)
	if correlationID == "" {
		writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeError(
			"AUTHORIZATION_UNAVAILABLE",
			"Authorization is temporarily unavailable.",
			true,
		)})
		return
	}
	result, err := command(domainID, actorID, correlationID, body)
	if err != nil {
		server.writeCommandError(response, err, correlationID)
		return
	}
	writeRawJSON(response, result.Status, result.Body)
}

func commandIdempotency(request *http.Request, domainID, actorID string, body []byte) persistence.Idempotency {
	return persistence.Idempotency{
		IsolationDomainID: domainID, Method: request.Method, Path: request.URL.EscapedPath(),
		Key: request.Header.Get("Idempotency-Key"), RequestDigest: authenticatedRequestDigest(actorID, body),
	}
}

func invocationCommandIdempotency(
	request *http.Request,
	domainID string,
	actorID string,
	body []byte,
	dispatchTarget *persistence.InvocationDispatchTarget,
) persistence.Idempotency {
	idempotency := commandIdempotency(request, domainID, actorID, body)
	if dispatchTarget == nil {
		return idempotency
	}
	digest := sha256.New()
	_, _ = digest.Write(idempotency.RequestDigest[:])
	for _, value := range []string{
		dispatchTarget.IsolationDomainID,
		dispatchTarget.ServiceID,
		dispatchTarget.RevisionID,
		dispatchTarget.RuntimeProfile,
	} {
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(value))
	}
	copy(idempotency.RequestDigest[:], digest.Sum(nil))
	return idempotency
}

func (server *DurableServer) writeCommandError(response http.ResponseWriter, err error, correlationID string) {
	var problem *persistence.DomainError
	if errors.As(err, &problem) {
		status := http.StatusConflict
		if problem.Code == "RESOURCE_NOT_FOUND" {
			status = http.StatusNotFound
		} else if problem.Code == "COMMAND_IN_PROGRESS" ||
			problem.Code == "INVOCATION_APPROVAL_UNAVAILABLE" {
			status = http.StatusServiceUnavailable
		} else if problem.Code == "INVOCATION_APPROVAL_FORBIDDEN" {
			status = http.StatusForbidden
		} else if problem.Code == "INVALID_INVOCATION_APPROVAL" {
			status = http.StatusBadRequest
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
