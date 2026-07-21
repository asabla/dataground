package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRejectsNonLoopbackAndImplicitPlaintextEndpoints(t *testing.T) {
	tests := map[string][]string{
		"remote HTTPS": {
			"--endpoint", "https://s3.example.test", "--bucket", "disposable",
			"--run-id", "0123456789abcdef0123456789abcdef", "--allow-loopback-http",
		},
		"remote HTTP": {
			"--endpoint", "http://s3.example.test", "--bucket", "disposable",
			"--run-id", "0123456789abcdef0123456789abcdef", "--allow-loopback-http",
		},
		"implicit loopback HTTP": {
			"--endpoint", "http://127.0.0.1:8333", "--bucket", "disposable",
			"--run-id", "0123456789abcdef0123456789abcdef",
		},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := run(context.Background(), args, &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d", code)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "invalid S3 conformance command configuration") ||
				strings.Contains(stderr.String(), "example.test") {
				t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestHTTPStyleLoopbackValidation(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:8333", "http://[::1]:8333"} {
		if !isHTTPStyleLoopback(endpoint) {
			t.Fatalf("loopback endpoint rejected: %s", endpoint)
		}
	}
	for _, endpoint := range []string{
		"https://127.0.0.1:8333", "http://192.0.2.1:8333", "http://localhost:8333", "not-a-url",
	} {
		if isHTTPStyleLoopback(endpoint) {
			t.Fatalf("unsafe endpoint accepted: %s", endpoint)
		}
	}
}
