package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/persistence"
)

var invocationIDPattern = regexp.MustCompile(`^inv_[0-9a-z]{20,32}$`)

type invocationPage struct {
	Items      []domain.InvocationSummary `json:"items"`
	NextCursor string                     `json:"nextCursor,omitempty"`
}

type invocationListCursor struct {
	Version           int       `json:"version"`
	IsolationDomainID string    `json:"isolationDomainId"`
	ServiceID         string    `json:"serviceId"`
	CreatedAt         time.Time `json:"createdAt"`
	ID                string    `json:"id"`
}

func parseInvocationListQuery(rawQuery, domainID, serviceID string) (int, *invocationListCursor, error) {
	invalid := errors.New("invocation-list query is invalid")
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return 0, nil, invalid
	}
	for name, values := range query {
		if (name != "limit" && name != "cursor") || len(values) != 1 {
			return 0, nil, invalid
		}
	}
	limit := defaultServiceListLimit
	if query.Has("limit") {
		parsed, err := strconv.Atoi(query.Get("limit"))
		if err != nil || parsed < 1 || parsed > maximumServiceListLimit || strconv.Itoa(parsed) != query.Get("limit") {
			return 0, nil, invalid
		}
		limit = parsed
	}
	if !query.Has("cursor") {
		return limit, nil, nil
	}
	raw := query.Get("cursor")
	if raw == "" || len(raw) > maximumServiceCursorLen {
		return 0, nil, invalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, nil, invalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	var cursor invocationListCursor
	if err := decoder.Decode(&cursor); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return 0, nil, invalid
	}
	if cursor.Version != 1 || cursor.IsolationDomainID != domainID || cursor.ServiceID != serviceID ||
		cursor.CreatedAt.IsZero() || !invocationIDPattern.MatchString(cursor.ID) {
		return 0, nil, invalid
	}
	cursor.CreatedAt = cursor.CreatedAt.UTC()
	canonical, err := json.Marshal(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(canonical) != raw {
		return 0, nil, invalid
	}
	return limit, &cursor, nil
}

func writeInvocationPage(response http.ResponseWriter, request *http.Request, items []domain.InvocationSummary, hasMore bool) {
	page := invocationPage{Items: items}
	if hasMore {
		last := items[len(items)-1]
		encoded, err := json.Marshal(invocationListCursor{Version: 1,
			IsolationDomainID: last.Metadata.IsolationDomainID, ServiceID: last.ServiceID,
			CreatedAt: last.Metadata.CreatedAt.UTC(), ID: last.Metadata.ID,
		})
		if err != nil || last.Metadata.CreatedAt.IsZero() || !invocationIDPattern.MatchString(last.Metadata.ID) {
			writeInvocationListUnavailable(response, request)
			return
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	writeJSON(response, http.StatusOK, page)
}

func writeInvocationListUnavailable(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(
		authenticatedCorrelationID(request), "INVOCATION_LIST_UNAVAILABLE", "Invocation history is temporarily unavailable.", true,
	)})
}

func (server *Server) listInvocations(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	serviceID := request.PathValue("serviceId")
	limit, cursor, err := parseInvocationListQuery(request.URL.RawQuery, domainID, serviceID)
	if err != nil {
		problem := safeErrorWithCorrelation(authenticatedCorrelationID(request), "INVALID_REQUEST", "Request validation failed.", false)
		problem.FieldErrors = []FieldError{{Field: "query", Code: "INVALID_VALUE", Message: "Invocation-list limit or cursor is invalid."}}
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: problem})
		return
	}
	server.mu.RLock()
	_, serviceExists := server.services[resourceKey(domainID, serviceID)]
	items := make([]domain.InvocationSummary, 0)
	if serviceExists {
		for _, invocation := range server.invocations {
			if invocation.Metadata.IsolationDomainID != domainID || invocation.ServiceID != serviceID {
				continue
			}
			createdAt := invocation.Metadata.CreatedAt
			if cursor != nil && !(createdAt.Before(cursor.CreatedAt) || (createdAt.Equal(cursor.CreatedAt) && invocation.Metadata.ID < cursor.ID)) {
				continue
			}
			items = append(items, domain.InvocationSummary{
				Metadata: invocation.Metadata, ServiceID: invocation.ServiceID, RevisionID: invocation.RevisionID,
				Alias: invocation.Alias, State: invocation.State, CorrelationID: invocation.CorrelationID,
				OperationID: invocation.OperationID, CompletedAt: invocation.CompletedAt,
			})
		}
	}
	server.mu.RUnlock()
	if !serviceExists {
		writeJSON(response, http.StatusNotFound, ErrorEnvelope{Error: safeErrorWithCorrelation(
			authenticatedCorrelationID(request), "RESOURCE_NOT_FOUND", "Agent service was not found.", false,
		)})
		return
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Metadata.CreatedAt.Equal(items[j].Metadata.CreatedAt) {
			return items[i].Metadata.ID > items[j].Metadata.ID
		}
		return items[i].Metadata.CreatedAt.After(items[j].Metadata.CreatedAt)
	})
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	writeInvocationPage(response, request, items, hasMore)
}

func (server *DurableServer) listInvocations(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	serviceID := request.PathValue("serviceId")
	limit, cursor, err := parseInvocationListQuery(request.URL.RawQuery, domainID, serviceID)
	if err != nil {
		problem := safeErrorWithCorrelation(authenticatedCorrelationID(request), "INVALID_REQUEST", "Request validation failed.", false)
		problem.FieldErrors = []FieldError{{Field: "query", Code: "INVALID_VALUE", Message: "Invocation-list limit or cursor is invalid."}}
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: problem})
		return
	}
	var beforeCreatedAt *time.Time
	var beforeID string
	if cursor != nil {
		beforeCreatedAt = &cursor.CreatedAt
		beforeID = cursor.ID
	}
	listed, err := server.repository.ListInvocations(request.Context(), domainID, serviceID, beforeCreatedAt, beforeID, limit)
	if err != nil {
		var problem *persistence.DomainError
		if errors.As(err, &problem) && problem.Code == "RESOURCE_NOT_FOUND" {
			writeJSON(response, http.StatusNotFound, ErrorEnvelope{Error: APIError{
				Code: problem.Code, Message: problem.Message, CorrelationID: authenticatedCorrelationID(request), Retryable: false,
			}})
			return
		}
		writeInvocationListUnavailable(response, request)
		return
	}
	writeInvocationPage(response, request, listed.Items, listed.HasMore)
}
