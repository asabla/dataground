package domain

import (
	"strings"
	"unicode/utf8"
)

// Workspace metadata is display content, never a host path or execution grant.
func ValidWorkspaceMetadata(name, description string) bool {
	return name == strings.TrimSpace(name) && len(name) > 0 && len(name) <= 128 && len(description) <= 2048 && utf8.ValidString(name) && utf8.ValidString(description) && !strings.ContainsRune(name, 0) && !strings.ContainsRune(description, 0)
}
