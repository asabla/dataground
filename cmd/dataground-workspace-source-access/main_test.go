package main

import (
	"strings"
	"testing"
)

func accessArgs(operation string) []string {
	args := []string{operation, "--isolation-domain", "iso_" + strings.Repeat("1", 20), "--workspace", "wsp_" + strings.Repeat("2", 20), "--source", "src_" + strings.Repeat("3", 20), "--archive-digest", "sha256:" + strings.Repeat("a", 64), "--storage-profile-digest", "sha256:" + strings.Repeat("b", 64), "--revision", "rev_" + strings.Repeat("4", 20), "--generation", "1", "--actor", "operator", "--correlation-id", "cor_" + strings.Repeat("5", 20)}
	switch operation {
	case "activate":
		return append(args, "--reason", "reviewed source use", "--activated-at", "2030-01-01T00:00:00Z", "--expires-at", "2030-01-01T00:30:00Z")
	case "revoke":
		return append(args, "--reason", "revoke reviewed source use")
	case "verify":
		return append(args, "--operation-id", "op_"+strings.Repeat("6", 20), "--storage-endpoint", "http://127.0.0.1:8333", "--storage-bucket", "sources")
	}
	return args
}
func TestSourceAccessCommandsValidateExplicitScopeAndClosedFlags(t *testing.T) {
	for _, operation := range []string{"activate", "revoke", "verify"} {
		if _, err := parseOptions(accessArgs(operation)); err != nil {
			t.Fatal(operation, err)
		}
	}
	for _, args := range [][]string{nil, {"unknown"}, append(accessArgs("activate"), "--generation", "2"), append(accessArgs("revoke"), "--expires-at", "2030-01-01T00:30:00Z"), append(accessArgs("verify"), "--reason", "should not activate"), accessArgs("activate")[:len(accessArgs("activate"))-2], append(accessArgs("verify"), "extra")} {
		if _, err := parseOptions(args); err == nil {
			t.Fatal("incomplete, duplicate or unrelated flag accepted")
		}
	}
	for _, value := range []string{"0", "-1", "01", "+1", "9223372036854775808"} {
		args := accessArgs("activate")
		for i, v := range args {
			if v == "--generation" {
				args[i+1] = value
			}
		}
		if _, err := parseOptions(args); err == nil {
			t.Fatal("noncanonical generation accepted")
		}
	}
	for _, endpoint := range []string{"http://localhost:8333", "http://192.0.2.1:8333", "http://secret@127.0.0.1:8333", "http://127.0.0.1:8333/path", "http://127.0.0.1:8333?", "https://127.0.0.1:8333"} {
		args := accessArgs("verify")
		for i, v := range args {
			if v == "--storage-endpoint" {
				args[i+1] = endpoint
			}
		}
		if _, err := parseOptions(args); err == nil {
			t.Fatal("unreviewed storage endpoint accepted")
		}
	}
	for _, pair := range [][2]string{{"--expires-at", "2030-01-01T01:00:00.000001Z"}, {"--activated-at", "2030-01-01T00:00:00.000000001Z"}, {"--reason", "secret\nline"}, {"--actor", "bad\nactor"}, {"--revision", "wildcard"}} {
		args := accessArgs("activate")
		for i, v := range args {
			if v == pair[0] {
				args[i+1] = pair[1]
			}
		}
		if _, err := parseOptions(args); err == nil {
			t.Fatal("invalid grant accepted", pair[0])
		}
	}
}
