package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/authz"
)

const MaximumArtifactContentBytes int64 = 16 << 20

// DurableArtifactContentConfig explicitly installs a read-only object port.
// It grants no authorization and must point to the worker's exact object store.
type DurableArtifactContentConfig struct {
	Objects      artifact.ObjectReader
	MaximumBytes int64
}

func newArtifactContentHandler(catalog artifact.ReadCatalog, authorizer authz.Authorizer, configs ...DurableArtifactContentConfig) (http.HandlerFunc, error) {
	if len(configs) == 0 {
		return artifactContentUnavailable, nil
	}
	if len(configs) != 1 || configs[0].MaximumBytes > MaximumArtifactContentBytes {
		return nil, errors.New("one bounded artifact content configuration is required")
	}
	reader, err := artifact.NewReader(catalog, configs[0].Objects, configs[0].MaximumBytes)
	if err != nil {
		return nil, err
	}
	// Hold capacity through the response write, not only the object read. Slow
	// clients cannot accumulate an unbounded number of verified content buffers.
	slots := make(chan struct{}, 4)
	return func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.RawQuery != "" || request.Header.Get("Range") != "" {
			artifactContentError(response, request, http.StatusBadRequest, "INVALID_ARGUMENT", "Artifact content requires a complete GET without query or range.", false)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			artifactContentUnavailable(response, request)
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
		defer cancel()
		request = request.WithContext(ctx)
		if !authorizeArtifactContent(response, request, authorizer) {
			return
		}
		content, err := reader.Read(ctx, request.PathValue("isolationDomainId"), request.PathValue("invocationId"), request.PathValue("artifactId"))
		if err != nil {
			switch {
			case errors.Is(err, artifact.ErrInvocationArtifactInvalid):
				artifactContentError(response, request, http.StatusBadRequest, "INVALID_ARGUMENT", "The artifact reference is invalid.", false)
			case errors.Is(err, artifact.ErrInvocationArtifactMissing):
				artifactContentError(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "Artifact was not found.", false)
			default:
				artifactContentUnavailable(response, request)
			}
			return
		}
		defer clear(content.Bytes)
		// Re-evaluate current policy after storage I/O and before disclosing bytes.
		if !authorizeArtifactContent(response, request, authorizer) {
			return
		}
		controller := http.NewResponseController(response)
		if err := controller.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil {
			artifactContentUnavailable(response, request)
			return
		}
		defer controller.SetWriteDeadline(time.Time{})
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		response.Header().Set("Content-Type", "application/octet-stream")
		response.Header().Set("Content-Disposition", "attachment; filename=\""+content.Record.ID+".bin\"")
		response.Header().Set("Content-Length", strconv.Itoa(len(content.Bytes)))
		response.Header().Set("ETag", "\""+content.Record.Digest+"\"")
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write(content.Bytes)
	}, nil
}

func authorizeArtifactContent(response http.ResponseWriter, request *http.Request, authorizer authz.Authorizer) bool {
	principal, ok := authenticatedPrincipal(request)
	if !ok || request.Context().Err() != nil {
		artifactContentUnavailable(response, request)
		return false
	}
	err := authorizer.Authorize(request.Context(), authz.Request{
		Principal: principal, Action: authz.ReadInvocationArtifactContent, ResourceType: authz.Artifact,
		ResourceID: request.PathValue("artifactId"), IsolationDomainID: request.PathValue("isolationDomainId"), CorrelationID: authenticatedCorrelationID(request),
	})
	if err == nil && request.Context().Err() == nil {
		return true
	}
	if errors.Is(err, authz.ErrDenied) {
		artifactContentError(response, request, http.StatusForbidden, "ACTION_FORBIDDEN", "The authenticated principal cannot perform this action.", false)
	} else {
		artifactContentError(response, request, http.StatusServiceUnavailable, "AUTHORIZATION_UNAVAILABLE", "Authorization is temporarily unavailable.", true)
	}
	return false
}

func artifactContentUnavailable(response http.ResponseWriter, request *http.Request) {
	artifactContentError(response, request, http.StatusServiceUnavailable, "ARTIFACT_CONTENT_UNAVAILABLE", "Artifact content is unavailable.", true)
}

func artifactContentError(response http.ResponseWriter, request *http.Request, status int, code, message string, retryable bool) {
	writeJSON(response, status, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), code, message, retryable)})
}
