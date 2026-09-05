package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/domain"
)

func questionPrompts() []domain.QuestionPrompt {
	return []domain.QuestionPrompt{{ID: "item_1", Title: "Destination", Prompt: "Choose a destination.", Options: []domain.QuestionOption{{ID: "option_1", Label: "Local"}, {ID: "option_2", Label: "Remote"}}, AllowFreeText: true}, {ID: "item_2", Title: "Details", Prompt: "Add context.", AllowFreeText: true}}
}
func questionAnswerText(value string) *string { return &value }
func TestQuestionAnswerBindsEveryItemAndPermittedChoice(t *testing.T) {
	prompts := questionPrompts()
	valid := []domain.QuestionAnswer{{QuestionID: "item_2", Text: questionAnswerText("Context")}, {QuestionID: "item_1", OptionIDs: []string{"option_2"}}}
	if err := domain.ValidateQuestionAnswers(prompts, valid); err != nil {
		t.Fatal(err)
	}
	for _, answers := range [][]domain.QuestionAnswer{
		nil, valid[:1], {valid[0], valid[0]},
		{valid[0], {QuestionID: "item_1", OptionIDs: []string{"provider-option"}}},
		{valid[0], {QuestionID: "item_1", OptionIDs: []string{"option_1", "option_2"}}},
		{valid[0], {QuestionID: "item_1", OptionIDs: []string{"option_1"}, Text: questionAnswerText("also text")}},
		{valid[0], {QuestionID: "item_1", Text: questionAnswerText(" ")}},
		{valid[0], {QuestionID: "item_1", Text: questionAnswerText(strings.Repeat("界", 1400))}},
		{valid[0], {QuestionID: "item_1", Text: questionAnswerText("secret\x1bcontrol")}},
	} {
		if err := domain.ValidateQuestionAnswers(prompts, answers); !errors.Is(err, domain.ErrQuestionInvalid) {
			t.Fatal("invalid or incomplete answer accepted")
		}
	}
	prompts[0].Multiple = true
	valid[1].OptionIDs = []string{"option_1", "option_2"}
	if err := domain.ValidateQuestionAnswers(prompts, valid); err != nil {
		t.Fatal(err)
	}
	valid[1].OptionIDs = []string{"option_1", "option_1"}
	if domain.ValidateQuestionAnswers(prompts, valid) == nil {
		t.Fatal("duplicate selection accepted")
	}
	prompts[0].AllowFreeText = false
	valid[1] = domain.QuestionAnswer{QuestionID: "item_1", Text: questionAnswerText("free text")}
	if domain.ValidateQuestionAnswers(prompts, valid) == nil {
		t.Fatal("disabled free text accepted")
	}
}
func TestQuestionPromptsRejectUnboundedAmbiguousOrUnanswerableContent(t *testing.T) {
	for _, change := range []func([]domain.QuestionPrompt) []domain.QuestionPrompt{
		func(p []domain.QuestionPrompt) []domain.QuestionPrompt { return nil },
		func(p []domain.QuestionPrompt) []domain.QuestionPrompt { return append(p, p...) },
		func(p []domain.QuestionPrompt) []domain.QuestionPrompt { p[1].ID = p[0].ID; return p },
		func(p []domain.QuestionPrompt) []domain.QuestionPrompt {
			p[0].Prompt = strings.Repeat("界", 700)
			return p
		},
		func(p []domain.QuestionPrompt) []domain.QuestionPrompt { p[0].Title = "\xff"; return p },
		func(p []domain.QuestionPrompt) []domain.QuestionPrompt { p[0].Options = p[0].Options[:1]; return p },
		func(p []domain.QuestionPrompt) []domain.QuestionPrompt { p[1].AllowFreeText = false; return p },
		func(p []domain.QuestionPrompt) []domain.QuestionPrompt { p[1].Multiple = true; return p },
		func(p []domain.QuestionPrompt) []domain.QuestionPrompt {
			p[0].Options[1].ID = p[0].Options[0].ID
			return p
		},
		func(p []domain.QuestionPrompt) []domain.QuestionPrompt {
			p[0].Options[1].Label = p[0].Options[0].Label
			return p
		},
	} {
		if domain.ValidateQuestionPrompts(change(questionPrompts())) == nil {
			t.Fatal("invalid prompt set accepted")
		}
	}
}
