package reconcile

import (
	"context"
	"errors"

	"github.com/asabla/dataground/internal/persistence"
)

type EffectRoute struct {
	OperationKind string
	Phase         string
}

// RoutedDriver directs selected durable effects to specialized drivers while
// preserving one fallback for all unclaimed routes.
type RoutedDriver struct {
	fallback EffectDriver
	routes   map[EffectRoute]EffectDriver
}

func NewRoutedDriver(fallback EffectDriver, routes map[EffectRoute]EffectDriver) (*RoutedDriver, error) {
	if fallback == nil {
		return nil, errors.New("fallback effect driver is required")
	}
	owned := make(map[EffectRoute]EffectDriver, len(routes))
	for route, driver := range routes {
		if route.OperationKind == "" || route.Phase == "" || driver == nil {
			return nil, errors.New("complete effect routes are required")
		}
		owned[route] = driver
	}
	return &RoutedDriver{fallback: fallback, routes: owned}, nil
}

func (driver *RoutedDriver) Observe(
	ctx context.Context,
	effect persistence.EffectRecord,
) (map[string]any, bool, error) {
	return driver.forEffect(effect).Observe(ctx, effect)
}

func (driver *RoutedDriver) Apply(
	ctx context.Context,
	effect persistence.EffectRecord,
) (map[string]any, error) {
	return driver.forEffect(effect).Apply(ctx, effect)
}

func (driver *RoutedDriver) forEffect(effect persistence.EffectRecord) EffectDriver {
	if selected := driver.routes[EffectRoute{
		OperationKind: effect.OperationKind,
		Phase:         effect.Phase,
	}]; selected != nil {
		return selected
	}
	return driver.fallback
}

var _ EffectDriver = (*RoutedDriver)(nil)
