package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/domain"
)

var workspaceIDPattern = regexp.MustCompile(`^wsp_[0-9a-z]{20,32}$`)

const (
	defaultWorkspaceListLimit = 50
	maximumWorkspaceListLimit = 100
	maximumWorkspaceCursorLen = 512
)

type workspacePage struct {
	Items      []domain.Workspace `json:"items"`
	NextCursor string             `json:"nextCursor,omitempty"`
}

type workspaceListCursor struct {
	IsolationDomainID string    `json:"isolationDomainId"`
	CreatedAt         time.Time `json:"createdAt"`
	ID                string    `json:"id"`
}

func parseWorkspaceListQuery(query url.Values, domainID string) (int, *workspaceListCursor, error) {
	for name := range query {
		if name != "limit" && name != "cursor" {
			return 0, nil, errors.New("workspace-list query parameter is unknown")
		}
	}
	if len(query["limit"]) > 1 || len(query["cursor"]) > 1 {
		return 0, nil, errors.New("workspace-list query parameter is repeated")
	}
	limit := defaultWorkspaceListLimit
	if query.Has("limit") {
		raw := query.Get("limit")
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maximumWorkspaceListLimit || strconv.Itoa(parsed) != raw {
			return 0, nil, errors.New("workspace-list limit is invalid")
		}
		limit = parsed
	}
	rawCursor := query.Get("cursor")
	if !query.Has("cursor") {
		return limit, nil, nil
	}
	if rawCursor == "" || len(rawCursor) > maximumWorkspaceCursorLen {
		return 0, nil, errors.New("workspace-list cursor is too large")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(rawCursor)
	if err != nil || len(decoded) == 0 || len(decoded) > maximumWorkspaceCursorLen {
		return 0, nil, errors.New("workspace-list cursor is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	var cursor workspaceListCursor
	if err := decoder.Decode(&cursor); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return 0, nil, errors.New("workspace-list cursor is invalid")
	}
	if cursor.IsolationDomainID != domainID || cursor.CreatedAt.IsZero() || !workspaceIDPattern.MatchString(cursor.ID) {
		return 0, nil, errors.New("workspace-list cursor is invalid")
	}
	cursor.CreatedAt = cursor.CreatedAt.UTC()
	canonical, err := json.Marshal(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(canonical) != rawCursor {
		return 0, nil, errors.New("workspace-list cursor is not canonical")
	}
	return limit, &cursor, nil
}

func encodeWorkspaceListCursor(workspace domain.Workspace) (string, error) {
	if workspace.Metadata.CreatedAt.IsZero() || !workspaceIDPattern.MatchString(workspace.Metadata.ID) {
		return "", errors.New("workspace-list boundary is invalid")
	}
	encoded, err := json.Marshal(workspaceListCursor{
		IsolationDomainID: workspace.Metadata.IsolationDomainID,
		CreatedAt:         workspace.Metadata.CreatedAt.UTC(),
		ID:                workspace.Metadata.ID,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}
