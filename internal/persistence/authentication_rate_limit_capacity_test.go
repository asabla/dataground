package persistence

import (
	"testing"
	"time"
)

func TestAuthenticationRateLimitCapacityConfigValidatesBoundedProfile(t *testing.T) {
	t.Parallel()

	valid := AuthenticationRateLimitCapacityConfig{
		Contract:          authenticationRateLimitCapacityRequestContract,
		RunID:             "cap_0123456789abcdefghij",
		SourceRevision:    "0123456789abcdef0123456789abcdef01234567",
		DeploymentProfile: "team",
		DatabaseName:      "dataground_capacity",
		Policy: AuthenticationRateLimitPolicy{
			Window: time.Minute, GlobalBurst: 100, IsolationDomainBurst: 20, CredentialBurst: 10,
		},
		AttemptsPerPhase:  200,
		Workers:           10,
		MaximumP99Latency: time.Second,
		MinimumThroughput: 10,
	}
	if !valid.Valid() {
		t.Fatal("valid capacity configuration was rejected")
	}

	invalid := []AuthenticationRateLimitCapacityConfig{
		{},
		func() AuthenticationRateLimitCapacityConfig { value := valid; value.Contract = "unknown"; return value }(),
		func() AuthenticationRateLimitCapacityConfig { value := valid; value.RunID = "cap_short"; return value }(),
		func() AuthenticationRateLimitCapacityConfig {
			value := valid
			value.SourceRevision = "main"
			return value
		}(),
		func() AuthenticationRateLimitCapacityConfig {
			value := valid
			value.DeploymentProfile = "large"
			return value
		}(),
		func() AuthenticationRateLimitCapacityConfig {
			value := valid
			value.DatabaseName = "bad-name"
			return value
		}(),
		func() AuthenticationRateLimitCapacityConfig { value := valid; value.AttemptsPerPhase = 0; return value }(),
		func() AuthenticationRateLimitCapacityConfig {
			value := valid
			value.AttemptsPerPhase = value.Policy.GlobalBurst
			return value
		}(),
		func() AuthenticationRateLimitCapacityConfig { value := valid; value.Workers = 257; return value }(),
		func() AuthenticationRateLimitCapacityConfig {
			value := valid
			value.MaximumP99Latency = 0
			return value
		}(),
		func() AuthenticationRateLimitCapacityConfig {
			value := valid
			value.MinimumThroughput = 0
			return value
		}(),
	}
	for index, config := range invalid {
		if config.Valid() {
			t.Fatalf("invalid capacity configuration %d was accepted", index)
		}
	}
}

func TestAuthenticationRateLimitCapacityEvidenceRequiresExactAcceptedSemantics(t *testing.T) {
	t.Parallel()

	policy := AuthenticationRateLimitPolicy{
		Window: time.Minute, GlobalBurst: 100, IsolationDomainBurst: 20, CredentialBurst: 10,
	}
	evidence := validAuthenticationRateLimitCapacityEvidence(t, policy)
	if !evidence.AcceptedFor(evidence.SourceRevision, evidence.DeploymentProfile, evidence.GoVersion, policy) {
		t.Fatal("accepted capacity evidence was rejected")
	}
	mutations := []func(*AuthenticationRateLimitCapacityEvidence){
		func(value *AuthenticationRateLimitCapacityEvidence) { value.Accepted = false },
		func(value *AuthenticationRateLimitCapacityEvidence) { value.SourceRevision = "main" },
		func(value *AuthenticationRateLimitCapacityEvidence) { value.Policy.GlobalBurst++ },
		func(value *AuthenticationRateLimitCapacityEvidence) { value.Phases[0].Allowed++ },
		func(value *AuthenticationRateLimitCapacityEvidence) { value.Phases[1].P99LatencyAccepted = false },
		func(value *AuthenticationRateLimitCapacityEvidence) { value.Phases[1].Workers++ },
		func(value *AuthenticationRateLimitCapacityEvidence) {
			value.PostgreSQLMaxConnections = int(value.Phases[0].Workers) - 1
		},
		func(value *AuthenticationRateLimitCapacityEvidence) {
			value.Phases[2].DurationNanoseconds = policy.Window.Nanoseconds()
		},
	}
	for index, mutate := range mutations {
		candidate := evidence
		candidate.Phases = append([]AuthenticationRateLimitCapacityPhase(nil), evidence.Phases...)
		mutate(&candidate)
		if candidate.AcceptedFor(evidence.SourceRevision, evidence.DeploymentProfile, evidence.GoVersion, policy) {
			t.Fatalf("invalid capacity evidence mutation %d was accepted", index)
		}
	}
}

func validAuthenticationRateLimitCapacityEvidence(
	t *testing.T,
	policy AuthenticationRateLimitPolicy,
) AuthenticationRateLimitCapacityEvidence {
	t.Helper()
	bursts := []uint32{policy.CredentialBurst, policy.IsolationDomainBurst, policy.GlobalBurst}
	names := []string{"credential", "isolation-domain", "global"}
	phases := make([]AuthenticationRateLimitCapacityPhase, 0, len(names))
	for index, name := range names {
		phases = append(phases, AuthenticationRateLimitCapacityPhase{
			Name:                      name,
			Generation:                uint64(index + 1),
			Attempts:                  200,
			Workers:                   20,
			Allowed:                   bursts[index],
			Denied:                    200 - bursts[index],
			DurationNanoseconds:       (100 * time.Millisecond).Nanoseconds(),
			P50LatencyNanoseconds:     time.Millisecond.Nanoseconds(),
			P95LatencyNanoseconds:     (2 * time.Millisecond).Nanoseconds(),
			P99LatencyNanoseconds:     (3 * time.Millisecond).Nanoseconds(),
			MaximumLatencyNanoseconds: (4 * time.Millisecond).Nanoseconds(),
			CompletedPerSecondMilli:   2_000_000,
			P99LatencyAccepted:        true,
			MinimumThroughputAccepted: true,
		})
	}
	return AuthenticationRateLimitCapacityEvidence{
		Contract:                     authenticationRateLimitCapacityEvidenceContract,
		RunID:                        "cap_0123456789abcdefghij",
		SourceRevision:               "0123456789abcdef0123456789abcdef01234567",
		DeploymentProfile:            "team",
		DatabaseName:                 "dataground_capacity",
		GoVersion:                    "go1.26.5",
		PostgreSQLServerVersion:      180005,
		PostgreSQLMaxConnections:     100,
		RecordedAt:                   time.Now().UTC().Format(time.RFC3339Nano),
		Accepted:                     true,
		Policy:                       authenticationRateLimitCapacityPolicy(policy),
		MaximumP99LatencyNanoseconds: (100 * time.Millisecond).Nanoseconds(),
		MinimumThroughputPerSecond:   50,
		Phases:                       phases,
	}
}

func TestAuthenticationRateLimitCapacityStatisticsAreDeterministic(t *testing.T) {
	t.Parallel()

	latencies := []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond, 4 * time.Millisecond}
	if got := capacityLatencyPercentile(latencies, 50); got != 2*time.Millisecond {
		t.Fatalf("p50 = %s", got)
	}
	if got := capacityLatencyPercentile(latencies, 99); got != 4*time.Millisecond {
		t.Fatalf("p99 = %s", got)
	}
	if got := capacityCompletedPerSecondMilli(10, time.Second); got != 10_000 {
		t.Fatalf("completed per second milli = %d", got)
	}
	if got := capacityCompletedPerSecondMilli(0, time.Second); got != 0 {
		t.Fatalf("empty throughput = %d", got)
	}
}

func TestAuthenticationRateLimitCapacityFactoriesPreserveIntendedContention(t *testing.T) {
	t.Parallel()

	const runID = "cap_0123456789abcdefghij"
	credential := capacityCredentialFactory(runID)
	credentialDomainOne, credentialDigestOne := credential(1)
	credentialDomainTwo, credentialDigestTwo := credential(2)
	if credentialDomainOne != credentialDomainTwo || credentialDigestOne != credentialDigestTwo {
		t.Fatal("credential phase did not reuse one domain and credential")
	}

	domain := capacityDomainFactory(runID)
	domainOne, domainCredentialOne := domain(1)
	domainTwo, domainCredentialTwo := domain(2)
	if domainOne != domainTwo || domainCredentialOne == domainCredentialTwo {
		t.Fatal("domain phase did not reuse one domain with distinct credentials")
	}

	global := capacityGlobalFactory(runID)
	globalDomainOne, globalCredentialOne := global(1)
	globalDomainTwo, globalCredentialTwo := global(2)
	if globalDomainOne == globalDomainTwo || globalCredentialOne == globalCredentialTwo {
		t.Fatal("global phase did not use distinct domains and credentials")
	}
}
