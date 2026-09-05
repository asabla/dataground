package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/reference"
)

const maximumRequestBytes = 1 << 20

var (
	isolationDomainPattern = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
	idempotencyKeyPattern  = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
	aliasPattern           = regexp.MustCompile(`^[a-z](?:[a-z0-9-]*[a-z0-9])?$`)
)

type healthResponse struct {
	Status string `json:"status"`
}

type storedResponse struct {
	RequestDigest [sha256.Size]byte
	Status        int
	Body          []byte
}

type Server struct {
	mu                   sync.RWMutex
	services             map[string]AgentService
	revisions            map[string]ServiceRevision
	revisionCounts       map[string]int
	aliases              map[string]ServiceAlias
	invocations          map[string]Invocation
	operations           map[string]Operation
	events               map[string][]EventEnvelope
	artifacts            map[string]ArtifactDescriptor
	idempotencyResponses map[string]storedResponse
	now                  func() time.Time
}

func NewHandler(authenticator authn.Authenticator, authorizer authz.Authorizer) (http.Handler, error) {
	return NewServer().Handler(authenticator, authorizer)
}

func NewDPoPBoundHandler(
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	binder *DPoPRequestBinder,
) (http.Handler, error) {
	return NewServer().DPoPBoundHandler(authenticator, authorizer, binder)
}

func NewRateLimitedDPoPHandler(
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	binder *DPoPRequestBinder,
	rateLimiter AuthenticationRateLimiter,
) (http.Handler, error) {
	return NewServer().RateLimitedDPoPHandler(authenticator, authorizer, binder, rateLimiter)
}

func NewServer() *Server {
	return &Server{
		services:             make(map[string]AgentService),
		revisions:            make(map[string]ServiceRevision),
		revisionCounts:       make(map[string]int),
		aliases:              make(map[string]ServiceAlias),
		invocations:          make(map[string]Invocation),
		operations:           make(map[string]Operation),
		events:               make(map[string][]EventEnvelope),
		artifacts:            make(map[string]ArtifactDescriptor),
		idempotencyResponses: make(map[string]storedResponse),
		now:                  func() time.Time { return time.Now().UTC() },
	}
}

func (server *Server) Handler(
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
) (http.Handler, error) {
	return server.handler(authenticator, authorizer, nil, nil)
}

func (server *Server) DPoPBoundHandler(
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	binder *DPoPRequestBinder,
) (http.Handler, error) {
	if binder == nil {
		return nil, errors.New("DPoP request binder is required")
	}
	return server.handler(authenticator, authorizer, binder, nil)
}

func (server *Server) RateLimitedDPoPHandler(
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
	return server.handler(authenticator, authorizer, binder, rateLimiter)
}

func (server *Server) handler(
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	binder *DPoPRequestBinder,
	rateLimiter AuthenticationRateLimiter,
) (http.Handler, error) {
	protected, err := newProtectedRoute(authenticator, authorizer, binder, rateLimiter)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", healthHandler)
	mux.HandleFunc("GET /readyz", healthHandler)
	mux.Handle("GET /v1/isolation-domains/{isolationDomainId}/agent-services", protected(
		authz.ListAgentServices, authz.IsolationDomain, "", server.listAgentServices,
	))
	mux.Handle("POST /v1/isolation-domains/{isolationDomainId}/agent-services", protected(
		authz.CreateAgentService, authz.IsolationDomain, "", server.createAgentService,
	))
	mux.Handle("GET /v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/revisions", protected(
		authz.ListServiceRevisions, authz.AgentService, "serviceId", server.listServiceRevisions,
	))
	mux.Handle("POST /v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/revisions", protected(
		authz.CreateServiceRevision, authz.AgentService, "serviceId", server.createServiceRevision,
	))
	mux.Handle("GET /v1/isolation-domains/{isolationDomainId}/service-revisions/{revisionId}", protected(
		authz.ReadServiceRevision, authz.ServiceRevision, "revisionId", server.getServiceRevision,
	))
	mux.Handle("POST /v1/isolation-domains/{isolationDomainId}/service-revisions/{revisionId}/actions/publish", protected(
		authz.PublishServiceRevision, authz.ServiceRevision, "revisionId", server.publishServiceRevision,
	))
	mux.Handle("POST /v1/isolation-domains/{isolationDomainId}/service-revisions/{revisionId}/actions/retire", protected(
		authz.RetireServiceRevision, authz.ServiceRevision, "revisionId", server.retireServiceRevision,
	))
	mux.Handle("GET /v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/aliases", protected(
		authz.ListServiceAliases, authz.AgentService, "serviceId", server.listServiceAliases,
	))
	mux.Handle("GET /v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/aliases/{alias}", protected(
		authz.ReadServiceAlias, authz.AgentService, "serviceId", server.getServiceAlias,
	))
	mux.Handle("PUT /v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/aliases/{alias}", protected(
		authz.AssignServiceAlias, authz.AgentService, "serviceId", server.assignServiceAlias,
	))
	mux.Handle("POST /v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/aliases/{alias}/actions/withdraw", protected(
		authz.WithdrawServiceAlias, authz.AgentService, "serviceId", server.withdrawServiceAlias,
	))
	mux.Handle("GET /v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/invocations", protected(
		authz.ListInvocations, authz.AgentService, "serviceId", server.listInvocations,
	))
	mux.Handle("POST /v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/invocations", protected(
		authz.InvokeAgentService, authz.AgentService, "serviceId", server.invokeAgentService,
	))
	mux.Handle("GET /v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}", protected(
		authz.ReadInvocation, authz.Invocation, "invocationId", server.getInvocation,
	))
	mux.Handle("GET /v1/isolation-domains/{isolationDomainId}/operations/{operationId}", protected(
		authz.ReadOperation, authz.Operation, "operationId", server.getOperation,
	))
	mux.Handle("POST /v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/actions/cancel", protected(
		authz.CancelInvocation, authz.Invocation, "invocationId", server.cancelInvocation,
	))
	mux.Handle("GET /v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/approvals/{approvalId}", protected(
		authz.ReadInvocationApproval, authz.InvocationApproval, "approvalId", server.getInvocationApproval,
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
	return mux, nil
}

func healthHandler(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, healthResponse{Status: "ok"})
}

func (server *Server) listAgentServices(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	limit, cursor, err := parseServiceListQuery(request.URL.Query())
	if err != nil {
		status, body := invalidField("query", "Service-list limit or cursor is invalid.")
		writeJSON(response, status, body)
		return
	}
	server.mu.RLock()
	services := make([]AgentService, 0, len(server.services))
	for _, service := range server.services {
		if service.Metadata.IsolationDomainID == domainID {
			services = append(services, service)
		}
	}
	server.mu.RUnlock()
	sort.Slice(services, func(left, right int) bool {
		if services[left].Metadata.CreatedAt.Equal(services[right].Metadata.CreatedAt) {
			return services[left].Metadata.ID > services[right].Metadata.ID
		}
		return services[left].Metadata.CreatedAt.After(services[right].Metadata.CreatedAt)
	})
	if cursor != nil {
		services = slicesAfterServiceCursor(services, *cursor)
	}
	hasMore := len(services) > limit
	if hasMore {
		services = services[:limit]
	}
	page := agentServicePage{Items: services}
	if hasMore {
		page.NextCursor, err = encodeServiceListCursor(services[len(services)-1])
		if err != nil {
			writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeError(
				"SERVICE_LIST_UNAVAILABLE", "Agent services are temporarily unavailable.", true,
			)})
			return
		}
	}
	writeJSON(response, http.StatusOK, page)
}

func slicesAfterServiceCursor(services []AgentService, cursor serviceListCursor) []AgentService {
	for index, service := range services {
		createdAt := service.Metadata.CreatedAt
		if createdAt.Before(cursor.CreatedAt) ||
			(createdAt.Equal(cursor.CreatedAt) && service.Metadata.ID < cursor.ID) {
			return services[index:]
		}
	}
	return services[:0]
}

func (server *Server) createAgentService(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(actorID string, body []byte) (int, any) {
		domainID, apiError := isolationDomain(request)
		if apiError != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *apiError}
		}
		input, apiError := decodeBody[createAgentServiceRequest](body)
		if apiError != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *apiError}
		}
		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" || len(input.Name) > 128 {
			return invalidField("name", "Name must contain between 1 and 128 characters.")
		}
		if len(input.Description) > 2048 {
			return invalidField("description", "Description must not exceed 2048 characters.")
		}

		now := server.now()
		service := AgentService{
			Metadata:    newMetadata(newID("svc"), domainID, actorID, now),
			Name:        input.Name,
			Description: input.Description,
		}
		server.services[resourceKey(domainID, service.Metadata.ID)] = service
		return http.StatusCreated, service
	})
}

func (server *Server) listServiceRevisions(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	limit, cursor, err := parseRevisionListQuery(request.URL.Query())
	if err != nil {
		status, body := invalidField("query", "Revision-list limit or cursor is invalid.")
		writeJSON(response, status, body)
		return
	}
	serviceID := request.PathValue("serviceId")
	server.mu.RLock()
	_, serviceExists := server.services[resourceKey(domainID, serviceID)]
	revisions := make([]ServiceRevision, 0, len(server.revisions))
	if serviceExists {
		for _, revision := range server.revisions {
			if revision.Metadata.IsolationDomainID == domainID && revision.ServiceID == serviceID {
				revisions = append(revisions, revision)
			}
		}
	}
	server.mu.RUnlock()
	if !serviceExists {
		status, body := notFound("Agent service was not found.")
		writeJSON(response, status, body)
		return
	}
	sort.Slice(revisions, func(left, right int) bool {
		if revisions[left].RevisionNumber == revisions[right].RevisionNumber {
			return revisions[left].Metadata.ID > revisions[right].Metadata.ID
		}
		return revisions[left].RevisionNumber > revisions[right].RevisionNumber
	})
	if cursor != nil {
		revisions = slicesAfterRevisionCursor(revisions, *cursor)
	}
	hasMore := len(revisions) > limit
	if hasMore {
		revisions = revisions[:limit]
	}
	page := serviceRevisionPage{Items: revisions}
	if hasMore {
		page.NextCursor, err = encodeRevisionListCursor(revisions[len(revisions)-1])
		if err != nil {
			writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeError(
				"REVISION_LIST_UNAVAILABLE", "Service revisions are temporarily unavailable.", true,
			)})
			return
		}
	}
	writeJSON(response, http.StatusOK, page)
}

func slicesAfterRevisionCursor(revisions []ServiceRevision, cursor revisionListCursor) []ServiceRevision {
	for index, revision := range revisions {
		if revision.RevisionNumber < cursor.RevisionNumber ||
			(revision.RevisionNumber == cursor.RevisionNumber && revision.Metadata.ID < cursor.ID) {
			return revisions[index:]
		}
	}
	return revisions[:0]
}

func (server *Server) createServiceRevision(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(actorID string, body []byte) (int, any) {
		domainID, apiError := isolationDomain(request)
		if apiError != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *apiError}
		}
		serviceID := request.PathValue("serviceId")
		if _, exists := server.services[resourceKey(domainID, serviceID)]; !exists {
			return notFound("Agent service was not found.")
		}
		input, apiError := decodeBody[createServiceRevisionRequest](body)
		if apiError != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *apiError}
		}
		if strings.TrimSpace(input.RuntimeProfile) == "" || len(input.RuntimeProfile) > 128 {
			return invalidField("runtimeProfile", "Runtime profile must contain between 1 and 128 characters.")
		}
		if duplicateString(input.RequiredCapabilities) {
			return invalidField("requiredCapabilities", "Required capabilities must be unique.")
		}
		for _, capability := range input.RequiredCapabilities {
			if strings.TrimSpace(capability) == "" || len(capability) > 128 {
				return invalidField("requiredCapabilities", "Capability names must contain between 1 and 128 characters.")
			}
		}

		serviceKey := resourceKey(domainID, serviceID)
		server.revisionCounts[serviceKey]++
		now := server.now()
		revision := ServiceRevision{
			Metadata:             newMetadata(newID("rev"), domainID, actorID, now),
			ServiceID:            serviceID,
			RevisionNumber:       server.revisionCounts[serviceKey],
			State:                "draft",
			RuntimeProfile:       input.RuntimeProfile,
			RequiredCapabilities: append([]string{}, input.RequiredCapabilities...),
			InputSchema:          input.InputSchema,
			OutputSchema:         input.OutputSchema,
		}
		server.revisions[resourceKey(domainID, revision.Metadata.ID)] = revision
		return http.StatusCreated, revision
	})
}

func (server *Server) publishServiceRevision(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(actorID string, body []byte) (int, any) {
		domainID, apiError := isolationDomain(request)
		if apiError != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *apiError}
		}
		input, apiError := decodeBody[publishServiceRevisionRequest](body)
		if apiError != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *apiError}
		}
		key := resourceKey(domainID, request.PathValue("revisionId"))
		revision, exists := server.revisions[key]
		if !exists {
			return notFound("Service revision was not found.")
		}
		if revision.Metadata.Version != input.ExpectedVersion {
			return conflict("VERSION_CONFLICT", "Revision version did not match.")
		}
		if revision.State != "draft" {
			return conflict("REVISION_IMMUTABLE", "Only a draft revision can be published.")
		}
		if revision.RuntimeProfile != "reference/v1" {
			return conflict("RUNTIME_PROFILE_UNAVAILABLE", "Runtime profile is not available in the reference server.")
		}
		capabilities := reference.Capabilities()
		for _, capability := range revision.RequiredCapabilities {
			if capabilities[capability] != "supported" {
				return conflict("REQUIRED_CAPABILITY_UNAVAILABLE", "A required runtime capability is unavailable.")
			}
		}

		if problem := domain.ValidateRevisionSchemas(revision.InputSchema, revision.OutputSchema); problem != nil {
			problem.CorrelationID = authenticatedCorrelationID(request)
			return http.StatusConflict, ErrorEnvelope{Error: *problem}
		}

		now := server.now()
		revision.State = "published"
		revision.PublishedAt = &now
		revision.Metadata.Version++
		revision.Metadata.UpdatedAt = now
		server.revisions[key] = revision
		return http.StatusOK, revision
	})
}

func (server *Server) assignServiceAlias(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(actorID string, body []byte) (int, any) {
		domainID, apiError := isolationDomain(request)
		if apiError != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *apiError}
		}
		serviceID := request.PathValue("serviceId")
		if _, exists := server.services[resourceKey(domainID, serviceID)]; !exists {
			return notFound("Agent service was not found.")
		}
		aliasName := request.PathValue("alias")
		if len(aliasName) > 63 || !aliasPattern.MatchString(aliasName) {
			return invalidField("alias", "Alias is not valid.")
		}
		input, apiError := decodeBody[assignServiceAliasRequest](body)
		if apiError != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *apiError}
		}
		revision, exists := server.revisions[resourceKey(domainID, input.RevisionID)]
		if !exists || revision.ServiceID != serviceID {
			return notFound("Published service revision was not found.")
		}
		if revision.State != "published" {
			return conflict("REVISION_NOT_PUBLISHED", "Alias target must be published.")
		}

		key := aliasKey(domainID, serviceID, aliasName)
		current, exists := server.aliases[key]
		now := server.now()
		if exists {
			requiredVersion := current.Metadata.Version
			if current.WithdrawnAt != nil {
				requiredVersion = 0
			}
			if (input.ExpectedVersion == nil && requiredVersion != 0) || (input.ExpectedVersion != nil && *input.ExpectedVersion != requiredVersion) {
				return conflict("VERSION_CONFLICT", "Alias version did not match.")
			}
			current.RevisionID = input.RevisionID
			current.WithdrawnAt = nil
			current.Metadata.Generation++
			current.Metadata.Version++
			current.Metadata.UpdatedAt = now
			server.aliases[key] = current
			return http.StatusOK, current
		}
		if input.ExpectedVersion != nil && *input.ExpectedVersion != 0 {
			return conflict("VERSION_CONFLICT", "A new alias expects version zero.")
		}
		alias := ServiceAlias{
			Metadata:   newMetadata(newID("als"), domainID, actorID, now),
			ServiceID:  serviceID,
			Name:       aliasName,
			RevisionID: input.RevisionID,
		}
		server.aliases[key] = alias
		return http.StatusOK, alias
	})
}

func (server *Server) getServiceAlias(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	aliasName := request.PathValue("alias")
	if len(aliasName) > 63 || !aliasPattern.MatchString(aliasName) {
		status, body := invalidField("alias", "Alias is not valid.")
		writeJSON(response, status, body)
		return
	}
	serviceID := request.PathValue("serviceId")
	server.mu.RLock()
	_, serviceExists := server.services[resourceKey(domainID, serviceID)]
	alias, aliasExists := server.aliases[aliasKey(domainID, serviceID, aliasName)]
	server.mu.RUnlock()
	if !serviceExists {
		status, body := notFound("Agent service was not found.")
		writeJSON(response, status, body)
		return
	}
	if !aliasExists || alias.WithdrawnAt != nil {
		writeJSON(response, http.StatusNotFound, ErrorEnvelope{Error: safeError(
			"SERVICE_ALIAS_NOT_FOUND", "Service alias was not found.", false,
		)})
		return
	}
	writeJSON(response, http.StatusOK, alias)
}

func (server *Server) invokeAgentService(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(actorID string, body []byte) (int, any) {
		domainID, apiError := isolationDomain(request)
		if apiError != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *apiError}
		}
		serviceID := request.PathValue("serviceId")
		if _, exists := server.services[resourceKey(domainID, serviceID)]; !exists {
			return notFound("Agent service was not found.")
		}
		input, apiError := decodeBody[invokeAgentServiceRequest](body)
		if apiError != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *apiError}
		}
		if !aliasPattern.MatchString(input.Alias) || input.Input == nil {
			return invalidField("alias", "Alias and input are required.")
		}
		alias, exists := server.aliases[aliasKey(domainID, serviceID, input.Alias)]
		if !exists || alias.WithdrawnAt != nil {
			return notFound("Service alias was not found.")
		}
		revision, exists := server.revisions[resourceKey(domainID, alias.RevisionID)]
		if !exists || revision.State != "published" {
			return notFound("Published service revision was not found.")
		}
		if problem := domain.ValidateInvocationInput(revision.InputSchema, input.Input); problem != nil {
			problem.CorrelationID = authenticatedCorrelationID(request)
			status := http.StatusBadRequest
			if problem.Code == "REVISION_INPUT_SCHEMA_INVALID" {
				status = http.StatusConflict
			}
			return status, ErrorEnvelope{Error: *problem}
		}

		scenario := reference.ScenarioSuccess
		if value, exists := input.Input["scenario"]; exists {
			text, ok := value.(string)
			if !ok {
				return invalidField("input.scenario", "Reference scenario must be a string.")
			}
			scenario = text
		}
		fixture, err := reference.Load(scenario)
		if err != nil {
			return invalidField("input.scenario", "Reference scenario is not registered.")
		}
		normalized, err := reference.Normalize(fixture.Events)
		if err != nil {
			return http.StatusInternalServerError, ErrorEnvelope{Error: safeError("REFERENCE_FIXTURE_INVALID", "Reference runtime fixture could not be normalized.", false)}
		}

		now := server.now()
		invocationID := newID("inv")
		correlationID := authenticatedCorrelationID(request)
		if correlationID == "" {
			return http.StatusInternalServerError, ErrorEnvelope{Error: safeError(
				"AUTHORIZATION_UNAVAILABLE",
				"Authorization is temporarily unavailable.",
				true,
			)}
		}
		operationID := newID("op")
		invocation := Invocation{
			Metadata:      newMetadata(invocationID, domainID, actorID, now),
			ServiceID:     serviceID,
			RevisionID:    revision.Metadata.ID,
			Alias:         input.Alias,
			State:         "running",
			Input:         input.Input,
			CorrelationID: correlationID,
			OperationID:   operationID,
			ArtifactIDs:   []string{},
		}
		journal := make([]EventEnvelope, 0, len(normalized))
		for _, runtimeEvent := range normalized {
			eventTime := now.Add(time.Duration(runtimeEvent.Sequence-1) * time.Millisecond)
			payload := cloneMap(runtimeEvent.Payload)
			if runtimeEvent.Type == "artifact.available" {
				artifact := server.createArtifact(domainID, invocationID, eventTime, payload)
				invocation.ArtifactIDs = append(invocation.ArtifactIDs, artifact.Metadata.ID)
				payload = map[string]any{"artifactId": artifact.Metadata.ID, "descriptor": artifact}
			}
			journal = append(journal, EventEnvelope{
				SchemaVersion:     "dataground.event/v1",
				ID:                derivedID("evt", invocationID+":"+runtimeEvent.Key),
				IsolationDomainID: domainID,
				InvocationID:      invocationID,
				Sequence:          runtimeEvent.Sequence,
				Type:              runtimeEvent.Type,
				OccurredAt:        eventTime,
				RecordedAt:        eventTime,
				CorrelationID:     correlationID,
				ActorID:           "reference-runtime",
				ServiceID:         serviceID,
				RevisionID:        revision.Metadata.ID,
				Payload:           payload,
				Extensions:        runtimeEvent.Extensions,
			})
			applyEvent(&invocation, runtimeEvent.Type, payload, eventTime)
		}
		key := resourceKey(domainID, invocationID)
		server.invocations[key] = invocation
		server.events[key] = journal
		operationState := invocation.State
		if operationState == "running" {
			operationState = "observing"
		}
		server.operations[resourceKey(domainID, operationID)] = Operation{
			Metadata:            newMetadata(operationID, domainID, actorID, now),
			Kind:                "invocation-execution",
			Command:             "invoke",
			DesiredState:        "succeeded",
			ObservedState:       operationState,
			StateMachineVersion: 1,
			Attempt:             1,
			CorrelationID:       correlationID,
			DueAt:               &now,
			DeadlineAt:          &now,
			TerminalResult:      invocation.Result,
			Error:               invocation.Error,
		}
		return http.StatusAccepted, invocation
	})
}

func (server *Server) getInvocation(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	server.mu.RLock()
	invocation, exists := server.invocations[resourceKey(domainID, request.PathValue("invocationId"))]
	server.mu.RUnlock()
	if !exists {
		status, body := notFound("Invocation was not found.")
		writeJSON(response, status, body)
		return
	}
	writeJSON(response, http.StatusOK, invocation)
}

func (server *Server) getOperation(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	server.mu.RLock()
	operation, exists := server.operations[resourceKey(domainID, request.PathValue("operationId"))]
	server.mu.RUnlock()
	if !exists {
		status, body := notFound("Operation was not found.")
		writeJSON(response, status, body)
		return
	}
	writeJSON(response, http.StatusOK, operation)
}

func (server *Server) cancelInvocation(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(actorID string, body []byte) (int, any) {
		domainID, apiError := isolationDomain(request)
		if apiError != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *apiError}
		}
		input, apiError := decodeBody[cancelInvocationRequest](body)
		if apiError != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *apiError}
		}
		if len(input.Reason) > 512 {
			return invalidField("reason", "Cancellation reason must not exceed 512 characters.")
		}
		key := resourceKey(domainID, request.PathValue("invocationId"))
		invocation, exists := server.invocations[key]
		if !exists {
			return notFound("Invocation was not found.")
		}
		if invocation.State == "cancelled" {
			return http.StatusOK, invocation
		}
		if invocation.State == "succeeded" || invocation.State == "failed" {
			return conflict("INVOCATION_TERMINAL", "A completed invocation cannot be cancelled.")
		}

		now := server.now()
		sequence := uint64(len(server.events[key]) + 1)
		invocation.State = "cancelled"
		invocation.CompletedAt = &now
		invocation.Metadata.Generation++
		invocation.Metadata.Version++
		invocation.Metadata.UpdatedAt = now
		server.invocations[key] = invocation
		operation := server.operations[resourceKey(domainID, invocation.OperationID)]
		operation.Command = "cancel"
		operation.DesiredState = "cancelled"
		operation.ObservedState = "cancelled"
		operation.Metadata.Generation++
		operation.Metadata.Version++
		operation.Metadata.UpdatedAt = now
		server.operations[resourceKey(domainID, invocation.OperationID)] = operation
		server.events[key] = append(server.events[key], EventEnvelope{
			SchemaVersion:     "dataground.event/v1",
			ID:                derivedID("evt", invocation.Metadata.ID+":cancel:"+strconv.FormatUint(sequence, 10)),
			IsolationDomainID: domainID,
			InvocationID:      invocation.Metadata.ID,
			Sequence:          sequence,
			Type:              "lifecycle.cancelled",
			OccurredAt:        now,
			RecordedAt:        now,
			CorrelationID:     invocation.CorrelationID,
			ActorID:           actorID,
			ServiceID:         invocation.ServiceID,
			RevisionID:        invocation.RevisionID,
			Payload:           map[string]any{"reason": "caller requested cancellation"},
		})
		return http.StatusOK, invocation
	})
}

func (server *Server) resolveInvocationApproval(response http.ResponseWriter, request *http.Request) {
	server.mutate(response, request, func(_ string, body []byte) (int, any) {
		input, apiError := decodeBody[resolveInvocationApprovalRequest](body)
		if apiError != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *apiError}
		}
		if input.ExpectedVersion != 1 || (input.Decision != "approve" && input.Decision != "deny") {
			return invalidField(
				"decision",
				"Decision must be approve or deny and expectedVersion must be 1.",
			)
		}
		return notFound("Invocation approval was not found.")
	})
}

func (server *Server) getInvocationApproval(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	status, body := notFound("Invocation approval was not found.")
	writeJSON(response, status, body)
}

func (server *Server) streamInvocationEvents(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	cursor, err := parseCursor(request.Header.Get("Last-Event-ID"))
	if err != nil {
		status, body := invalidField("Last-Event-ID", "Event cursor must be a non-negative integer.")
		writeJSON(response, status, body)
		return
	}
	server.mu.RLock()
	journal, exists := server.events[resourceKey(domainID, request.PathValue("invocationId"))]
	journal = append([]EventEnvelope(nil), journal...)
	server.mu.RUnlock()
	if !exists {
		status, body := notFound("Invocation was not found.")
		writeJSON(response, status, body)
		return
	}

	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("X-Accel-Buffering", "no")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(http.StatusOK)
	for _, event := range journal {
		if event.Sequence <= cursor {
			continue
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(response, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, encoded)
	}
}

func (server *Server) getInvocationArtifact(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	server.mu.RLock()
	artifact, exists := server.artifacts[artifactKey(domainID, request.PathValue("invocationId"), request.PathValue("artifactId"))]
	server.mu.RUnlock()
	if !exists {
		status, body := notFound("Artifact was not found.")
		writeJSON(response, status, body)
		return
	}
	writeJSON(response, http.StatusOK, artifact)
}

func (server *Server) mutate(response http.ResponseWriter, request *http.Request, mutation func(string, []byte) (int, any)) {
	actorID := authenticatedActorID(request)
	if actorID == "" {
		writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeError("AUTHENTICATION_UNAVAILABLE", "Authentication is temporarily unavailable.", true)})
		return
	}
	if _, apiError := isolationDomain(request); apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(response, http.StatusUnsupportedMediaType, ErrorEnvelope{Error: safeError("UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json.", false)})
		return
	}
	key := request.Header.Get("Idempotency-Key")
	if !idempotencyKeyPattern.MatchString(key) {
		status, body := invalidField("Idempotency-Key", "A valid idempotency key is required.")
		writeJSON(response, status, body)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maximumRequestBytes))
	if err != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: safeError("INVALID_REQUEST", "Request body is invalid or too large.", false)})
		return
	}
	digest := authenticatedRequestDigest(actorID, body)
	cacheKey := request.Method + " " + request.URL.EscapedPath() + " " + key

	server.mu.Lock()
	defer server.mu.Unlock()
	if stored, exists := server.idempotencyResponses[cacheKey]; exists {
		if stored.RequestDigest != digest {
			writeJSON(response, http.StatusConflict, ErrorEnvelope{Error: safeError("IDEMPOTENCY_KEY_REUSED", "Idempotency key was reused with a different request.", false)})
			return
		}
		writeRawJSON(response, stored.Status, stored.Body)
		return
	}

	status, result := mutation(actorID, body)
	encoded, err := json.Marshal(result)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, ErrorEnvelope{Error: safeError("INTERNAL_ERROR", "Response could not be encoded.", false)})
		return
	}
	server.idempotencyResponses[cacheKey] = storedResponse{RequestDigest: digest, Status: status, Body: encoded}
	writeRawJSON(response, status, encoded)
}

func (server *Server) createArtifact(domainID, invocationID string, now time.Time, payload map[string]any) ArtifactDescriptor {
	artifactID := derivedID("art", invocationID+":"+fmt.Sprint(payload["digest"]))
	artifact := ArtifactDescriptor{
		Metadata:     newMetadata(artifactID, domainID, "reference-runtime", now),
		InvocationID: invocationID,
		Name:         stringValue(payload["name"]),
		Kind:         stringValue(payload["kind"]),
		MediaType:    stringValue(payload["mediaType"]),
		SizeBytes:    int64Value(payload["sizeBytes"]),
		Digest:       stringValue(payload["digest"]),
		State:        "available",
		Sensitive:    boolValue(payload["sensitive"]),
	}
	server.artifacts[artifactKey(domainID, invocationID, artifactID)] = artifact
	return artifact
}

func applyEvent(invocation *Invocation, eventType string, payload map[string]any, occurredAt time.Time) {
	switch eventType {
	case "lifecycle.waiting":
		invocation.State = "waiting"
	case "lifecycle.succeeded":
		invocation.State = "succeeded"
		invocation.Result = map[string]any{"message": stringValue(payload["message"])}
		invocation.CompletedAt = &occurredAt
	case "lifecycle.failed":
		invocation.State = "failed"
		invocation.CompletedAt = &occurredAt
	case "lifecycle.cancelled":
		invocation.State = "cancelled"
		invocation.CompletedAt = &occurredAt
	case "error.occurred":
		invocation.Error = &APIError{
			Code:          stringValue(payload["code"]),
			Message:       stringValue(payload["message"]),
			CorrelationID: invocation.CorrelationID,
			Retryable:     boolValue(payload["retryable"]),
		}
	case "usage.recorded":
		invocation.Usage = &Usage{
			InputTokens:  int(int64Value(payload["inputTokens"])),
			OutputTokens: int(int64Value(payload["outputTokens"])),
			TotalTokens:  int(int64Value(payload["totalTokens"])),
		}
	}
}

func isolationDomain(request *http.Request) (string, *APIError) {
	domainID := request.PathValue("isolationDomainId")
	if !isolationDomainPattern.MatchString(domainID) {
		error := safeError("INVALID_ISOLATION_DOMAIN", "Isolation domain identifier is invalid.", false)
		return "", &error
	}
	return domainID, nil
}

func decodeBody[T any](body []byte) (T, *APIError) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		apiError := safeError("INVALID_REQUEST", "Request body does not match the contract.", false)
		return value, &apiError
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		apiError := safeError("INVALID_REQUEST", "Request body must contain exactly one JSON value.", false)
		return value, &apiError
	}
	return value, nil
}

func parseCursor(value string) (uint64, error) {
	if value == "" {
		return 0, nil
	}
	return strconv.ParseUint(value, 10, 64)
}

func newMetadata(id, domainID, actorID string, now time.Time) ResourceMetadata {
	return ResourceMetadata{ID: id, IsolationDomainID: domainID, Generation: 1, Version: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: actorID}
}

func newID(prefix string) string {
	return identity.New(prefix)
}

func derivedID(prefix, seed string) string {
	return identity.Derived(prefix, seed)
}

func resourceKey(domainID, resourceID string) string {
	return domainID + "/" + resourceID
}

func aliasKey(domainID, serviceID, alias string) string {
	return domainID + "/" + serviceID + "/" + alias
}

func artifactKey(domainID, invocationID, artifactID string) string {
	return domainID + "/" + invocationID + "/" + artifactID
}

func safeError(code, message string, retryable bool) APIError {
	return safeErrorWithCorrelation(newID("cor"), code, message, retryable)
}

func safeErrorWithCorrelation(correlationID, code, message string, retryable bool) APIError {
	return APIError{
		Code: code, Message: message, CorrelationID: correlationID, Retryable: retryable,
	}
}

func invalidField(field, message string) (int, any) {
	error := safeError("INVALID_REQUEST", "Request validation failed.", false)
	error.FieldErrors = []FieldError{{Field: field, Code: "INVALID_VALUE", Message: message}}
	return http.StatusBadRequest, ErrorEnvelope{Error: error}
}

func notFound(message string) (int, any) {
	return http.StatusNotFound, ErrorEnvelope{Error: safeError("RESOURCE_NOT_FOUND", message, false)}
}

func conflict(code, message string) (int, any) {
	return http.StatusConflict, ErrorEnvelope{Error: safeError(code, message, false)}
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		encoded = []byte(`{"error":{"code":"INTERNAL_ERROR","message":"Response could not be encoded.","correlationId":"unavailable","retryable":false}}`)
	}
	writeRawJSON(response, status, encoded)
}

func writeRawJSON(response http.ResponseWriter, status int, encoded []byte) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_, _ = response.Write(append(encoded, '\n'))
}

func duplicateString(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func cloneMap(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func int64Value(value any) int64 {
	switch number := value.(type) {
	case int:
		return int64(number)
	case int64:
		return number
	case float64:
		return int64(number)
	default:
		return 0
	}
}

func boolValue(value any) bool {
	boolean, _ := value.(bool)
	return boolean
}
