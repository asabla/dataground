package canaryprovider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"

	"github.com/asabla/dataground/internal/execution"
)

const (
	canaryPrefix       = "dataground-canary-v1:"
	canaryEntropyBytes = 32
)

var (
	ErrInvalidProvisioning    = errors.New("invalid credential provider provisioning configuration")
	ErrProvisioning           = errors.New("credential provider provisioning failed")
	ErrProvisionSerialization = errors.New("credential provider provisioning cannot be serialized")
	provisionRunIDPattern     = regexp.MustCompile("^[a-f0-9]{32}$")
)

type ProvisionConfig struct {
	RunID             string
	IsolationDomainID string
	GatewayID         string
}

// Provisioned is the secret-free result of creating one temporary provider.
// The canary plaintext is cleared before this value is returned.
type Provisioned struct {
	state *provisionedState
}

type provisionedState struct {
	binding    execution.ProviderBinding
	commitment string
}

// Provision generates one cryptographically random structured canary and asks
// the narrow provider boundary to create the exact run-derived Codex binding.
func Provision(
	ctx context.Context,
	config ProvisionConfig,
	provisioner execution.CredentialEvidenceProviderProvisioner,
) (*Provisioned, error) {
	return provisionWithEntropy(ctx, config, provisioner, rand.Reader)
}

func provisionWithEntropy(
	ctx context.Context,
	config ProvisionConfig,
	provisioner execution.CredentialEvidenceProviderProvisioner,
	entropy io.Reader,
) (*Provisioned, error) {
	if ctx == nil ||
		isNilProvisioner(provisioner) ||
		entropy == nil ||
		!provisionRunIDPattern.MatchString(config.RunID) ||
		config.IsolationDomainID == "" ||
		config.GatewayID == "" {
		return nil, ErrInvalidProvisioning
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(ErrProvisioning, err)
	}

	random := make([]byte, canaryEntropyBytes)
	if _, err := io.ReadFull(entropy, random); err != nil {
		clear(random)
		return nil, provisioningError(ctx)
	}
	canary := make([]byte, len(canaryPrefix)+base64.RawURLEncoding.EncodedLen(len(random)))
	copy(canary, canaryPrefix)
	base64.RawURLEncoding.Encode(canary[len(canaryPrefix):], random)
	clear(random)
	defer clear(canary)

	digest := sha256.Sum256(canary)
	commitment := "sha256:" + hex.EncodeToString(digest[:])
	name := providerNamePrefix + config.RunID
	binding, err := provisioner.CreateCredentialEvidenceProvider(
		ctx,
		execution.CredentialEvidenceProviderRequest{
			IsolationDomainID: config.IsolationDomainID,
			GatewayID:         config.GatewayID,
			Name:              name,
			Canary:            canary,
		},
	)
	if err != nil {
		return nil, provisioningError(ctx)
	}
	if binding.IsolationDomainID != config.IsolationDomainID ||
		binding.GatewayID != config.GatewayID ||
		binding.Name != name ||
		binding.ID == "" ||
		binding.ResourceVersion == 0 {
		return nil, ErrProvisioning
	}

	return &Provisioned{state: &provisionedState{
		binding:    binding,
		commitment: commitment,
	}}, nil
}

func (provisioned *Provisioned) Binding() execution.ProviderBinding {
	if provisioned == nil || provisioned.state == nil {
		return execution.ProviderBinding{}
	}
	return provisioned.state.binding
}

func (provisioned *Provisioned) Commitment() string {
	if provisioned == nil || provisioned.state == nil {
		return ""
	}
	return provisioned.state.commitment
}

func (Provisioned) MarshalJSON() ([]byte, error) {
	return nil, ErrProvisionSerialization
}

func provisioningError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrProvisioning, err)
	}
	return ErrProvisioning
}

func isNilProvisioner(provisioner execution.CredentialEvidenceProviderProvisioner) bool {
	if provisioner == nil {
		return true
	}
	value := reflect.ValueOf(provisioner)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

var _ json.Marshaler = Provisioned{}
