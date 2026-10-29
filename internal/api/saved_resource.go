package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/persistence"
)

type savedResourceKey struct{ Scope, PrincipalKind, PrincipalID, ResourceID string }
type setSavedResourceRequest struct {
	Kind       string `json:"kind"`
	ResourceID string `json:"resourceId"`
	Saved      *bool  `json:"saved"`
}
type savedResourceCursor struct {
	Kind              string    `json:"kind"`
	Version           int       `json:"version"`
	IsolationDomainID string    `json:"isolationDomainId"`
	Principal         string    `json:"principal"`
	SavedAt           time.Time `json:"savedAt"`
	ID                string    `json:"id"`
}
type savedResourcePage struct {
	Items      []domain.SavedResource `json:"items"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

func savedOwner(request *http.Request) persistence.SavedResourceOwner {
	p, _ := authenticatedPrincipal(request)
	return persistence.SavedResourceOwner{Kind: p.Kind(), ID: p.ID()}
}
func savedPrincipalBinding(owner persistence.SavedResourceOwner) string {
	// Both fields use closed alphabets without colons; kind and ID are distinct
	// identity coordinates even when two principal types share the same ID.
	digest := sha256.Sum256([]byte("dataground.saved-resource-owner/v1:" + string(owner.Kind) + ":" + owner.ID))
	return hex.EncodeToString(digest[:])
}
func decodeSavedResourceRequest(body []byte) (setSavedResourceRequest, bool) {
	if len(body) > 1024 || !utf8.Valid(body) {
		return setSavedResourceRequest{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return setSavedResourceRequest{}, false
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || (key != "kind" && key != "resourceId" && key != "saved") {
			return setSavedResourceRequest{}, false
		}
		seen[key] = true
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return setSavedResourceRequest{}, false
		}
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') || len(seen) != 3 || decoder.Decode(&struct{}{}) != io.EOF {
		return setSavedResourceRequest{}, false
	}
	input, problem := decodeBody[setSavedResourceRequest](body)
	return input, problem == nil && input.Saved != nil && domain.ValidSavedResource(input.Kind, input.ResourceID)
}

func (server *Server) setSavedResource(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	owner := savedOwner(request)
	server.mutateWithDigest(response, request, func(actor string, body []byte) (int, any) {
		input, valid := decodeSavedResourceRequest(body)
		if !valid || !owner.Valid() {
			return invalidField("savedResource", "Supply a resource kind, matching identifier and saved state.")
		}
		scope := request.PathValue("isolationDomainId")
		if *input.Saved && server.savedResourceTarget(scope, input.Kind, input.ResourceID) == nil {
			return notFound("Resource was not found.")
		}
		key := savedResourceKey{Scope: scope, PrincipalKind: string(owner.Kind), PrincipalID: actor, ResourceID: input.ResourceID}
		state, exists := server.savedResources[key]
		if !exists || state.Saved != *input.Saved {
			state = domain.SavedResourceState{IsolationDomainID: scope, Kind: input.Kind, ResourceID: input.ResourceID, Saved: *input.Saved, UpdatedAt: server.now().UTC()}
			server.savedResources[key] = state
		}
		return http.StatusOK, state
	}, func(_ string, body []byte) [sha256.Size]byte {
		return authenticatedRequestDigest(savedPrincipalBinding(owner), body)
	})
}
func (server *DurableServer) setSavedResource(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	server.mutate(response, request, func(scope, actor, correlation string, body []byte) (persistence.CommandResult, error) {
		input, valid := decodeSavedResourceRequest(body)
		if !valid {
			return encodedResult(invalidField("savedResource", "Supply a resource kind, matching identifier and saved state."))
		}
		owner := savedOwner(request)
		idempotency := commandIdempotency(request, scope, actor, body)
		idempotency.RequestDigest = authenticatedRequestDigest(savedPrincipalBinding(owner), body)
		return server.repository.SetSavedResource(request.Context(), idempotency, persistence.SetSavedResourceInput{Owner: owner, Kind: input.Kind, ResourceID: input.ResourceID, Saved: *input.Saved, CorrelationID: correlation})
	})
}

// Caller holds the server lock. This projection never returns run inputs,
// outputs, creator fields, sensitive contents or internal storage identifiers.
func (server *Server) savedResourceTarget(scope, kind, id string) *domain.ResourceSearchResult {
	var r domain.ResourceSearchResult
	switch kind {
	case "workspace":
		w, ok := server.workspaces[resourceKey(scope, id)]
		if !ok {
			return nil
		}
		r = domain.ResourceSearchResult{Metadata: domain.SearchIdentity(w.Metadata), Kind: kind, Name: w.Name, Description: w.Description}
	case "agent-service":
		s, ok := server.services[resourceKey(scope, id)]
		if !ok {
			return nil
		}
		r = domain.ResourceSearchResult{Metadata: domain.SearchIdentity(s.Metadata), Kind: kind, Name: s.Name, Description: s.Description, WorkspaceID: s.WorkspaceID}
	case "invocation":
		i, ok := server.invocations[resourceKey(scope, id)]
		if !ok {
			return nil
		}
		s, ok := server.services[resourceKey(scope, i.ServiceID)]
		if !ok {
			return nil
		}
		r = domain.ResourceSearchResult{Metadata: domain.SearchIdentity(i.Metadata), Kind: kind, Name: s.Name, WorkspaceID: s.WorkspaceID, ServiceID: i.ServiceID, State: i.State}
	case "artifact":
		for _, a := range server.artifacts {
			if a.Metadata.IsolationDomainID != scope || a.Metadata.ID != id {
				continue
			}
			i, ok := server.invocations[resourceKey(scope, a.InvocationID)]
			if !ok {
				return nil
			}
			s, ok := server.services[resourceKey(scope, i.ServiceID)]
			if !ok {
				return nil
			}
			r = domain.ResourceSearchResult{Metadata: domain.SearchIdentity(a.Metadata), Kind: kind, Name: a.Name, WorkspaceID: s.WorkspaceID, ServiceID: i.ServiceID, InvocationID: a.InvocationID, State: a.State}
			return &r
		}
		return nil
	default:
		return nil
	}
	return &r
}

func parseSavedResourceList(raw, scope string, owner persistence.SavedResourceOwner) (int, *savedResourceCursor, error) {
	invalid := errors.New("saved resource query is invalid")
	query, err := url.ParseQuery(raw)
	if err != nil || !owner.Valid() {
		return 0, nil, invalid
	}
	for name, values := range query {
		if (name != "limit" && name != "cursor") || len(values) != 1 {
			return 0, nil, invalid
		}
	}
	limit := defaultServiceListLimit
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > maximumServiceListLimit || strconv.Itoa(limit) != query.Get("limit") {
			return 0, nil, invalid
		}
	}
	if !query.Has("cursor") {
		return limit, nil, nil
	}
	encoded := query.Get("cursor")
	if encoded == "" || len(encoded) > maximumServiceCursorLen {
		return 0, nil, invalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return 0, nil, invalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	var cursor savedResourceCursor
	if decoder.Decode(&cursor) != nil || decoder.Decode(&struct{}{}) != io.EOF || cursor.Kind != "saved-resources" || cursor.Version != 1 || cursor.IsolationDomainID != scope || cursor.Principal != savedPrincipalBinding(owner) || cursor.SavedAt.IsZero() || !domain.ValidResourceSearchID(cursor.ID) {
		return 0, nil, invalid
	}
	cursor.SavedAt = cursor.SavedAt.UTC()
	canonical, err := json.Marshal(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(canonical) != encoded {
		return 0, nil, invalid
	}
	return limit, &cursor, nil
}
func savedListError(response http.ResponseWriter, request *http.Request, invalid bool) {
	status, code, message := http.StatusServiceUnavailable, "SAVED_RESOURCE_LIST_UNAVAILABLE", "Saved resources are temporarily unavailable."
	if invalid {
		status, code, message = http.StatusBadRequest, "INVALID_REQUEST", "Saved-resource limit or cursor is invalid."
	}
	writeJSON(response, status, ErrorEnvelope{Error: safeErrorWithCorrelation(authenticatedCorrelationID(request), code, message, !invalid)})
}
func writeSavedResourcePage(response http.ResponseWriter, request *http.Request, items []domain.SavedResource, more bool) {
	page := savedResourcePage{Items: items}
	if more {
		if len(items) == 0 {
			savedListError(response, request, false)
			return
		}
		last := items[len(items)-1]
		cursor := savedResourceCursor{Kind: "saved-resources", Version: 1, IsolationDomainID: last.IsolationDomainID, Principal: savedPrincipalBinding(savedOwner(request)), SavedAt: last.SavedAt.UTC(), ID: last.ResourceID}
		encoded, err := json.Marshal(cursor)
		if err != nil {
			savedListError(response, request, false)
			return
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	writeJSON(response, http.StatusOK, page)
}
func (server *Server) listSavedResources(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	scope, problem := isolationDomain(request)
	if problem != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *problem})
		return
	}
	owner := savedOwner(request)
	limit, cursor, err := parseSavedResourceList(request.URL.RawQuery, scope, owner)
	if err != nil {
		savedListError(response, request, true)
		return
	}
	items := make([]domain.SavedResource, 0)
	server.mu.RLock()
	for key, state := range server.savedResources {
		if key.Scope != scope || key.PrincipalKind != string(owner.Kind) || key.PrincipalID != owner.ID || !state.Saved {
			continue
		}
		if cursor != nil && !(state.UpdatedAt.Before(cursor.SavedAt) || (state.UpdatedAt.Equal(cursor.SavedAt) && state.ResourceID < cursor.ID)) {
			continue
		}
		items = append(items, domain.SavedResource{IsolationDomainID: scope, Kind: state.Kind, ResourceID: state.ResourceID, SavedAt: state.UpdatedAt, Resource: server.savedResourceTarget(scope, state.Kind, state.ResourceID)})
	}
	server.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].SavedAt.Equal(items[j].SavedAt) {
			return items[i].ResourceID > items[j].ResourceID
		}
		return items[i].SavedAt.After(items[j].SavedAt)
	})
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	writeSavedResourcePage(response, request, items, more)
}
func (server *DurableServer) listSavedResources(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	scope, problem := isolationDomain(request)
	if problem != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *problem})
		return
	}
	owner := savedOwner(request)
	limit, cursor, err := parseSavedResourceList(request.URL.RawQuery, scope, owner)
	if err != nil {
		savedListError(response, request, true)
		return
	}
	var before *time.Time
	var id string
	if cursor != nil {
		before = &cursor.SavedAt
		id = cursor.ID
	}
	page, err := server.repository.ListSavedResources(request.Context(), scope, owner, before, id, limit)
	if err != nil {
		savedListError(response, request, false)
		return
	}
	writeSavedResourcePage(response, request, page.Items, page.HasMore)
}
