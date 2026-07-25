package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/asabla/dataground/internal/execution"
)

const (
	providerBindingPageSize  = 100
	providerBindingPageLimit = 16
)

var providerBindingNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

type providerBindingView struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ResourceVersion uint64 `json:"resource_version"`
}

// ObserveProviderBinding reads credential-safe provider metadata from the exact
// persisted gateway. OpenShell's structured output exposes keys, never values;
// this boundary retains only immutable identity needed for cleanup.
func (provider *Provider) ObserveProviderBinding(
	ctx context.Context,
	ref execution.ProviderBindingRef,
) (execution.ProviderBindingObservation, error) {
	if provider == nil || ctx == nil || !validProviderBindingRef(ref) {
		return execution.ProviderBindingObservation{}, execution.ErrStateConflict
	}
	if err := ctx.Err(); err != nil {
		return execution.ProviderBindingObservation{}, err
	}
	gateway, err := provider.executionContext(ctx, ref.IsolationDomainID, ref.GatewayID)
	if err != nil {
		return execution.ProviderBindingObservation{}, err
	}

	for page := 0; page < providerBindingPageLimit; page++ {
		offset := page * providerBindingPageSize
		result, runErr := provider.runner.Run(
			ctx,
			provider.binary,
			provider.gatewayArgs(
				gateway.Endpoint,
				"provider",
				"list",
				"--limit",
				strconv.Itoa(providerBindingPageSize),
				"--offset",
				strconv.Itoa(offset),
				"--output",
				"json",
			)...,
		)
		if runErr != nil || result.ExitCode != 0 {
			return execution.ProviderBindingObservation{}, ErrProviderFailure
		}

		var bindings []providerBindingView
		if err := json.Unmarshal(result.Stdout, &bindings); err != nil ||
			len(bindings) > providerBindingPageSize {
			return execution.ProviderBindingObservation{}, ErrProviderFailure
		}
		var match *providerBindingView
		for index := range bindings {
			if bindings[index].Name != ref.Name {
				continue
			}
			if match != nil {
				return execution.ProviderBindingObservation{}, ErrProviderFailure
			}
			match = &bindings[index]
		}
		if match != nil {
			if match.ID == "" || match.ResourceVersion == 0 {
				return execution.ProviderBindingObservation{}, ErrProviderFailure
			}
			return execution.ProviderBindingObservation{
				IsolationDomainID: ref.IsolationDomainID,
				GatewayID:         ref.GatewayID,
				ID:                match.ID,
				Name:              match.Name,
				ResourceVersion:   match.ResourceVersion,
				Exists:            true,
				ObservedAt:        provider.now().UTC(),
			}, nil
		}
		if len(bindings) < providerBindingPageSize {
			return absentProviderBinding(ref, provider.now().UTC()), nil
		}
	}
	return execution.ProviderBindingObservation{}, ErrProviderFailure
}

// DeleteProviderBinding removes only the binding that still has the exact
// immutable identity observed at construction. A lost acknowledgement is
// accepted only when a subsequent observation confirms absence.
func (provider *Provider) DeleteProviderBinding(
	ctx context.Context,
	ref execution.ProviderBindingRef,
) error {
	observation, err := provider.ObserveProviderBinding(ctx, ref)
	if err != nil {
		return err
	}
	if !observation.Exists {
		return nil
	}
	if !providerBindingMatchesRef(observation, ref) {
		return execution.ErrStateConflict
	}

	gateway, err := provider.executionContext(ctx, ref.IsolationDomainID, ref.GatewayID)
	if err != nil {
		return err
	}
	result, runErr := provider.runner.Run(
		ctx,
		provider.binary,
		provider.gatewayArgs(gateway.Endpoint, "provider", "delete", ref.Name)...,
	)
	if runErr == nil && result.ExitCode == 0 {
		return nil
	}

	observed, observeErr := provider.ObserveProviderBinding(ctx, ref)
	if observeErr == nil && !observed.Exists {
		return nil
	}
	if observeErr == nil && !providerBindingMatchesRef(observed, ref) {
		return execution.ErrStateConflict
	}
	return ErrProviderFailure
}

func validProviderBindingRef(ref execution.ProviderBindingRef) bool {
	return ref.IsolationDomainID != "" &&
		ref.GatewayID != "" &&
		ref.ID != "" &&
		len(ref.Name) <= 64 &&
		providerBindingNamePattern.MatchString(ref.Name) &&
		ref.ResourceVersion > 0
}

func providerBindingMatchesRef(
	observation execution.ProviderBindingObservation,
	ref execution.ProviderBindingRef,
) bool {
	return observation.Exists &&
		observation.IsolationDomainID == ref.IsolationDomainID &&
		observation.GatewayID == ref.GatewayID &&
		observation.ID == ref.ID &&
		observation.Name == ref.Name &&
		observation.ResourceVersion == ref.ResourceVersion &&
		!observation.ObservedAt.IsZero()
}

func absentProviderBinding(
	ref execution.ProviderBindingRef,
	observedAt time.Time,
) execution.ProviderBindingObservation {
	return execution.ProviderBindingObservation{
		IsolationDomainID: ref.IsolationDomainID,
		GatewayID:         ref.GatewayID,
		Name:              ref.Name,
		Exists:            false,
		ObservedAt:        observedAt,
	}
}

var (
	_ execution.ProviderBindingManager = (*Provider)(nil)
	_                                = errors.Is
)
