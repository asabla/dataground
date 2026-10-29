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
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/persistence"
)

type scopeInvocationCursor struct {
	Kind              string    `json:"kind"`
	Version           int       `json:"version"`
	IsolationDomainID string    `json:"isolationDomainId"`
	State             string    `json:"state"`
	CreatedAt         time.Time `json:"createdAt"`
	ID                string    `json:"id"`
}
type scopeInvocationPage struct {
	Items      []domain.ScopeInvocationSummary `json:"items"`
	NextCursor string                          `json:"nextCursor,omitempty"`
}

func parseScopeInvocationQuery(raw, scope string) (int, string, *scopeInvocationCursor, error) {
	invalid := errors.New("scope invocation query is invalid")
	query, err := url.ParseQuery(raw)
	if err != nil {
		return 0, "", nil, invalid
	}
	for name, values := range query {
		if (name != "state" && name != "limit" && name != "cursor") || len(values) != 1 {
			return 0, "", nil, invalid
		}
	}
	state := query.Get("state")
	if !persistence.ValidInvocationDiscoveryState(state) || (query.Has("state") && state == "") {
		return 0, "", nil, invalid
	}
	limit := defaultServiceListLimit
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > maximumServiceListLimit || strconv.Itoa(limit) != query.Get("limit") {
			return 0, "", nil, invalid
		}
	}
	if !query.Has("cursor") {
		return limit, state, nil, nil
	}
	encoded := query.Get("cursor")
	if encoded == "" || len(encoded) > maximumServiceCursorLen {
		return 0, "", nil, invalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return 0, "", nil, invalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	var cursor scopeInvocationCursor
	if decoder.Decode(&cursor) != nil || decoder.Decode(&struct{}{}) != io.EOF || cursor.Kind != "scope-invocations" || cursor.Version != 1 || cursor.IsolationDomainID != scope || cursor.State != state || cursor.CreatedAt.IsZero() || !invocationIDPattern.MatchString(cursor.ID) {
		return 0, "", nil, invalid
	}
	cursor.CreatedAt = cursor.CreatedAt.UTC()
	canonical, err := json.Marshal(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(canonical) != encoded {
		return 0, "", nil, invalid
	}
	return limit, state, &cursor, nil
}

func writeScopeInvocationPage(response http.ResponseWriter, request *http.Request, state string, items []domain.ScopeInvocationSummary, more bool) {
	page := scopeInvocationPage{Items: items}
	if more {
		if len(items) == 0 {
			writeInvocationListUnavailable(response, request)
			return
		}
		last := items[len(items)-1]
		encoded, err := json.Marshal(scopeInvocationCursor{Kind: "scope-invocations", Version: 1, IsolationDomainID: last.Metadata.IsolationDomainID, State: state, CreatedAt: last.Metadata.CreatedAt.UTC(), ID: last.Metadata.ID})
		if err != nil || last.Metadata.CreatedAt.IsZero() || !invocationIDPattern.MatchString(last.Metadata.ID) {
			writeInvocationListUnavailable(response, request)
			return
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, page)
}

func scopeInvocationQueryError(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), "INVALID_REQUEST", "Invocation-list limit, state or cursor is invalid.", false)})
}

func (server *Server) listScopeInvocations(response http.ResponseWriter, request *http.Request) {
	scope, problem := isolationDomain(request)
	if problem != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *problem})
		return
	}
	limit, state, cursor, err := parseScopeInvocationQuery(request.URL.RawQuery, scope)
	if err != nil {
		scopeInvocationQueryError(response, request)
		return
	}
	items := make([]domain.ScopeInvocationSummary, 0)
	server.mu.RLock()
	for _, invocation := range server.invocations {
		if invocation.Metadata.IsolationDomainID != scope || (state != "" && invocation.State != state) {
			continue
		}
		created := invocation.Metadata.CreatedAt
		if cursor != nil && !(created.Before(cursor.CreatedAt) || (created.Equal(cursor.CreatedAt) && invocation.Metadata.ID < cursor.ID)) {
			continue
		}
		service, exists := server.services[resourceKey(scope, invocation.ServiceID)]
		if !exists {
			continue
		}
		items = append(items, domain.ScopeInvocationSummary{ServiceName: service.Name, InvocationSummary: domain.InvocationSummary{
			Metadata: invocation.Metadata, ServiceID: invocation.ServiceID, RevisionID: invocation.RevisionID, Alias: invocation.Alias, State: invocation.State, CorrelationID: invocation.CorrelationID, OperationID: invocation.OperationID, CompletedAt: invocation.CompletedAt,
		}})
	}
	server.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].Metadata.CreatedAt.Equal(items[j].Metadata.CreatedAt) {
			return items[i].Metadata.ID > items[j].Metadata.ID
		}
		return items[i].Metadata.CreatedAt.After(items[j].Metadata.CreatedAt)
	})
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	writeScopeInvocationPage(response, request, state, items, more)
}

func (server *DurableServer) listScopeInvocations(response http.ResponseWriter, request *http.Request) {
	scope, problem := isolationDomain(request)
	if problem != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *problem})
		return
	}
	limit, state, cursor, err := parseScopeInvocationQuery(request.URL.RawQuery, scope)
	if err != nil {
		scopeInvocationQueryError(response, request)
		return
	}
	var before *time.Time
	var id string
	if cursor != nil {
		before = &cursor.CreatedAt
		id = cursor.ID
	}
	page, err := server.repository.ListScopeInvocations(request.Context(), scope, state, before, id, limit)
	if err != nil {
		writeInvocationListUnavailable(response, request)
		return
	}
	writeScopeInvocationPage(response, request, state, page.Items, page.HasMore)
}
