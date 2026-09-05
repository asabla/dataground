package domain

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrQuestionInvalid = errors.New("question content or answer is invalid")
var questionItemIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// QuestionPrompt contains bounded, untrusted plain text. IDs identify choices
// within this request; adapters must replace provider-native identifiers.
type QuestionPrompt struct {
	ID            string           `json:"id"`
	Title         string           `json:"title"`
	Prompt        string           `json:"prompt"`
	Options       []QuestionOption `json:"options,omitempty"`
	Multiple      bool             `json:"multiple"`
	AllowFreeText bool             `json:"allowFreeText"`
}

type QuestionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// QuestionAnswer supplies either selected option IDs or explicit free text.
// A missing answer never implies selection of a default option.
type QuestionAnswer struct {
	QuestionID string   `json:"questionId"`
	OptionIDs  []string `json:"optionIds,omitempty"`
	Text       *string  `json:"text,omitempty"`
}

func ValidateQuestionPrompts(prompts []QuestionPrompt) error {
	if len(prompts) < 1 || len(prompts) > 3 {
		return ErrQuestionInvalid
	}
	seen := map[string]bool{}
	for _, prompt := range prompts {
		if !questionItemIDPattern.MatchString(prompt.ID) || seen[prompt.ID] || !questionText(prompt.Title, 128, false) || !questionText(prompt.Prompt, 2048, false) {
			return ErrQuestionInvalid
		}
		seen[prompt.ID] = true
		if len(prompt.Options) == 0 {
			if !prompt.AllowFreeText || prompt.Multiple {
				return ErrQuestionInvalid
			}
		} else if len(prompt.Options) < 2 || len(prompt.Options) > 4 {
			return ErrQuestionInvalid
		}
		ids, labels := map[string]bool{}, map[string]bool{}
		for _, option := range prompt.Options {
			if !questionItemIDPattern.MatchString(option.ID) || ids[option.ID] || labels[strings.TrimSpace(option.Label)] || !questionText(option.Label, 256, false) || !questionText(option.Description, 1024, true) {
				return ErrQuestionInvalid
			}
			ids[option.ID] = true
			labels[strings.TrimSpace(option.Label)] = true
		}
	}
	encoded, err := json.Marshal(prompts)
	if err != nil || len(encoded) > 32768 {
		return ErrQuestionInvalid
	}
	return nil
}

func ValidateQuestionAnswers(prompts []QuestionPrompt, answers []QuestionAnswer) error {
	if ValidateQuestionPrompts(prompts) != nil || len(answers) != len(prompts) {
		return ErrQuestionInvalid
	}
	byID := map[string]QuestionPrompt{}
	for _, prompt := range prompts {
		byID[prompt.ID] = prompt
	}
	seen := map[string]bool{}
	for _, answer := range answers {
		prompt, exists := byID[answer.QuestionID]
		if !exists || seen[answer.QuestionID] {
			return ErrQuestionInvalid
		}
		seen[answer.QuestionID] = true
		if answer.Text != nil {
			if !prompt.AllowFreeText || len(answer.OptionIDs) != 0 || !questionText(*answer.Text, 4096, false) {
				return ErrQuestionInvalid
			}
			continue
		}
		if len(answer.OptionIDs) == 0 || len(answer.OptionIDs) > len(prompt.Options) || (!prompt.Multiple && len(answer.OptionIDs) != 1) {
			return ErrQuestionInvalid
		}
		selected := map[string]bool{}
		for _, id := range answer.OptionIDs {
			found := false
			for _, option := range prompt.Options {
				if id == option.ID {
					found = true
					break
				}
			}
			if !found || selected[id] {
				return ErrQuestionInvalid
			}
			selected[id] = true
		}
	}
	encoded, err := json.Marshal(answers)
	if err != nil || len(encoded) > 16384 {
		return ErrQuestionInvalid
	}
	return nil
}

func questionText(value string, maximum int, allowEmpty bool) bool {
	if !utf8.ValidString(value) || len(value) > maximum || (!allowEmpty && strings.TrimSpace(value) == "") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}
