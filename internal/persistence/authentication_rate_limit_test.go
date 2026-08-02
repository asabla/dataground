package persistence

import (
	"crypto/sha256"
	"testing"
	"time"
)

func TestAuthenticationRateLimitPolicyRejectsUnsafeBounds(t *testing.T) {
	t.Parallel()

	valid := AuthenticationRateLimitPolicy{
		Window:               time.Minute,
		GlobalBurst:          100,
		IsolationDomainBurst: 20,
		CredentialBurst:      5,
	}
	if !valid.Valid() {
		t.Fatal("valid policy was rejected")
	}
	for name, mutate := range map[string]func(*AuthenticationRateLimitPolicy){
		"short window": func(policy *AuthenticationRateLimitPolicy) { policy.Window = time.Second - 1 },
		"long window":  func(policy *AuthenticationRateLimitPolicy) { policy.Window = 24*time.Hour + 1 },
		"zero global":  func(policy *AuthenticationRateLimitPolicy) { policy.GlobalBurst = 0 },
		"excessive global": func(policy *AuthenticationRateLimitPolicy) {
			policy.GlobalBurst = maximumAuthenticationRateLimitBurst + 1
		},
		"domain over global": func(policy *AuthenticationRateLimitPolicy) { policy.IsolationDomainBurst = policy.GlobalBurst + 1 },
		"credential over domain": func(policy *AuthenticationRateLimitPolicy) {
			policy.CredentialBurst = policy.IsolationDomainBurst + 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := valid
			mutate(&candidate)
			if candidate.Valid() {
				t.Fatal("invalid policy was accepted")
			}
		})
	}
}

func TestAuthenticationRateLimitPolicyActivationValidatesAttribution(t *testing.T) {
	t.Parallel()

	reasonDigest := sha256.Sum256([]byte("reviewed policy"))
	valid := AuthenticationRateLimitPolicyActivation{
		Contract:   authenticationRateLimitPolicyContract,
		Generation: 1,
		Policy: AuthenticationRateLimitPolicy{
			Window: time.Minute, GlobalBurst: 100, IsolationDomainBurst: 20, CredentialBurst: 5,
		},
		ActivatedBy:   "usr_0123456789abcdefghij",
		CorrelationID: "cor_0123456789abcdefghij",
		ReasonDigest:  append([]byte(nil), reasonDigest[:]...),
	}
	if !valid.Valid() {
		t.Fatal("valid activation was rejected")
	}
	for name, mutate := range map[string]func(*AuthenticationRateLimitPolicyActivation){
		"contract":    func(value *AuthenticationRateLimitPolicyActivation) { value.Contract = "other" },
		"generation":  func(value *AuthenticationRateLimitPolicyActivation) { value.Generation = 0 },
		"actor":       func(value *AuthenticationRateLimitPolicyActivation) { value.ActivatedBy = "" },
		"correlation": func(value *AuthenticationRateLimitPolicyActivation) { value.CorrelationID = "" },
		"reason":      func(value *AuthenticationRateLimitPolicyActivation) { value.ReasonDigest = nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := valid
			mutate(&candidate)
			if candidate.Valid() {
				t.Fatal("invalid activation was accepted")
			}
		})
	}
}

func TestAuthenticationRateLimitBucketEnforcesBurstAndRefill(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	arrival := now
	first := advanceAuthenticationRateLimitBucket(now, arrival, time.Minute, 2)
	if !first.result.Allowed || !first.nextArrival.Equal(now.Add(30*time.Second)) {
		t.Fatalf("first admission = %#v", first)
	}
	second := advanceAuthenticationRateLimitBucket(now, first.nextArrival, time.Minute, 2)
	if !second.result.Allowed || !second.nextArrival.Equal(now.Add(time.Minute)) {
		t.Fatalf("second admission = %#v", second)
	}
	denied := advanceAuthenticationRateLimitBucket(now, second.nextArrival, time.Minute, 2)
	if denied.result.Allowed || denied.result.RetryAfter != 30*time.Second ||
		!denied.nextArrival.Equal(second.nextArrival) {
		t.Fatalf("burst denial = %#v", denied)
	}
	refilled := advanceAuthenticationRateLimitBucket(now.Add(30*time.Second), denied.nextArrival, time.Minute, 2)
	if !refilled.result.Allowed {
		t.Fatalf("refilled admission = %#v", refilled)
	}
}

func TestAuthenticationRateLimitDigestsSeparateScopesAndCredentials(t *testing.T) {
	t.Parallel()

	domain := []byte("iso_0123456789abcdefghij")
	credential := sha256.Sum256([]byte("credential"))
	domainDigest := authenticationRateLimitDigest("domain", domain)
	credentialDigest := authenticationRateLimitDigest("credential", append(append([]byte(nil), domain...), credential[:]...))
	if domainDigest == credentialDigest || domainDigest == credential || credentialDigest == credential ||
		domainDigest == authenticationRateLimitGlobalDigest || credentialDigest == authenticationRateLimitGlobalDigest {
		t.Fatal("rate limit subjects were not domain-separated")
	}
}

func TestAuthenticationRateLimitPolicyDigestBindsEveryField(t *testing.T) {
	t.Parallel()

	policy := AuthenticationRateLimitPolicy{
		Window: time.Minute, GlobalBurst: 100, IsolationDomainBurst: 20, CredentialBurst: 5,
	}
	want := policy.digest()
	mutations := []func(*AuthenticationRateLimitPolicy){
		func(candidate *AuthenticationRateLimitPolicy) { candidate.Window++ },
		func(candidate *AuthenticationRateLimitPolicy) { candidate.GlobalBurst++ },
		func(candidate *AuthenticationRateLimitPolicy) { candidate.IsolationDomainBurst++ },
		func(candidate *AuthenticationRateLimitPolicy) { candidate.CredentialBurst++ },
	}
	for _, mutate := range mutations {
		candidate := policy
		mutate(&candidate)
		if candidate.digest() == want {
			t.Fatal("policy mutation retained the active digest")
		}
	}
}
