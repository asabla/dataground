package postgres

import (
	"errors"
	"testing"
)

func TestEnforcementBundlePersistenceFailureIsSafeAndClassifiable(t *testing.T) {
	err := enforcementBundlePersistenceFailure(EnforcementBundlePersistenceInsert)
	stage, ok := EnforcementBundlePersistenceFailure(err)
	if !ok || stage != EnforcementBundlePersistenceInsert || !errors.Is(err, ErrPersistence) {
		t.Fatalf("stage = %q, classified = %v, error = %v", stage, ok, err)
	}
	if err.Error() != "enforcement bundle persistence failed during insert" {
		t.Fatalf("error = %q", err)
	}
}
