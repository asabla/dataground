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
		AttemptsPerPhase:  100,
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
		func() AuthenticationRateLimitCapacityConfig { value := valid; value.Workers = 101; return value }(),
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
