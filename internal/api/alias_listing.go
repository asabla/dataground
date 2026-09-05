package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/persistence"
)

type serviceAliasPage struct {
	Items      []domain.ServiceAlias `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

type aliasListCursor struct {
	Version           int    `json:"version"`
	IsolationDomainID string `json:"isolationDomainId"`
	ServiceID         string `json:"serviceId"`
	Name              string `json:"name"`
}

func parseAliasListQuery(rawQuery, domainID, serviceID string) (int, *aliasListCursor, error) {
	invalid := errors.New("alias-list query is invalid")
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
	var cursor aliasListCursor
	if err := decoder.Decode(&cursor); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return 0, nil, invalid
	}
	if cursor.Version != 1 || cursor.IsolationDomainID != domainID || cursor.ServiceID != serviceID ||
		len(cursor.Name) > 63 || !aliasPattern.MatchString(cursor.Name) {
		return 0, nil, invalid
	}
	canonical, err := json.Marshal(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(canonical) != raw {
		return 0, nil, invalid
	}
	return limit, &cursor, nil
}

func writeAliasPage(response http.ResponseWriter, request *http.Request, items []domain.ServiceAlias, hasMore bool) {
	page := serviceAliasPage{Items: items}
	if hasMore {
		last := items[len(items)-1]
		encoded, err := json.Marshal(aliasListCursor{Version: 1,
			IsolationDomainID: last.Metadata.IsolationDomainID, ServiceID: last.ServiceID,
			Name: last.Name,
		})
		if err != nil || len(last.Name) > 63 || !aliasPattern.MatchString(last.Name) {
			writeAliasListUnavailable(response, request)
			return
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	writeJSON(response, http.StatusOK, page)
}

func writeAliasListUnavailable(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(
		authenticatedCorrelationID(request), "ALIAS_LIST_UNAVAILABLE", "Service aliases are temporarily unavailable.", true,
	)})
}

func (server *Server) listServiceAliases(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	serviceID := request.PathValue("serviceId")
	limit, cursor, err := parseAliasListQuery(request.URL.RawQuery, domainID, serviceID)
	if err != nil {
		problem := safeErrorWithCorrelation(authenticatedCorrelationID(request), "INVALID_REQUEST", "Request validation failed.", false)
		problem.FieldErrors = []FieldError{{Field: "query", Code: "INVALID_VALUE", Message: "Alias-list limit or cursor is invalid."}}
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: problem})
		return
	}
	server.mu.RLock()
	_, serviceExists := server.services[resourceKey(domainID, serviceID)]
	items := make([]domain.ServiceAlias, 0)
	if serviceExists {
		for _, alias := range server.aliases {
			if alias.Metadata.IsolationDomainID != domainID || alias.ServiceID != serviceID || alias.WithdrawnAt != nil ||
				(cursor != nil && alias.Name <= cursor.Name) {
				continue
			}
			items = append(items, alias)
		}
	}
	server.mu.RUnlock()
	if !serviceExists {
		writeJSON(response, http.StatusNotFound, ErrorEnvelope{Error: safeErrorWithCorrelation(
			authenticatedCorrelationID(request), "RESOURCE_NOT_FOUND", "Agent service was not found.", false,
		)})
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	writeAliasPage(response, request, items, hasMore)
}

func (server *DurableServer) listServiceAliases(response http.ResponseWriter, request *http.Request) {
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	serviceID := request.PathValue("serviceId")
	limit, cursor, err := parseAliasListQuery(request.URL.RawQuery, domainID, serviceID)
	if err != nil {
		problem := safeErrorWithCorrelation(authenticatedCorrelationID(request), "INVALID_REQUEST", "Request validation failed.", false)
		problem.FieldErrors = []FieldError{{Field: "query", Code: "INVALID_VALUE", Message: "Alias-list limit or cursor is invalid."}}
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: problem})
		return
	}
	var afterName string
	if cursor != nil {
		afterName = cursor.Name
	}
	listed, err := server.repository.ListServiceAliases(request.Context(), domainID, serviceID, afterName, limit)
	if err != nil {
		var problem *persistence.DomainError
		if errors.As(err, &problem) && problem.Code == "RESOURCE_NOT_FOUND" {
			writeJSON(response, http.StatusNotFound, ErrorEnvelope{Error: APIError{
				Code: problem.Code, Message: problem.Message, CorrelationID: authenticatedCorrelationID(request), Retryable: false,
			}})
			return
		}
		writeAliasListUnavailable(response, request)
		return
	}
	writeAliasPage(response, request, listed.Items, listed.HasMore)
}
