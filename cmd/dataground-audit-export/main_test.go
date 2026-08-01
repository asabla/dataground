package main

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestRunRejectsInvalidAuthorizationAuditExportArgumentsBeforeDatabaseAccess(t *testing.T) {
	t.Setenv("DATAGROUND_DATABASE_URL", "")
	for name, arguments := range map[string][]string{
		"missing domain": nil,
		"zero limit":     {"-isolation-domain", "iso_00000000000000000001", "-limit", "0"},
		"large limit":    {"-isolation-domain", "iso_00000000000000000001", "-limit", "1001"},
		"extra argument": {"-isolation-domain", "iso_00000000000000000001", "unexpected"},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), arguments, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "isolation-domain is required") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
