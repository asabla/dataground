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

	"github.com/asabla/dataground/internal/domain"
)

var revisionIDPattern = regexp.MustCompile(`^rev_[0-9a-z]{20,32}$`)

type serviceRevisionPage struct {
	Items      []domain.ServiceRevision `json:"items"`
	NextCursor string                   `json:"nextCursor,omitempty"`
}

type revisionListCursor struct {
	RevisionNumber int    `json:"revisionNumber"`
	ID             string `json:"id"`
}

func parseRevisionListQuery(query url.Values) (int, *revisionListCursor, error) {
	for name := range query {
		if name != "limit" && name != "cursor" {
			return 0, nil, errors.New("revision-list query parameter is unknown")
		}
	}
	if len(query["limit"]) > 1 || len(query["cursor"]) > 1 {
		return 0, nil, errors.New("revision-list query parameter is repeated")
	}
	limit := defaultServiceListLimit
	if query.Has("limit") {
		raw := query.Get("limit")
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maximumServiceListLimit || strconv.Itoa(parsed) != raw {
			return 0, nil, errors.New("revision-list limit is invalid")
		}
		limit = parsed
	}
	if !query.Has("cursor") {
		return limit, nil, nil
	}
	rawCursor := query.Get("cursor")
	if rawCursor == "" || len(rawCursor) > maximumServiceCursorLen {
		return 0, nil, errors.New("revision-list cursor is too large")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(rawCursor)
	if err != nil || len(decoded) == 0 || len(decoded) > maximumServiceCursorLen {
		return 0, nil, errors.New("revision-list cursor is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	var cursor revisionListCursor
	if err := decoder.Decode(&cursor); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return 0, nil, errors.New("revision-list cursor is invalid")
	}
	if cursor.RevisionNumber < 1 || !revisionIDPattern.MatchString(cursor.ID) {
		return 0, nil, errors.New("revision-list cursor is invalid")
	}
	canonical, err := json.Marshal(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(canonical) != rawCursor {
		return 0, nil, errors.New("revision-list cursor is not canonical")
	}
	return limit, &cursor, nil
}

func encodeRevisionListCursor(revision domain.ServiceRevision) (string, error) {
	if revision.RevisionNumber < 1 || !revisionIDPattern.MatchString(revision.Metadata.ID) {
		return "", errors.New("revision-list boundary is invalid")
	}
	encoded, err := json.Marshal(revisionListCursor{
		RevisionNumber: revision.RevisionNumber,
		ID:             revision.Metadata.ID,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}
