package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/domain"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

type nativeQuestion struct {
	ID       string          `json:"id"`
	Header   string          `json:"header"`
	Question string          `json:"question"`
	IsOther  json.RawMessage `json:"isOther"`
	IsSecret json.RawMessage `json:"isSecret"`
	Options  []struct {
		Label       string  `json:"label"`
		Description *string `json:"description"`
	} `json:"options"`
}

type pendingQuestion struct {
	requestID json.RawMessage
	nativeKey string
	nativeIDs []string
	prompts   []domain.QuestionPrompt
	expiresAt time.Time
	timer     *time.Timer
	resolving bool
}

func (client *Client) handleQuestionRequest(message wireMessage) {
	reject := func(code int, reason string) {
		if err := client.respondError(message.ID, code, reason); err != nil {
			client.fail(err)
		}
	}
	client.stateMu.Lock()
	enabled := client.questionMode == dgruntime.QuestionInteractive && !client.interactionsClosed
	client.stateMu.Unlock()
	if !enabled {
		reject(-32601, "questions are unavailable for this turn")
		return
	}
	var params struct {
		ThreadID  string           `json:"threadId"`
		TurnID    string           `json:"turnId"`
		ItemID    string           `json:"itemId"`
		Questions []nativeQuestion `json:"questions"`
	}
	if len(message.Params) > 32768 || !utf8.Valid(message.Params) || !validQuestionJSON(message.Params) {
		reject(-32602, "question content is invalid")
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(message.Params))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&params) != nil || !client.matchesActiveTurn(params.ThreadID, params.TurnID) || !validQuestionNativeID(params.ItemID) {
		reject(-32602, "question scope or content is invalid")
		return
	}
	prompts := make([]domain.QuestionPrompt, 0, len(params.Questions))
	seenNativeIDs := map[string]bool{}
	nativeIDs := make([]string, 0, len(params.Questions))
	for index, question := range params.Questions {
		other, validOther := questionBoolean(question.IsOther)
		secret, validSecret := questionBoolean(question.IsSecret)
		if !validOther || !validSecret || secret || !validQuestionNativeID(question.ID) || seenNativeIDs[question.ID] {
			reject(-32602, "question content is unsupported")
			return
		}
		seenNativeIDs[question.ID] = true
		nativeIDs = append(nativeIDs, question.ID)
		prompt := domain.QuestionPrompt{ID: fmt.Sprintf("item_%d", index+1), Title: question.Header, Prompt: question.Question, AllowFreeText: other, Options: make([]domain.QuestionOption, 0, len(question.Options))}
		for optionIndex, option := range question.Options {
			if option.Description == nil {
				reject(-32602, "question content is invalid")
				return
			}
			prompt.Options = append(prompt.Options, domain.QuestionOption{ID: fmt.Sprintf("option_%d", optionIndex+1), Label: option.Label, Description: *option.Description})
		}
		prompts = append(prompts, prompt)
	}
	if domain.ValidateQuestionPrompts(prompts) != nil {
		reject(-32602, "question content is unsupported")
		return
	}
	key, _ := nativeRequestKey(message.ID)
	client.stateMu.Lock()
	_, duplicate := client.nativeRequests[key]
	if client.interactionsClosed || duplicate || len(client.questions) != 0 {
		client.stateMu.Unlock()
		reject(-32600, "question request is duplicate or unavailable")
		return
	}
	client.nextQuestion++
	id := fmt.Sprintf("question-%d", client.nextQuestion)
	pending := &pendingQuestion{requestID: append(json.RawMessage(nil), message.ID...), nativeKey: key, nativeIDs: nativeIDs, prompts: prompts, expiresAt: time.Now().UTC().Add(client.questionTimeout)}
	client.questions[id] = pending
	client.nativeRequests[key] = struct{}{}
	pending.timer = time.AfterFunc(client.questionTimeout, func() {
		client.stateMu.Lock()
		current, exists := client.questions[id]
		expired := exists && current == pending
		if expired {
			client.closeInteractionsLocked()
		}
		client.stateMu.Unlock()
		// An unanswered request never selects a default or sends an empty answer.
		// Stop the transport so headless or abandoned turns cannot wait indefinitely.
		if expired {
			client.fail(dgruntime.ErrQuestionExpired)
		}
	})
	client.stateMu.Unlock()
	client.emit("interaction.question.requested", map[string]any{"questionId": id, "questions": cloneQuestionPrompts(prompts), "expiresAt": pending.expiresAt.Format(time.RFC3339Nano)})
}

// QuestionPending exposes only adapter-local interaction authority. It lets the
// worker retire its durable request after native clearance without exposing a
// native handle or sending an answer to probe whether the request still exists.
func (client *Client) QuestionPending(ctx context.Context, id string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	client.stateMu.Lock()
	defer client.stateMu.Unlock()
	pending, exists := client.questions[id]
	return exists && !client.interactionsClosed && !pending.resolving && time.Now().Before(pending.expiresAt), nil
}

func (client *Client) AnswerQuestion(ctx context.Context, id string, answers []domain.QuestionAnswer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client.stateMu.Lock()
	pending, exists := client.questions[id]
	if !exists || client.interactionsClosed || pending.resolving {
		client.stateMu.Unlock()
		return dgruntime.ErrQuestionNotFound
	}
	if !time.Now().Before(pending.expiresAt) {
		client.stateMu.Unlock()
		return dgruntime.ErrQuestionExpired
	}
	if err := domain.ValidateQuestionAnswers(pending.prompts, answers); err != nil {
		client.stateMu.Unlock()
		return err
	}
	nativeAnswers := map[string]any{}
	for _, answer := range answers {
		for index, prompt := range pending.prompts {
			if prompt.ID != answer.QuestionID {
				continue
			}
			values := []string{}
			if answer.Text != nil {
				values = append(values, *answer.Text)
			} else {
				// Native responses use display labels. Only labels from the frozen request
				// can be selected; provider routing IDs never enter the normalized answer.
				for _, optionID := range answer.OptionIDs {
					for _, option := range prompt.Options {
						if option.ID == optionID {
							values = append(values, option.Label)
						}
					}
				}
			}
			nativeAnswers[pending.nativeIDs[index]] = map[string]any{"answers": values}
		}
	}
	pending.resolving = true
	requestID := append(json.RawMessage(nil), pending.requestID...)
	client.stateMu.Unlock()
	guard := func() error {
		client.stateMu.Lock()
		defer client.stateMu.Unlock()
		current, exists := client.questions[id]
		if !exists || current != pending || client.interactionsClosed {
			return dgruntime.ErrQuestionNotFound
		}
		if !time.Now().Before(current.expiresAt) {
			return dgruntime.ErrQuestionExpired
		}
		return nil
	}
	if err := client.writeGuarded(ctx, map[string]any{"id": requestID, "result": map[string]any{"answers": nativeAnswers}}, guard); err != nil {
		client.stateMu.Lock()
		if current, exists := client.questions[id]; exists && current == pending && !client.interactionsClosed {
			current.resolving = false
		}
		client.stateMu.Unlock()
		return err
	}
	client.stateMu.Lock()
	if current, exists := client.questions[id]; exists && current == pending {
		current.timer.Stop()
		delete(client.questions, id)
		delete(client.nativeRequests, current.nativeKey)
	}
	client.stateMu.Unlock()
	return nil
}

func questionBoolean(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 {
		return false, true
	}
	switch string(bytes.TrimSpace(raw)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}
func validQuestionNativeID(value string) bool {
	if strings.TrimSpace(value) == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func cloneQuestionPrompts(prompts []domain.QuestionPrompt) []domain.QuestionPrompt {
	cloned := append([]domain.QuestionPrompt(nil), prompts...)
	for index := range cloned {
		cloned[index].Options = append([]domain.QuestionOption(nil), prompts[index].Options...)
	}
	return cloned
}

var _ dgruntime.QuestionTurn = (*Client)(nil)

// Reject ambiguous JSON before Go's case-insensitive struct decoder can select
// one of two spellings of a security-relevant field such as isSecret.
func validQuestionJSON(raw []byte) bool {
	allowed := map[string]bool{"threadId": true, "turnId": true, "itemId": true, "questions": true, "id": true, "header": true, "question": true, "isOther": true, "isSecret": true, "options": true, "label": true, "description": true}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 8 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return true
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || !allowed[key] || seen[key] {
					return false
				}
				seen[key] = true
				if !value(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for decoder.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !value(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}
