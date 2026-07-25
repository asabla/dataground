package execution

import (
	"context"
	"time"
)

// ProviderBinding is one exact provider credential resource returned by an
// execution provider. Native binding and gateway identities remain internal.
type ProviderBinding struct {
	IsolationDomainID string `json:"-"`
	GatewayID         string `json:"-"`
	ID                string `json:"-"`
	Name              string `json:"-"`
	ResourceVersion   uint64 `json:"-"`
}

type ProviderBindingRef struct {
	IsolationDomainID string `json:"-"`
	GatewayID         string `json:"-"`
	ID                string `json:"-"`
	Name              string `json:"-"`
	ResourceVersion   uint64 `json:"-"`
}

type ProviderBindingObservation struct {
	IsolationDomainID string    `json:"-"`
	GatewayID         string    `json:"-"`
	ID                string    `json:"-"`
	Name              string    `json:"-"`
	ResourceVersion   uint64    `json:"-"`
	Exists            bool      `json:"-"`
	ObservedAt        time.Time `json:"-"`
}

// ProviderBindingManager is the narrow internal lifecycle port used by
// credential-evidence cleanup. It is intentionally separate from sandbox
// execution because ordinary workloads do not own deployment provider state.
type ProviderBindingManager interface {
	DeleteProviderBinding(context.Context, ProviderBindingRef) error
	ObserveProviderBinding(context.Context, ProviderBindingRef) (ProviderBindingObservation, error)
}
