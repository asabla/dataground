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

var (
	ErrEffectClaimRequired = errors.New("effect route requires an active operation claim")
	ErrEffectClaimMismatch = errors.New("effect route does not match the operation claim")
)

// RoutedDriver directs selected durable effects to specialized drivers while
// preserving one fallback for all routes without explicit ownership.
type RoutedDriver struct {
	fallback      EffectDriver
	routes        map[EffectRoute]EffectDriver
	claimedRoutes map[EffectRoute]ClaimedEffectDriver
}

func NewRoutedDriver(fallback EffectDriver, routes map[EffectRoute]EffectDriver) (*RoutedDriver, error) {
	return newRoutedDriver(fallback, routes, nil)
}

// NewClaimedRoutedDriver adds routes that cannot be called without the exact
// active operation claim. Ordinary routes and the fallback retain the original
// effect-only contract.
func NewClaimedRoutedDriver(
	fallback EffectDriver,
	routes map[EffectRoute]EffectDriver,
	claimedRoutes map[EffectRoute]ClaimedEffectDriver,
) (*RoutedDriver, error) {
	return newRoutedDriver(fallback, routes, claimedRoutes)
}

func newRoutedDriver(
	fallback EffectDriver,
	routes map[EffectRoute]EffectDriver,
	claimedRoutes map[EffectRoute]ClaimedEffectDriver,
) (*RoutedDriver, error) {
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
	ownedClaimed := make(map[EffectRoute]ClaimedEffectDriver, len(claimedRoutes))
	for route, driver := range claimedRoutes {
		if route.OperationKind == "" || route.Phase == "" || driver == nil {
			return nil, errors.New("complete claimed effect routes are required")
		}
		if owned[route] != nil {
			return nil, errors.New("effect route cannot be both ordinary and claim-bound")
		}
		ownedClaimed[route] = driver
	}
	return &RoutedDriver{fallback: fallback, routes: owned, claimedRoutes: ownedClaimed}, nil
}

func (driver *RoutedDriver) Observe(
	ctx context.Context,
	effect persistence.EffectRecord,
) (map[string]any, bool, error) {
	if driver.claimedForEffect(effect) != nil {
		return nil, false, ErrEffectClaimRequired
	}
	return driver.forEffect(effect).Observe(ctx, effect)
}

func (driver *RoutedDriver) Apply(
	ctx context.Context,
	effect persistence.EffectRecord,
) (map[string]any, error) {
	if driver.claimedForEffect(effect) != nil {
		return nil, ErrEffectClaimRequired
	}
	return driver.forEffect(effect).Apply(ctx, effect)
}

func (driver *RoutedDriver) ObserveClaimed(
	ctx context.Context,
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
) (map[string]any, bool, error) {
	if selected := driver.claimedForEffect(effect); selected != nil {
		if !effectMatchesClaim(effect, claim) {
			return nil, false, ErrEffectClaimMismatch
		}
		return selected.ObserveClaimed(ctx, claim, effect)
	}
	return driver.forEffect(effect).Observe(ctx, effect)
}

func (driver *RoutedDriver) ApplyClaimed(
	ctx context.Context,
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
) (map[string]any, error) {
	if selected := driver.claimedForEffect(effect); selected != nil {
		if !effectMatchesClaim(effect, claim) {
			return nil, ErrEffectClaimMismatch
		}
		return selected.ApplyClaimed(ctx, claim, effect)
	}
	return driver.forEffect(effect).Apply(ctx, effect)
}

func (driver *RoutedDriver) forEffect(effect persistence.EffectRecord) EffectDriver {
	if selected := driver.routes[effectRoute(effect)]; selected != nil {
		return selected
	}
	return driver.fallback
}

func (driver *RoutedDriver) claimedForEffect(effect persistence.EffectRecord) ClaimedEffectDriver {
	return driver.claimedRoutes[effectRoute(effect)]
}

func effectRoute(effect persistence.EffectRecord) EffectRoute {
	return EffectRoute{OperationKind: effect.OperationKind, Phase: effect.Phase}
}

func effectMatchesClaim(effect persistence.EffectRecord, claim persistence.OperationClaim) bool {
	return effect.IsolationDomainID != "" &&
		effect.OperationKind != "" &&
		effect.OperationID != "" &&
		claim.IsolationDomainID == effect.IsolationDomainID &&
		claim.Kind == effect.OperationKind &&
		claim.ID == effect.OperationID &&
		claim.Command != "" &&
		claim.ObservedState != "" &&
		claim.LeaseOwner != "" &&
		claim.FencingToken > 0
}

var (
	_ EffectDriver        = (*RoutedDriver)(nil)
	_ ClaimedEffectDriver = (*RoutedDriver)(nil)
)
