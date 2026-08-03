package main

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestRunRejectsInvalidOperatorAuditExportArgumentsBeforeDatabaseAccess(t *testing.T) {
	t.Setenv("DATAGROUND_DATABASE_URL", "")
	valid := []string{
		"-export-id", "oax_00000000000000000001",
		"-isolation-domain", "iso_00000000000000000001",
		"-actor", "operator@example.invalid",
		"-reason", "incident export",
		"-correlation-id", "cor_00000000000000000001",
	}
	for name, arguments := range map[string][]string{
		"missing flags":  nil,
		"zero limit":     append(append([]string{}, valid...), "-limit", "0"),
		"large limit":    append(append([]string{}, valid...), "-limit", "1001"),
		"extra argument": append(append([]string{}, valid...), "unexpected"),
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), arguments, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "export-id") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
