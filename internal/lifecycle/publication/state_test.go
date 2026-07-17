package publication_test

import (
	"testing"

	"github.com/asabla/dataground/internal/lifecycle/publication"
)

func TestPublicationTransitions(t *testing.T) {
	t.Parallel()

	valid := [][2]publication.State{
		{publication.StateQueued, publication.StateValidating},
		{publication.StateValidating, publication.StateApplying},
		{publication.StateApplying, publication.StateObserving},
		{publication.StateObserving, publication.StatePublished},
		{publication.StateObserving, publication.StateApplying},
		{publication.StateFailed, publication.StateQueued},
	}
	for _, transition := range valid {
		if err := publication.ValidateTransition(transition[0], transition[1]); err != nil {
			t.Errorf("expected transition %q -> %q: %v", transition[0], transition[1], err)
		}
	}

	invalid := [][2]publication.State{
		{publication.StateQueued, publication.StatePublished},
		{publication.StatePublished, publication.StateApplying},
		{publication.StateCancelled, publication.StateQueued},
	}
	for _, transition := range invalid {
		if err := publication.ValidateTransition(transition[0], transition[1]); err == nil {
			t.Errorf("expected transition %q -> %q to fail", transition[0], transition[1])
		}
	}
}

func TestCancellationPreventsPublicationEffects(t *testing.T) {
	t.Parallel()

	if publication.AllowsEffect(publication.CommandCancel, publication.StateApplying, "publish-revision") {
		t.Fatal("cancellation allowed a new publication effect")
	}
	if !publication.AllowsEffect(publication.CommandCancel, publication.StateApplying, "cancel-publication") {
		t.Fatal("cancellation did not allow its cleanup effect")
	}
	if publication.AllowsEffect(publication.CommandPublish, publication.StatePublished, "publish-revision") {
		t.Fatal("terminal publication allowed a new effect")
	}
}
