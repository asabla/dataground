package execution

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrProviderBindingSerialization = errors.New("provider binding credentials cannot be serialized")

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

// CredentialEvidenceProviderRequest is the narrow secret-bearing request used
// only to create one temporary provider for a credential non-exposure run.
type CredentialEvidenceProviderRequest struct {
	IsolationDomainID string `json:"-"`
	GatewayID         string `json:"-"`
	Name              string `json:"-"`
	Canary            []byte `json:"-"`
}

// RuntimeConformanceCredentials contains the exact Codex credential fields
// accepted by the checked runtime profile. Callers retain ownership of their
// source bytes; every consumer must clear its private copies.
type RuntimeConformanceCredentials struct {
	AccessToken  []byte `json:"-"`
	RefreshToken []byte `json:"-"`
	AccountID    []byte `json:"-"`
	IDToken      []byte `json:"-"`
}

type RuntimeConformanceProviderRequest struct {
	IsolationDomainID string                        `json:"-"`
	GatewayID         string                        `json:"-"`
	Name              string                        `json:"-"`
	Credentials       RuntimeConformanceCredentials `json:"-"`
}

type RuntimeConformanceProviderRef struct {
	IsolationDomainID string `json:"-"`
	GatewayID         string `json:"-"`
	Name              string `json:"-"`
}

// ProviderBindingManager is the narrow internal lifecycle port used by
// credential-evidence cleanup. It is intentionally separate from sandbox
// execution because ordinary workloads do not own deployment provider state.
type ProviderBindingManager interface {
	DeleteProviderBinding(context.Context, ProviderBindingRef) error
	ObserveProviderBinding(context.Context, ProviderBindingRef) (ProviderBindingObservation, error)
}

// CredentialEvidenceProviderProvisioner is intentionally separate from the
// ordinary provider registry. It can create only the temporary Codex binding
// used by the closed credential-evidence harness.
type CredentialEvidenceProviderProvisioner interface {
	CreateCredentialEvidenceProvider(
		context.Context,
		CredentialEvidenceProviderRequest,
	) (ProviderBinding, error)
}

// RuntimeConformanceProviderProvisioner can create and observe only the exact
// run-derived Codex provider used by the runtime conformance launcher.
type RuntimeConformanceProviderProvisioner interface {
	CreateRuntimeConformanceProvider(
		context.Context,
		RuntimeConformanceProviderRequest,
	) (ProviderBinding, error)
	ObserveRuntimeConformanceProvider(
		context.Context,
		RuntimeConformanceProviderRef,
	) (ProviderBindingObservation, error)
}

func (RuntimeConformanceCredentials) MarshalJSON() ([]byte, error) {
	return nil, ErrProviderBindingSerialization
}

func (RuntimeConformanceProviderRequest) MarshalJSON() ([]byte, error) {
	return nil, ErrProviderBindingSerialization
}

var (
	_ json.Marshaler = RuntimeConformanceCredentials{}
	_ json.Marshaler = RuntimeConformanceProviderRequest{}
)
