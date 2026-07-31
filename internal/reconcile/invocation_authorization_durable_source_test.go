package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/persistence"
)

func TestDurableInvocationAuthorizationPolicySourceResolvesOwnedExactPolicy(t *testing.T) {
	t.Parallel()

	policy, record := durablePolicyFixture(t)
	store := &durablePolicyStoreStub{record: record}
	source, err := NewDurableInvocationAuthorizationPolicySource(store)
	if err != nil {
		t.Fatalf("construct durable policy source: %v", err)
	}
	scope := InvocationAuthorizationPolicyScope{
		IsolationDomainID: policy.IsolationDomainID,
		ServiceID:         policy.ServiceID,
		RevisionID:        policy.RevisionID,
	}
	first, err := source.ResolveInvocationAuthorizationPolicy(context.Background(), scope)
	if err != nil {
		t.Fatalf("resolve durable policy: %v", err)
	}
	if first.Digest != policy.Digest ||
		first.PolicySetID != policy.PolicySetID ||
		string(first.Schema) != string(policy.Schema) ||
		string(first.Policies) != string(policy.Policies) {
		t.Fatalf("resolved policy = %#v, want %#v", first, policy)
	}
	first.Schema[0] = 'X'
	first.Policies[0] = 'X'
	if store.record.Schema[0] == 'X' || store.record.Policies[0] == 'X' {
		t.Fatal("resolved policy shared store-owned bytes")
	}
}

func TestDurableInvocationAuthorizationPolicySourceFailsClosed(t *testing.T) {
	t.Parallel()

	policy, record := durablePolicyFixture(t)
	scope := InvocationAuthorizationPolicyScope{
		IsolationDomainID: policy.IsolationDomainID,
		ServiceID:         policy.ServiceID,
		RevisionID:        policy.RevisionID,
	}
	tests := map[string]struct {
		record persistence.InvocationAuthorizationPolicyRecord
		err    error
		want   error
	}{
		"missing": {
			err:  persistence.ErrInvocationAuthorizationPolicyRecordMissing,
			want: ErrInvocationAuthorizationPolicyUnavailable,
		},
		"backend failure": {
			err:  errors.New("database detail"),
			want: ErrInvocationAuthorizationPolicyUnavailable,
		},
		"scope drift": {
			record: func() persistence.InvocationAuthorizationPolicyRecord {
				changed := record
				changed.ServiceID = "svc_other"
				return changed
			}(),
			want: ErrInvocationAuthorizationPolicyInvalid,
		},
		"digest drift": {
			record: func() persistence.InvocationAuthorizationPolicyRecord {
				changed := record
				changed.Policies = append(append([]byte(nil), changed.Policies...), ' ')
				return changed
			}(),
			want: ErrInvocationAuthorizationPolicyInvalid,
		},
	}
	for name, test := range tests {
		name, test := name, test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			source, err := NewDurableInvocationAuthorizationPolicySource(
				&durablePolicyStoreStub{record: test.record, err: test.err},
			)
			if err != nil {
				t.Fatalf("construct durable policy source: %v", err)
			}
			_, err = source.ResolveInvocationAuthorizationPolicy(context.Background(), scope)
			if !errors.Is(err, test.want) {
				t.Fatalf("resolve error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDurableInvocationAuthorizationPolicySourceRejectsIncompleteAssembly(t *testing.T) {
	t.Parallel()

	var store *durablePolicyStoreStub
	if _, err := NewDurableInvocationAuthorizationPolicySource(store); err == nil {
		t.Fatal("typed-nil durable policy store was accepted")
	}
	source, err := NewDurableInvocationAuthorizationPolicySource(&durablePolicyStoreStub{})
	if err != nil {
		t.Fatalf("construct durable policy source: %v", err)
	}
	if _, err := source.ResolveInvocationAuthorizationPolicy(
		context.Background(),
		InvocationAuthorizationPolicyScope{},
	); !errors.Is(err, ErrInvocationAuthorizationPolicyUnavailable) {
		t.Fatalf("invalid scope error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.ResolveInvocationAuthorizationPolicy(
		ctx,
		InvocationAuthorizationPolicyScope{
			IsolationDomainID: "iso_1",
			ServiceID:         "svc_1",
			RevisionID:        "rev_1",
		},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lookup error = %v", err)
	}
}

func durablePolicyFixture(
	t *testing.T,
) (InvocationAuthorizationPolicy, persistence.InvocationAuthorizationPolicyRecord) {
	t.Helper()
	policy, err := NewInvocationAuthorizationPolicy(
		InvocationAuthorizationPolicyScope{
			IsolationDomainID: "iso_1",
			ServiceID:         "svc_1",
			RevisionID:        "rev_1",
		},
		"policy_1",
		CanonicalInvocationCedarSchema(),
		[]byte("permit(principal, action, resource);"),
	)
	if err != nil {
		t.Fatalf("construct policy fixture: %v", err)
	}
	return policy, persistence.InvocationAuthorizationPolicyRecord{
		Contract:                  policy.Contract,
		IsolationDomainID:         policy.IsolationDomainID,
		ServiceID:                 policy.ServiceID,
		RevisionID:                policy.RevisionID,
		PolicySetID:               policy.PolicySetID,
		PolicyDigest:              append([]byte(nil), policy.Digest[:]...),
		Schema:                    append([]byte(nil), policy.Schema...),
		Policies:                  append([]byte(nil), policy.Policies...),
		InstalledBy:               "usr_1",
		InstallationCorrelationID: "cor_1",
		ReasonDigest:              make([]byte, 32),
	}
}

type durablePolicyStoreStub struct {
	record persistence.InvocationAuthorizationPolicyRecord
	err    error
}

func (store *durablePolicyStoreStub) GetInvocationAuthorizationPolicy(
	context.Context,
	string,
	string,
	string,
) (persistence.InvocationAuthorizationPolicyRecord, error) {
	return store.record, store.err
}

var _ DurableInvocationAuthorizationPolicyStore = (*durablePolicyStoreStub)(nil)
