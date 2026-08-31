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

var serviceIDPattern = regexp.MustCompile(`^svc_[0-9a-z]{20,32}$`)

const (
	defaultServiceListLimit = 50
	maximumServiceListLimit = 100
	maximumServiceCursorLen = 512
)

type agentServicePage struct {
	Items      []domain.AgentService `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

type serviceListCursor struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
}

func parseServiceListQuery(query url.Values) (int, *serviceListCursor, error) {
	for name := range query {
		if name != "limit" && name != "cursor" {
			return 0, nil, errors.New("service-list query parameter is unknown")
		}
	}
	if len(query["limit"]) > 1 || len(query["cursor"]) > 1 {
		return 0, nil, errors.New("service-list query parameter is repeated")
	}
	limit := defaultServiceListLimit
	if query.Has("limit") {
		raw := query.Get("limit")
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maximumServiceListLimit || strconv.Itoa(parsed) != raw {
			return 0, nil, errors.New("service-list limit is invalid")
		}
		limit = parsed
	}
	rawCursor := query.Get("cursor")
	if !query.Has("cursor") {
		return limit, nil, nil
	}
	if rawCursor == "" || len(rawCursor) > maximumServiceCursorLen {
		return 0, nil, errors.New("service-list cursor is too large")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(rawCursor)
	if err != nil || len(decoded) == 0 || len(decoded) > maximumServiceCursorLen {
		return 0, nil, errors.New("service-list cursor is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	var cursor serviceListCursor
	if err := decoder.Decode(&cursor); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return 0, nil, errors.New("service-list cursor is invalid")
	}
	if cursor.CreatedAt.IsZero() || !serviceIDPattern.MatchString(cursor.ID) {
		return 0, nil, errors.New("service-list cursor is invalid")
	}
	cursor.CreatedAt = cursor.CreatedAt.UTC()
	canonical, err := json.Marshal(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(canonical) != rawCursor {
		return 0, nil, errors.New("service-list cursor is not canonical")
	}
	return limit, &cursor, nil
}

func encodeServiceListCursor(service domain.AgentService) (string, error) {
	if service.Metadata.CreatedAt.IsZero() || !serviceIDPattern.MatchString(service.Metadata.ID) {
		return "", errors.New("service-list boundary is invalid")
	}
	encoded, err := json.Marshal(serviceListCursor{
		CreatedAt: service.Metadata.CreatedAt.UTC(),
		ID:        service.Metadata.ID,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}
