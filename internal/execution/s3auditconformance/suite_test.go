package s3auditconformance

import (
	"context"
	"testing"
)

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	if err := Run(context.Background(), nil, Config{RunID: "invalid"}); err == nil {
		t.Fatal("invalid conformance configuration was accepted")
	}
}
