package reconcile

import (
	"errors"
	"reflect"

	"github.com/asabla/dataground/internal/persistence"
)

var ErrGovernedInvocationDriversIncomplete = errors.New("governed invocation drivers are incomplete")

// NewGovernedInvocationDriver composes the complete version 2 invocation
// effect lifecycle without changing the default worker. Admission remains an
// observable effect-only route; cancellation and runtime execution require the
// exact active operation claim.
func NewGovernedInvocationDriver(
	fallback EffectDriver,
	admission *InvocationAdmissionDriver,
	runtime *InvocationRuntimeDriver,
	cancellation *InvocationCancellationDriver,
) (*RoutedDriver, error) {
	if governedInvocationDependencyMissing(fallback) ||
		governedInvocationDependencyMissing(admission) ||
		governedInvocationDependencyMissing(runtime) ||
		governedInvocationDependencyMissing(cancellation) {
		return nil, ErrGovernedInvocationDriversIncomplete
	}
	return NewClaimedRoutedDriver(
		fallback,
		map[EffectRoute]EffectDriver{
			{
				OperationKind: persistence.OperationKindInvocation,
				Phase:         "start-invocation",
			}: admission,
		},
		map[EffectRoute]ClaimedEffectDriver{
			{
				OperationKind: persistence.OperationKindInvocation,
				Phase:         "cancel-invocation",
			}: cancellation,

			{
				OperationKind: persistence.OperationKindInvocation,
				Phase:         "run-invocation",
			}: runtime,
		},
	)
}

func governedInvocationDependencyMissing(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
