package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/persistence"
)

var questionListIDPattern = regexp.MustCompile(`^qst_[0-9a-z]{20,32}$`)

type invocationQuestionPage struct {
	Items      []domain.InvocationQuestionSummary `json:"items"`
	NextCursor string                             `json:"nextCursor,omitempty"`
}

type invocationQuestionListCursor struct {
	Version           int       `json:"version"`
	IsolationDomainID string    `json:"isolationDomainId"`
	InvocationID      string    `json:"invocationId"`
	CreatedAt         time.Time `json:"createdAt"`
	ID                string    `json:"id"`
}

func parseInvocationQuestionListQuery(rawQuery, domainID, invocationID string) (int, *invocationQuestionListCursor, error) {
	invalid := errors.New("invocation-question-list query is invalid")
	if !invocationIDPattern.MatchString(invocationID) {
		return 0, nil, invalid
	}
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
	var cursor invocationQuestionListCursor
	if err := decoder.Decode(&cursor); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return 0, nil, invalid
	}
	if cursor.Version != 1 || cursor.IsolationDomainID != domainID || cursor.InvocationID != invocationID ||
		cursor.CreatedAt.IsZero() || !questionListIDPattern.MatchString(cursor.ID) {
		return 0, nil, invalid
	}
	cursor.CreatedAt = cursor.CreatedAt.UTC()
	canonical, err := json.Marshal(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(canonical) != raw {
		return 0, nil, invalid
	}
	return limit, &cursor, nil
}

func writeInvocationQuestionPage(response http.ResponseWriter, request *http.Request, items []domain.InvocationQuestionSummary, hasMore bool) {
	page := invocationQuestionPage{Items: items}
	if hasMore {
		last := items[len(items)-1]
		encoded, err := json.Marshal(invocationQuestionListCursor{Version: 1,
			IsolationDomainID: last.IsolationDomainID, InvocationID: last.InvocationID,
			CreatedAt: last.CreatedAt.UTC(), ID: last.ID,
		})
		if err != nil || last.CreatedAt.IsZero() || !questionListIDPattern.MatchString(last.ID) {
			writeInvocationQuestionListUnavailable(response, request)
			return
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	writeJSON(response, http.StatusOK, page)
}

func writeInvocationQuestionListUnavailable(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(
		authenticatedCorrelationID(request), "INVOCATION_QUESTION_LIST_UNAVAILABLE", "Invocation questions are temporarily unavailable.", true,
	)})
}

func (server *Server) listInvocationQuestions(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	invocationID := request.PathValue("invocationId")
	_, _, err := parseInvocationQuestionListQuery(request.URL.RawQuery, domainID, invocationID)
	if err != nil {
		problem := safeErrorWithCorrelation(authenticatedCorrelationID(request), "INVALID_REQUEST", "Request validation failed.", false)
		problem.FieldErrors = []FieldError{{Field: "query", Code: "INVALID_VALUE", Message: "Invocation-question-list limit or cursor is invalid."}}
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: problem})
		return
	}
	server.mu.RLock()
	_, exists := server.invocations[resourceKey(domainID, invocationID)]
	server.mu.RUnlock()
	if !exists {
		writeJSON(response, http.StatusNotFound, ErrorEnvelope{Error: safeErrorWithCorrelation(
			authenticatedCorrelationID(request), "RESOURCE_NOT_FOUND", "Invocation was not found.", false,
		)})
		return
	}
	writeInvocationQuestionPage(response, request, []domain.InvocationQuestionSummary{}, false)
}

func (server *DurableServer) listInvocationQuestions(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	invocationID := request.PathValue("invocationId")
	limit, cursor, err := parseInvocationQuestionListQuery(request.URL.RawQuery, domainID, invocationID)
	if err != nil {
		problem := safeErrorWithCorrelation(authenticatedCorrelationID(request), "INVALID_REQUEST", "Request validation failed.", false)
		problem.FieldErrors = []FieldError{{Field: "query", Code: "INVALID_VALUE", Message: "Invocation-question-list limit or cursor is invalid."}}
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: problem})
		return
	}
	var beforeCreatedAt *time.Time
	var beforeID string
	if cursor != nil {
		beforeCreatedAt = &cursor.CreatedAt
		beforeID = cursor.ID
	}
	listed, err := server.repository.ListInvocationQuestions(request.Context(), domainID, invocationID, beforeCreatedAt, beforeID, limit)
	if err != nil {
		var problem *persistence.DomainError
		if errors.As(err, &problem) && problem.Code == "RESOURCE_NOT_FOUND" {
			writeJSON(response, http.StatusNotFound, ErrorEnvelope{Error: APIError{
				Code: problem.Code, Message: problem.Message, CorrelationID: authenticatedCorrelationID(request), Retryable: false,
			}})
			return
		}
		writeInvocationQuestionListUnavailable(response, request)
		return
	}
	writeInvocationQuestionPage(response, request, listed.Items, listed.HasMore)
}
