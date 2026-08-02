package main

import "testing"

func TestRunRejectsIncompleteArgumentsBeforeBuildInspection(t *testing.T) {
	t.Parallel()
	for _, arguments := range [][]string{
		nil,
		{"-trust-file", "/tmp/trust.json", "unexpected"},
		{"-statement-file", "/tmp/statement.json"},
	} {
		if err := run(arguments); err == nil {
			t.Fatalf("run(%q) accepted incomplete arguments", arguments)
		}
	}
}
