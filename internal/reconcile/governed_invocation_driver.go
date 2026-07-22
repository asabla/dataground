package reconcile

import (
	"errors"

	"github.com/asabla/dataground/internal/persistence"
)

var ErrGovernedInvocationDriversIncomplete = errors.New("governed invocation drivers are incomplete")

// NewGovernedInvocationDriver composes the complete version 2 invocation
// effect lifecycle without changing the default worker. Admission and
// cancellation remain observable effect-only routes; runtime execution always
// requires the exact active operation claim.
func NewGovernedInvocationDriver(
	fallback EffectDriver,
	admission *InvocationAdmissionDriver,
	runtime *InvocationRuntimeDriver,
	cancellation *InvocationCancellationDriver,
) (*RoutedDriver, error) {
	if fallback == nil || admission == nil || runtime == nil || cancellation == nil {
		return nil, ErrGovernedInvocationDriversIncomplete
	}
	return NewClaimedRoutedDriver(
		fallback,
		map[EffectRoute]EffectDriver{
			{
				OperationKind: persistence.OperationKindInvocation,
				Phase:         "start-invocation",
			}: admission,
			{
				OperationKind: persistence.OperationKindInvocation,
				Phase:         "cancel-invocation",
			}: cancellation,
		},
		map[EffectRoute]ClaimedEffectDriver{
			{
				OperationKind: persistence.OperationKindInvocation,
				Phase:         "run-invocation",
			}: runtime,
		},
	)
}
