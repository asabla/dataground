package execution

import (
	"errors"
	"regexp"
	"slices"
	"sort"
)

var (
	ErrProviderProfileInvalid     = errors.New("execution provider profile is invalid")
	ErrProviderProfileUnavailable = errors.New("execution provider profile is unavailable")
	providerProfilePattern        = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
)

// ProviderProfileRegistry is an immutable set of deployment-owned provider
// identities. It carries names only; provider configuration and credentials
// remain inside the execution provider.
type ProviderProfileRegistry struct {
	names map[string]struct{}
}

func NewProviderProfileRegistry(names []string) (*ProviderProfileRegistry, error) {
	registry := &ProviderProfileRegistry{names: make(map[string]struct{}, len(names))}
	for _, name := range names {
		if len(name) > 64 || !providerProfilePattern.MatchString(name) {
			return nil, ErrProviderProfileInvalid
		}
		if _, exists := registry.names[name]; exists {
			return nil, ErrProviderProfileInvalid
		}
		registry.names[name] = struct{}{}
	}
	return registry, nil
}

// Resolve validates and owns an exact provider-profile selection.
func (registry *ProviderProfileRegistry) Resolve(names []string) ([]string, error) {
	if registry == nil {
		if len(names) == 0 {
			return nil, nil
		}
		return nil, ErrProviderProfileUnavailable
	}
	resolved := slices.Clone(names)
	for _, name := range resolved {
		if len(name) > 64 || !providerProfilePattern.MatchString(name) {
			return nil, ErrProviderProfileInvalid
		}
		if _, exists := registry.names[name]; !exists {
			return nil, ErrProviderProfileUnavailable
		}
	}
	sort.Strings(resolved)
	return slices.Compact(resolved), nil
}
