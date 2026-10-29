package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Service search folds ASCII letters only, independently of the database locale.
// Other Unicode characters and SQL wildcard characters are literal.
func FoldServiceSearch(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, value)
}

func ValidServiceSearch(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func MatchesServiceSearch(service AgentService, query string) bool {
	query = FoldServiceSearch(query)
	return strings.Contains(FoldServiceSearch(service.Name), query) ||
		strings.Contains(FoldServiceSearch(service.Description), query) ||
		strings.Contains(service.Metadata.ID, query)
}
