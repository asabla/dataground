package reference_test

import (
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/reference"
)

func TestAllReferenceScenariosNormalize(t *testing.T) {
	t.Parallel()

	for _, scenario := range reference.Scenarios() {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()

			fixture, err := reference.Load(scenario)
			if err != nil {
				t.Fatalf("load fixture: %v", err)
			}
			normalized, err := reference.Normalize(fixture.Events)
			if err != nil {
				t.Fatalf("normalize fixture: %v", err)
			}
			if len(normalized) == 0 {
				t.Fatal("expected at least one normalized event")
			}
			for index, event := range normalized {
				if event.Sequence != uint64(index+1) {
					t.Fatalf("expected sequence %d, got %d", index+1, event.Sequence)
				}
			}
		})
	}
}

func TestNormalizeRejectsConflictingDuplicate(t *testing.T) {
	t.Parallel()

	fixture, err := reference.Load(reference.ScenarioDuplicate)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	fixture.Events[2].Payload = map[string]any{"text": "conflicting content"}

	_, err = reference.Normalize(fixture.Events)
	if !errors.Is(err, reference.ErrConflictingDuplicate) {
		t.Fatalf("expected conflicting duplicate error, got %v", err)
	}
}

func TestNormalizeDeduplicatesAndReorders(t *testing.T) {
	t.Parallel()

	duplicate, err := reference.Load(reference.ScenarioDuplicate)
	if err != nil {
		t.Fatalf("load duplicate fixture: %v", err)
	}
	normalized, err := reference.Normalize(duplicate.Events)
	if err != nil {
		t.Fatalf("normalize duplicate fixture: %v", err)
	}
	if len(duplicate.Events) != 4 || len(normalized) != 3 {
		t.Fatalf("expected one duplicate to be removed, got %d raw and %d normalized", len(duplicate.Events), len(normalized))
	}

	outOfOrder, err := reference.Load(reference.ScenarioOutOfOrder)
	if err != nil {
		t.Fatalf("load out-of-order fixture: %v", err)
	}
	if outOfOrder.Events[1].Sequence != 3 || outOfOrder.Events[2].Sequence != 2 {
		t.Fatal("out-of-order fixture no longer exercises reordering")
	}
	normalized, err = reference.Normalize(outOfOrder.Events)
	if err != nil {
		t.Fatalf("normalize out-of-order fixture: %v", err)
	}
	for index, event := range normalized {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("expected sequence %d, got %d", index+1, event.Sequence)
		}
	}
}

func TestUnknownScenarioFailsClosed(t *testing.T) {
	t.Parallel()

	if _, err := reference.Load("unregistered"); err == nil {
		t.Fatal("expected an unknown scenario error")
	}
}
