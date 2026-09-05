package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const maximumEventReplayPageRecords = 500
const maximumEventReplayPageBytes = 1 << 20

func parseEventReplayLimit(rawQuery string) (int, error) {
	if rawQuery == "" {
		return 0, nil
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return 0, errors.New("invalid event replay query")
	}
	if !query.Has("limit") {
		return 0, nil
	}
	if len(query) != 1 || len(query["limit"]) != 1 {
		return 0, errors.New("invalid event replay query")
	}
	raw := query.Get("limit")
	for _, digit := range raw {
		if digit < '0' || digit > '9' {
			return 0, errors.New("invalid event replay limit")
		}
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > maximumEventReplayPageRecords {
		return 0, errors.New("invalid event replay limit")
	}
	return limit, nil
}

func writeInvocationEventReplay(response http.ResponseWriter, events []EventEnvelope, cursor uint64, limit int) {
	if limit == 0 {
		setEventReplayHeaders(response)
		response.WriteHeader(http.StatusOK)
		for _, event := range events {
			if event.Sequence <= cursor {
				continue
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(response, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, encoded); err != nil {
				return
			}
		}
		return
	}
	// Encode a complete bounded page before releasing any successful headers.
	// A transport interruption can then be retried from the last confirmed cursor.
	var page bytes.Buffer
	count := 0
	hasMore := false
	for _, event := range events {
		if event.Sequence <= cursor {
			continue
		}
		if count == limit {
			hasMore = true
			break
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeError("EVENT_REPLAY_UNAVAILABLE", "Invocation events are temporarily unavailable.", true)})
			return
		}
		frame := fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, encoded)
		if len(frame) > maximumEventReplayPageBytes-page.Len() {
			if count == 0 {
				writeJSON(response, http.StatusRequestEntityTooLarge, ErrorEnvelope{Error: safeError("EVENT_REPLAY_RECORD_TOO_LARGE", "The next event exceeds the bounded replay limit.", false)})
				return
			}
			hasMore = true
			break
		}
		page.WriteString(frame)
		count++
	}
	setEventReplayHeaders(response)
	response.Header().Set("X-DataGround-Has-More", strconv.FormatBool(hasMore))
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(page.Bytes())
}

func setEventReplayHeaders(response http.ResponseWriter) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("X-Accel-Buffering", "no")
	response.Header().Set("X-Content-Type-Options", "nosniff")
}
