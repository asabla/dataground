package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	authenticationRateLimitCapacityRequestContract  = "dataground.authentication-rate-limit-capacity/v3"
	authenticationRateLimitCapacityEvidenceContract = "dataground.authentication-rate-limit-capacity-evidence/v3"
	maximumAuthenticationRateLimitCapacityAttempts  = 100_000
	maximumAuthenticationRateLimitCapacityWorkers   = 256
	maximumAuthenticationRateLimitCapacityDuration  = 30 * time.Minute
)

var (
	ErrAuthenticationRateLimitCapacityInvalid = errors.New("authentication rate limit capacity probe is invalid")
	authenticationRateLimitCapacityRunPattern = regexp.MustCompile(`^cap_[0-9a-z]{20,32}$`)
	authenticationRateLimitDatabasePattern    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_$]{0,62}$`)
	authenticationRateLimitRevisionPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// AuthenticationRateLimitCapacityConfig describes one bounded measurement of
// the exact PostgreSQL admission path. The database must be dedicated to this
// run because the probe activates three generations and consumes their buckets.
type AuthenticationRateLimitCapacityConfig struct {
	Contract          string
	RunID             string
	SourceRevision    string
	DeploymentProfile string
	DatabaseName      string
	Policy            AuthenticationRateLimitPolicy
	AttemptsPerPhase  uint32
	Workers           uint32
	MaximumP99Latency time.Duration
	MinimumThroughput uint32
	DPoPNonce         AuthenticationRateLimitCapacityDPoPNonceConfig
}

func (config AuthenticationRateLimitCapacityConfig) Valid() bool {
	return config.Contract == authenticationRateLimitCapacityRequestContract &&
		authenticationRateLimitCapacityRunPattern.MatchString(config.RunID) &&
		authenticationRateLimitRevisionPattern.MatchString(config.SourceRevision) &&
		validAuthenticationRateLimitDeploymentProfile(config.DeploymentProfile) &&
		validAuthenticationRateLimitCapacityDatabaseName(config.DatabaseName) &&
		config.Policy.Valid() &&
		config.AttemptsPerPhase > config.Policy.GlobalBurst &&
		config.AttemptsPerPhase <= maximumAuthenticationRateLimitCapacityAttempts &&
		config.Workers > 0 && config.Workers <= maximumAuthenticationRateLimitCapacityWorkers &&
		config.Workers <= config.AttemptsPerPhase &&
		config.MaximumP99Latency > 0 && config.MaximumP99Latency <= time.Minute &&
		config.MinimumThroughput > 0 && config.DPoPNonce.Valid()
}

// AuthenticationRateLimitCapacityDPoPNonceConfig makes nonce capacity an
// explicit part of the measured authentication profile. Disabled profiles
// carry no latent sizing values; enabled profiles bind the exact serving
// lifetime, overlap, load shape, and acceptance thresholds.
type AuthenticationRateLimitCapacityDPoPNonceConfig struct {
	Enabled             bool
	Lifetime            time.Duration
	MaximumActivePerKey uint32
	AttemptsPerPhase    uint32
	Workers             uint32
	MaximumP99Latency   time.Duration
	MinimumThroughput   uint32
}

func (config AuthenticationRateLimitCapacityDPoPNonceConfig) Valid() bool {
	if !config.Enabled {
		return config.Lifetime == 0 && config.MaximumActivePerKey == 0 &&
			config.AttemptsPerPhase == 0 && config.Workers == 0 &&
			config.MaximumP99Latency == 0 && config.MinimumThroughput == 0
	}
	return authn.ValidDPoPNoncePolicyParameters(config.Lifetime, config.MaximumActivePerKey) &&
		config.AttemptsPerPhase > config.MaximumActivePerKey &&
		config.AttemptsPerPhase <= maximumAuthenticationRateLimitCapacityAttempts &&
		config.Workers > 0 && config.Workers <= maximumAuthenticationRateLimitCapacityWorkers &&
		config.Workers <= config.AttemptsPerPhase &&
		config.MaximumP99Latency > 0 && config.MaximumP99Latency <= time.Minute &&
		config.MinimumThroughput > 0
}

func validAuthenticationRateLimitDeploymentProfile(profile string) bool {
	return profile == "developer" || profile == "team" || profile == "production"
}

func validAuthenticationRateLimitCapacityDatabaseName(name string) bool {
	return authenticationRateLimitDatabasePattern.MatchString(name) &&
		(strings.Contains(name, "capacity") || strings.HasSuffix(name, "_test"))
}

type AuthenticationRateLimitCapacityPolicy struct {
	WindowNanoseconds        int64  `json:"windowNanoseconds"`
	GlobalBurst              uint32 `json:"globalBurst"`
	IsolationDomainBurst     uint32 `json:"isolationDomainBurst"`
	CredentialBurst          uint32 `json:"credentialBurst"`
	CanonicalPolicyDigestHex string `json:"canonicalPolicyDigestHex"`
}

type AuthenticationRateLimitCapacityPhase struct {
	Name                      string `json:"name"`
	Generation                uint64 `json:"generation"`
	Attempts                  uint32 `json:"attempts"`
	Workers                   uint32 `json:"workers"`
	Allowed                   uint32 `json:"allowed"`
	Denied                    uint32 `json:"denied"`
	DurationNanoseconds       int64  `json:"durationNanoseconds"`
	P50LatencyNanoseconds     int64  `json:"p50LatencyNanoseconds"`
	P95LatencyNanoseconds     int64  `json:"p95LatencyNanoseconds"`
	P99LatencyNanoseconds     int64  `json:"p99LatencyNanoseconds"`
	MaximumLatencyNanoseconds int64  `json:"maximumLatencyNanoseconds"`
	CompletedPerSecondMilli   uint64 `json:"completedPerSecondMilli"`
	P99LatencyAccepted        bool   `json:"p99LatencyAccepted"`
	MinimumThroughputAccepted bool   `json:"minimumThroughputAccepted"`
}

type AuthenticationRateLimitCapacityDPoPNoncePolicy struct {
	Enabled                      bool   `json:"enabled"`
	LifetimeNanoseconds          int64  `json:"lifetimeNanoseconds"`
	MaximumActivePerKey          uint32 `json:"maximumActivePerKey"`
	AttemptsPerPhase             uint32 `json:"attemptsPerPhase"`
	Workers                      uint32 `json:"workers"`
	MaximumP99LatencyNanoseconds int64  `json:"maximumP99LatencyNanoseconds"`
	MinimumThroughputPerSecond   uint32 `json:"minimumThroughputPerSecond"`
	enabledSet                   bool
}

func (policy *AuthenticationRateLimitCapacityDPoPNoncePolicy) UnmarshalJSON(encoded []byte) error {
	if policy == nil || bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return errors.New("DPoP nonce capacity policy must be an object")
	}
	var decoded struct {
		Enabled                      *bool  `json:"enabled"`
		LifetimeNanoseconds          int64  `json:"lifetimeNanoseconds"`
		MaximumActivePerKey          uint32 `json:"maximumActivePerKey"`
		AttemptsPerPhase             uint32 `json:"attemptsPerPhase"`
		Workers                      uint32 `json:"workers"`
		MaximumP99LatencyNanoseconds int64  `json:"maximumP99LatencyNanoseconds"`
		MinimumThroughputPerSecond   uint32 `json:"minimumThroughputPerSecond"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil || decoded.Enabled == nil {
		return errors.New("DPoP nonce capacity policy is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("DPoP nonce capacity policy has trailing data")
	}
	*policy = AuthenticationRateLimitCapacityDPoPNoncePolicy{
		Enabled:                      *decoded.Enabled,
		LifetimeNanoseconds:          decoded.LifetimeNanoseconds,
		MaximumActivePerKey:          decoded.MaximumActivePerKey,
		AttemptsPerPhase:             decoded.AttemptsPerPhase,
		Workers:                      decoded.Workers,
		MaximumP99LatencyNanoseconds: decoded.MaximumP99LatencyNanoseconds,
		MinimumThroughputPerSecond:   decoded.MinimumThroughputPerSecond,
		enabledSet:                   true,
	}
	return nil
}

func (policy AuthenticationRateLimitCapacityDPoPNoncePolicy) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Enabled                      bool   `json:"enabled"`
		LifetimeNanoseconds          int64  `json:"lifetimeNanoseconds"`
		MaximumActivePerKey          uint32 `json:"maximumActivePerKey"`
		AttemptsPerPhase             uint32 `json:"attemptsPerPhase"`
		Workers                      uint32 `json:"workers"`
		MaximumP99LatencyNanoseconds int64  `json:"maximumP99LatencyNanoseconds"`
		MinimumThroughputPerSecond   uint32 `json:"minimumThroughputPerSecond"`
	}{
		Enabled:                      policy.Enabled,
		LifetimeNanoseconds:          policy.LifetimeNanoseconds,
		MaximumActivePerKey:          policy.MaximumActivePerKey,
		AttemptsPerPhase:             policy.AttemptsPerPhase,
		Workers:                      policy.Workers,
		MaximumP99LatencyNanoseconds: policy.MaximumP99LatencyNanoseconds,
		MinimumThroughputPerSecond:   policy.MinimumThroughputPerSecond,
	})
}

type AuthenticationRateLimitCapacityDPoPNoncePhase struct {
	Name                      string `json:"name"`
	Attempts                  uint32 `json:"attempts"`
	Workers                   uint32 `json:"workers"`
	Challenges                uint32 `json:"challenges"`
	Validated                 uint32 `json:"validated"`
	ActiveRows                uint32 `json:"activeRows"`
	DurationNanoseconds       int64  `json:"durationNanoseconds"`
	P50LatencyNanoseconds     int64  `json:"p50LatencyNanoseconds"`
	P95LatencyNanoseconds     int64  `json:"p95LatencyNanoseconds"`
	P99LatencyNanoseconds     int64  `json:"p99LatencyNanoseconds"`
	MaximumLatencyNanoseconds int64  `json:"maximumLatencyNanoseconds"`
	CompletedPerSecondMilli   uint64 `json:"completedPerSecondMilli"`
	P99LatencyAccepted        bool   `json:"p99LatencyAccepted"`
	MinimumThroughputAccepted bool   `json:"minimumThroughputAccepted"`
	LifetimeAccepted          bool   `json:"lifetimeAccepted"`
	ActiveRowsAccepted        bool   `json:"activeRowsAccepted"`
}

type AuthenticationRateLimitCapacityEvidence struct {
	Contract                     string                                          `json:"contract"`
	RunID                        string                                          `json:"runId"`
	SourceRevision               string                                          `json:"sourceRevision"`
	DeploymentProfile            string                                          `json:"deploymentProfile"`
	DatabaseName                 string                                          `json:"databaseName"`
	GoVersion                    string                                          `json:"goVersion"`
	PostgreSQLServerVersion      int                                             `json:"postgresqlServerVersion"`
	PostgreSQLMaxConnections     int                                             `json:"postgresqlMaxConnections"`
	RecordedAt                   string                                          `json:"recordedAt"`
	Accepted                     bool                                            `json:"accepted"`
	Policy                       AuthenticationRateLimitCapacityPolicy           `json:"policy"`
	MaximumP99LatencyNanoseconds int64                                           `json:"maximumP99LatencyNanoseconds"`
	MinimumThroughputPerSecond   uint32                                          `json:"minimumThroughputPerSecond"`
	Phases                       []AuthenticationRateLimitCapacityPhase          `json:"phases"`
	DPoPNonce                    AuthenticationRateLimitCapacityDPoPNoncePolicy  `json:"dpopNonce"`
	DPoPNoncePhases              []AuthenticationRateLimitCapacityDPoPNoncePhase `json:"dpopNoncePhases"`
}

// AcceptedFor reports whether a capacity record is internally consistent and
// binds to the exact executable, deployment profile, and admission policy.
// Runtime database compatibility is checked separately through
// AuthenticationRateLimitCapacityReady so it remains part of readiness.
func (evidence AuthenticationRateLimitCapacityEvidence) AcceptedFor(
	sourceRevision string,
	deploymentProfile string,
	goVersion string,
	policy AuthenticationRateLimitPolicy,
	nonce AuthenticationRateLimitCapacityDPoPNonceConfig,
) bool {
	if evidence.Contract != authenticationRateLimitCapacityEvidenceContract ||
		!authenticationRateLimitCapacityRunPattern.MatchString(evidence.RunID) ||
		evidence.SourceRevision != sourceRevision ||
		!authenticationRateLimitRevisionPattern.MatchString(evidence.SourceRevision) ||
		evidence.DeploymentProfile != deploymentProfile ||
		!validAuthenticationRateLimitDeploymentProfile(evidence.DeploymentProfile) ||
		!validAuthenticationRateLimitCapacityDatabaseName(evidence.DatabaseName) ||
		evidence.GoVersion != goVersion || len(evidence.GoVersion) == 0 || len(evidence.GoVersion) > 64 ||
		evidence.PostgreSQLServerVersion <= 0 || evidence.PostgreSQLMaxConnections <= 0 ||
		evidence.MaximumP99LatencyNanoseconds <= 0 ||
		evidence.MaximumP99LatencyNanoseconds > time.Minute.Nanoseconds() ||
		evidence.MinimumThroughputPerSecond == 0 || !policy.Valid() ||
		!nonce.Valid() ||
		!evidence.capacityPolicyMatches(policy) || len(evidence.Phases) != 3 ||
		!evidence.capacityDPoPNonceMatches(nonce) {
		return false
	}
	recordedAt, err := time.Parse(time.RFC3339Nano, evidence.RecordedAt)
	if err != nil || recordedAt.Location() != time.UTC || recordedAt.After(time.Now().Add(time.Minute)) {
		return false
	}
	expectedNames := [...]string{"credential", "isolation-domain", "global"}
	expectedBursts := [...]uint32{policy.CredentialBurst, policy.IsolationDomainBurst, policy.GlobalBurst}
	accepted := true
	var expectedAttempts, expectedWorkers uint32
	for index, phase := range evidence.Phases {
		if index == 0 {
			expectedAttempts = phase.Attempts
			expectedWorkers = phase.Workers
		} else if phase.Attempts != expectedAttempts || phase.Workers != expectedWorkers {
			return false
		}
		if uint64(evidence.PostgreSQLMaxConnections) < uint64(phase.Workers) {
			return false
		}
		if !validAuthenticationRateLimitCapacityPhase(
			phase,
			expectedNames[index],
			uint64(index+1),
			expectedBursts[index],
			policy.Window,
			time.Duration(evidence.MaximumP99LatencyNanoseconds),
			evidence.MinimumThroughputPerSecond,
		) {
			return false
		}
		if !phase.P99LatencyAccepted || !phase.MinimumThroughputAccepted ||
			!authenticationRateLimitCapacityCountsAccepted(
				phase,
				expectedBursts[index],
				policy.Window,
			) {
			accepted = false
		}
	}
	if nonce.Enabled {
		if len(evidence.DPoPNoncePhases) != 3 {
			return false
		}
		expectedNames := [...]string{"nonce-issue-shared-key", "nonce-issue-distinct-keys", "nonce-validate"}
		for index, phase := range evidence.DPoPNoncePhases {
			if uint64(evidence.PostgreSQLMaxConnections) < uint64(phase.Workers) {
				return false
			}
			if !validAuthenticationRateLimitCapacityDPoPNoncePhase(
				phase,
				expectedNames[index],
				nonce,
			) {
				return false
			}
			if !phase.P99LatencyAccepted || !phase.MinimumThroughputAccepted ||
				!phase.LifetimeAccepted || !phase.ActiveRowsAccepted {
				accepted = false
			}
		}
	} else if evidence.DPoPNoncePhases == nil || len(evidence.DPoPNoncePhases) != 0 {
		return false
	}
	return evidence.Accepted && accepted
}

func (evidence AuthenticationRateLimitCapacityEvidence) capacityDPoPNonceMatches(
	config AuthenticationRateLimitCapacityDPoPNonceConfig,
) bool {
	nonce := evidence.DPoPNonce
	return nonce.enabledSet && nonce.Enabled == config.Enabled &&
		nonce.LifetimeNanoseconds == config.Lifetime.Nanoseconds() &&
		nonce.MaximumActivePerKey == config.MaximumActivePerKey &&
		nonce.AttemptsPerPhase == config.AttemptsPerPhase &&
		nonce.Workers == config.Workers &&
		nonce.MaximumP99LatencyNanoseconds == config.MaximumP99Latency.Nanoseconds() &&
		nonce.MinimumThroughputPerSecond == config.MinimumThroughput
}

// DPoPNonceConfig returns the exact nonce profile represented by this record.
// Callers still need AcceptedFor to prove that the complete record is
// internally consistent and matches their serving configuration.
func (evidence AuthenticationRateLimitCapacityEvidence) DPoPNonceConfig() AuthenticationRateLimitCapacityDPoPNonceConfig {
	return AuthenticationRateLimitCapacityDPoPNonceConfig{
		Enabled:             evidence.DPoPNonce.Enabled,
		Lifetime:            time.Duration(evidence.DPoPNonce.LifetimeNanoseconds),
		MaximumActivePerKey: evidence.DPoPNonce.MaximumActivePerKey,
		AttemptsPerPhase:    evidence.DPoPNonce.AttemptsPerPhase,
		Workers:             evidence.DPoPNonce.Workers,
		MaximumP99Latency:   time.Duration(evidence.DPoPNonce.MaximumP99LatencyNanoseconds),
		MinimumThroughput:   evidence.DPoPNonce.MinimumThroughputPerSecond,
	}
}

func validAuthenticationRateLimitCapacityDPoPNoncePhase(
	phase AuthenticationRateLimitCapacityDPoPNoncePhase,
	expectedName string,
	config AuthenticationRateLimitCapacityDPoPNonceConfig,
) bool {
	if phase.Name != expectedName || phase.Attempts != config.AttemptsPerPhase ||
		phase.Workers != config.Workers || phase.DurationNanoseconds <= 0 ||
		phase.DurationNanoseconds > maximumAuthenticationRateLimitCapacityDuration.Nanoseconds() ||
		phase.P50LatencyNanoseconds <= 0 ||
		phase.P50LatencyNanoseconds > phase.P95LatencyNanoseconds ||
		phase.P95LatencyNanoseconds > phase.P99LatencyNanoseconds ||
		phase.P99LatencyNanoseconds > phase.MaximumLatencyNanoseconds ||
		phase.MaximumLatencyNanoseconds > phase.DurationNanoseconds ||
		phase.CompletedPerSecondMilli != capacityCompletedPerSecondMilli(
			phase.Attempts,
			time.Duration(phase.DurationNanoseconds),
		) ||
		phase.P99LatencyAccepted !=
			(phase.P99LatencyNanoseconds <= config.MaximumP99Latency.Nanoseconds()) ||
		phase.MinimumThroughputAccepted !=
			(phase.CompletedPerSecondMilli >= uint64(config.MinimumThroughput)*1000) ||
		phase.LifetimeAccepted != (phase.DurationNanoseconds < config.Lifetime.Nanoseconds()) {
		return false
	}
	var expectedRows uint32
	switch expectedName {
	case "nonce-issue-shared-key":
		expectedRows = min(phase.Attempts, config.MaximumActivePerKey)
		if phase.Challenges != phase.Attempts || phase.Validated != 0 {
			return false
		}
	case "nonce-issue-distinct-keys":
		expectedRows = phase.Attempts
		if phase.Challenges != phase.Attempts || phase.Validated != 0 {
			return false
		}
	case "nonce-validate":
		expectedRows = config.Workers
		if phase.Challenges != 0 || phase.Validated != phase.Attempts {
			return false
		}
	default:
		return false
	}
	return phase.ActiveRows <= phase.Attempts &&
		phase.ActiveRowsAccepted == (phase.ActiveRows == expectedRows)
}

func (evidence AuthenticationRateLimitCapacityEvidence) capacityPolicyMatches(
	policy AuthenticationRateLimitPolicy,
) bool {
	digest, err := hex.DecodeString(evidence.Policy.CanonicalPolicyDigestHex)
	expected := policy.digest()
	return err == nil && len(digest) == sha256.Size &&
		subtle.ConstantTimeCompare(digest, expected[:]) == 1 &&
		evidence.Policy.WindowNanoseconds == policy.Window.Nanoseconds() &&
		evidence.Policy.GlobalBurst == policy.GlobalBurst &&
		evidence.Policy.IsolationDomainBurst == policy.IsolationDomainBurst &&
		evidence.Policy.CredentialBurst == policy.CredentialBurst
}

func validAuthenticationRateLimitCapacityPhase(
	phase AuthenticationRateLimitCapacityPhase,
	expectedName string,
	expectedGeneration uint64,
	expectedBurst uint32,
	window time.Duration,
	maximumP99 time.Duration,
	minimumThroughput uint32,
) bool {
	if phase.Name != expectedName || phase.Generation != expectedGeneration ||
		phase.Attempts == 0 || phase.Attempts > maximumAuthenticationRateLimitCapacityAttempts ||
		phase.Workers == 0 || phase.Workers > maximumAuthenticationRateLimitCapacityWorkers ||
		phase.Workers > phase.Attempts || phase.Attempts <= expectedBurst ||
		uint64(phase.Allowed)+uint64(phase.Denied) != uint64(phase.Attempts) ||
		phase.DurationNanoseconds <= 0 ||
		phase.DurationNanoseconds > maximumAuthenticationRateLimitCapacityDuration.Nanoseconds() ||
		!authenticationRateLimitCapacityCountsAccepted(phase, expectedBurst, window) ||
		phase.P50LatencyNanoseconds <= 0 ||
		phase.P50LatencyNanoseconds > phase.P95LatencyNanoseconds ||
		phase.P95LatencyNanoseconds > phase.P99LatencyNanoseconds ||
		phase.P99LatencyNanoseconds > phase.MaximumLatencyNanoseconds ||
		phase.MaximumLatencyNanoseconds > phase.DurationNanoseconds ||
		phase.CompletedPerSecondMilli != capacityCompletedPerSecondMilli(
			phase.Attempts,
			time.Duration(phase.DurationNanoseconds),
		) {
		return false
	}
	return phase.P99LatencyAccepted == (phase.P99LatencyNanoseconds <= maximumP99.Nanoseconds()) &&
		phase.MinimumThroughputAccepted ==
			(phase.CompletedPerSecondMilli >= uint64(minimumThroughput)*1000)
}

func authenticationRateLimitCapacityCountsAccepted(
	phase AuthenticationRateLimitCapacityPhase,
	burst uint32,
	window time.Duration,
) bool {
	if burst == 0 || window <= 0 || phase.DurationNanoseconds <= 0 ||
		phase.DurationNanoseconds > maximumAuthenticationRateLimitCapacityDuration.Nanoseconds() ||
		phase.Denied == 0 ||
		phase.Allowed < burst {
		return false
	}
	interval := window / time.Duration(burst)
	if interval <= 0 {
		return false
	}
	refills := uint64(time.Duration(phase.DurationNanoseconds) / interval)
	maximumAllowed := uint64(burst) + refills
	if maximumAllowed > uint64(phase.Attempts) {
		maximumAllowed = uint64(phase.Attempts)
	}
	return uint64(phase.Allowed) <= maximumAllowed
}

type authenticationRateLimitCapacityAttempt struct {
	allowed bool
	latency time.Duration
	err     error
}

type authenticationRateLimitCapacityRequestFactory func(int) (string, [sha256.Size]byte)

func OpenAuthenticationRateLimitCapacityPool(
	ctx context.Context,
	databaseURL string,
	workers uint32,
) (*pgxpool.Pool, error) {
	if ctx == nil || databaseURL == "" || workers == 0 || workers > maximumAuthenticationRateLimitCapacityWorkers {
		return nil, ErrAuthenticationRateLimitCapacityInvalid
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse authentication rate limit capacity database URL: %w", err)
	}
	config.MaxConns = int32(workers)
	config.MinConns = 0
	config.ConnConfig.RuntimeParams["application_name"] = "dataground-auth-capacity-probe"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create authentication rate limit capacity database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to authentication rate limit capacity database pool: %w", err)
	}
	return pool, nil
}

// MeasureAuthenticationRateLimitCapacity runs credential-, domain-, and
// global-contention phases against fresh generations in a dedicated database.
// Evidence is returned only when every attempt completes. Its Accepted field
// reports whether every operator-supplied latency and throughput threshold and
// the expected burst/refill denial semantics were met; dependency errors and
// cancellation return no partial record.
func (repository *Repository) MeasureAuthenticationRateLimitCapacity(
	ctx context.Context,
	config AuthenticationRateLimitCapacityConfig,
) (AuthenticationRateLimitCapacityEvidence, error) {
	var evidence AuthenticationRateLimitCapacityEvidence
	if repository == nil || repository.pool == nil || ctx == nil || !config.Valid() {
		return evidence, ErrAuthenticationRateLimitCapacityInvalid
	}
	if err := ctx.Err(); err != nil {
		return evidence, err
	}
	serverVersion, maximumConnections, err := repository.requireFreshAuthenticationRateLimitCapacityDatabase(ctx, config.DatabaseName)
	if err != nil {
		return evidence, err
	}

	evidence = AuthenticationRateLimitCapacityEvidence{
		Contract:                     authenticationRateLimitCapacityEvidenceContract,
		RunID:                        config.RunID,
		SourceRevision:               config.SourceRevision,
		DeploymentProfile:            config.DeploymentProfile,
		DatabaseName:                 config.DatabaseName,
		GoVersion:                    runtime.Version(),
		PostgreSQLServerVersion:      serverVersion,
		PostgreSQLMaxConnections:     maximumConnections,
		Policy:                       authenticationRateLimitCapacityPolicy(config.Policy),
		MaximumP99LatencyNanoseconds: config.MaximumP99Latency.Nanoseconds(),
		MinimumThroughputPerSecond:   config.MinimumThroughput,
		Accepted:                     true,
		Phases:                       make([]AuthenticationRateLimitCapacityPhase, 0, 3),
		DPoPNonce:                    authenticationRateLimitCapacityDPoPNoncePolicy(config.DPoPNonce),
		DPoPNoncePhases:              make([]AuthenticationRateLimitCapacityDPoPNoncePhase, 0, 3),
	}

	phaseFactories := []struct {
		name    string
		factory authenticationRateLimitCapacityRequestFactory
	}{
		{name: "credential", factory: capacityCredentialFactory(config.RunID)},
		{name: "isolation-domain", factory: capacityDomainFactory(config.RunID)},
		{name: "global", factory: capacityGlobalFactory(config.RunID)},
	}
	for index, definition := range phaseFactories {
		generation := uint64(index + 1)
		if err := repository.activateAuthenticationRateLimitCapacityGeneration(ctx, config, generation, definition.name); err != nil {
			return AuthenticationRateLimitCapacityEvidence{}, err
		}
		phase, err := repository.measureAuthenticationRateLimitCapacityPhase(
			ctx,
			config,
			generation,
			definition.name,
			definition.factory,
		)
		if err != nil {
			return AuthenticationRateLimitCapacityEvidence{}, err
		}
		evidence.Phases = append(evidence.Phases, phase)
		expectedBurst := []uint32{
			config.Policy.CredentialBurst,
			config.Policy.IsolationDomainBurst,
			config.Policy.GlobalBurst,
		}[index]
		if !phase.P99LatencyAccepted || !phase.MinimumThroughputAccepted ||
			!authenticationRateLimitCapacityCountsAccepted(
				phase,
				expectedBurst,
				config.Policy.Window,
			) {
			evidence.Accepted = false
		}
	}
	if config.DPoPNonce.Enabled {
		noncePhases, accepted, err := repository.measureAuthenticationRateLimitCapacityDPoPNonce(
			ctx,
			config.RunID,
			config.DPoPNonce,
		)
		if err != nil {
			return AuthenticationRateLimitCapacityEvidence{}, err
		}
		evidence.DPoPNoncePhases = noncePhases
		if !accepted {
			evidence.Accepted = false
		}
	}
	evidence.RecordedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return evidence, nil
}

// AuthenticationRateLimitCapacityReady verifies that the serving PostgreSQL
// profile still matches the exact server version and connection ceiling used
// by the accepted capacity run.
func (repository *Repository) AuthenticationRateLimitCapacityReady(
	ctx context.Context,
	evidence AuthenticationRateLimitCapacityEvidence,
) error {
	if repository == nil || repository.pool == nil || ctx == nil ||
		evidence.PostgreSQLServerVersion <= 0 || evidence.PostgreSQLMaxConnections <= 0 {
		return ErrAuthenticationRateLimitCapacityInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var serverVersionRaw, maximumConnectionsRaw string
	if err := repository.pool.QueryRow(ctx, `
		SELECT current_setting('server_version_num'), current_setting('max_connections')
	`).Scan(&serverVersionRaw, &maximumConnectionsRaw); err != nil {
		return fmt.Errorf("inspect authentication rate limit serving database profile: %w", err)
	}
	serverVersion, serverErr := strconv.Atoi(serverVersionRaw)
	maximumConnections, connectionsErr := strconv.Atoi(maximumConnectionsRaw)
	if serverErr != nil || connectionsErr != nil ||
		serverVersion != evidence.PostgreSQLServerVersion ||
		maximumConnections != evidence.PostgreSQLMaxConnections {
		return ErrAuthenticationRateLimitCapacityInvalid
	}
	return nil
}

func (repository *Repository) requireFreshAuthenticationRateLimitCapacityDatabase(
	ctx context.Context,
	expectedName string,
) (int, int, error) {
	var (
		actualName        string
		serverVersionRaw  string
		maxConnectionsRaw string
		activations       int
		buckets           int
		nonces            int
	)
	err := repository.pool.QueryRow(ctx, `
		SELECT
			current_database(),
			current_setting('server_version_num'),
			current_setting('max_connections'),
			(SELECT count(*) FROM authentication_rate_limit_policy_activations),
			(SELECT count(*) FROM authentication_rate_limit_buckets),
			(SELECT count(*) FROM oidc_dpop_nonces)
	`).Scan(&actualName, &serverVersionRaw, &maxConnectionsRaw, &activations, &buckets, &nonces)
	if err != nil {
		return 0, 0, fmt.Errorf("inspect authentication rate limit capacity database: %w", err)
	}
	serverVersion, err := strconv.Atoi(serverVersionRaw)
	maximumConnections, connectionsErr := strconv.Atoi(maxConnectionsRaw)
	if err != nil || connectionsErr != nil || serverVersion <= 0 || maximumConnections <= 0 ||
		actualName != expectedName || activations != 0 || buckets != 0 || nonces != 0 {
		return 0, 0, ErrAuthenticationRateLimitCapacityInvalid
	}
	return serverVersion, maximumConnections, nil
}

func authenticationRateLimitCapacityDPoPNoncePolicy(
	config AuthenticationRateLimitCapacityDPoPNonceConfig,
) AuthenticationRateLimitCapacityDPoPNoncePolicy {
	return AuthenticationRateLimitCapacityDPoPNoncePolicy{
		Enabled:                      config.Enabled,
		LifetimeNanoseconds:          config.Lifetime.Nanoseconds(),
		MaximumActivePerKey:          config.MaximumActivePerKey,
		AttemptsPerPhase:             config.AttemptsPerPhase,
		Workers:                      config.Workers,
		MaximumP99LatencyNanoseconds: config.MaximumP99Latency.Nanoseconds(),
		MinimumThroughputPerSecond:   config.MinimumThroughput,
		enabledSet:                   true,
	}
}

func (repository *Repository) activateAuthenticationRateLimitCapacityGeneration(
	ctx context.Context,
	config AuthenticationRateLimitCapacityConfig,
	generation uint64,
	phase string,
) error {
	reasonDigest := sha256.Sum256([]byte("dataground authentication admission capacity probe\x00" + config.RunID + "\x00" + phase))
	correlationDigest := sha256.Sum256([]byte(config.RunID + "\x00" + phase))
	return repository.ActivateAuthenticationRateLimitPolicy(ctx, AuthenticationRateLimitPolicyActivation{
		Contract:      authenticationRateLimitPolicyContract,
		Generation:    generation,
		Policy:        config.Policy,
		ActivatedBy:   "capacity-probe",
		CorrelationID: "cor_" + hex.EncodeToString(correlationDigest[:10]),
		ReasonDigest:  append([]byte(nil), reasonDigest[:]...),
	})
}

func (repository *Repository) measureAuthenticationRateLimitCapacityPhase(
	ctx context.Context,
	config AuthenticationRateLimitCapacityConfig,
	generation uint64,
	name string,
	factory authenticationRateLimitCapacityRequestFactory,
) (AuthenticationRateLimitCapacityPhase, error) {
	phase := AuthenticationRateLimitCapacityPhase{
		Name: name, Generation: generation, Attempts: config.AttemptsPerPhase, Workers: config.Workers,
	}
	phaseCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	results := make(chan authenticationRateLimitCapacityAttempt, config.Workers)
	var workers sync.WaitGroup
	for worker := uint32(0); worker < config.Workers; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				domain, credential := factory(index)
				started := time.Now()
				result, err := repository.AllowAuthentication(phaseCtx, domain, credential, generation, config.Policy)
				results <- authenticationRateLimitCapacityAttempt{
					allowed: result.Allowed,
					latency: time.Since(started),
					err:     err,
				}
			}
		}()
	}
	started := time.Now()
	go func() {
		defer close(jobs)
		for index := 0; index < int(config.AttemptsPerPhase); index++ {
			select {
			case jobs <- index:
			case <-phaseCtx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	latencies := make([]time.Duration, 0, config.AttemptsPerPhase)
	var firstError error
	for result := range results {
		if result.err != nil {
			if firstError == nil {
				firstError = result.err
				cancel()
			}
			continue
		}
		latencies = append(latencies, result.latency)
		if result.allowed {
			phase.Allowed++
		} else {
			phase.Denied++
		}
	}
	phaseDuration := time.Since(started)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return AuthenticationRateLimitCapacityPhase{}, ctxErr
	}
	if firstError != nil {
		return AuthenticationRateLimitCapacityPhase{}, fmt.Errorf("measure authentication admission capacity: %w", firstError)
	}
	if len(latencies) != int(config.AttemptsPerPhase) || phase.Allowed+phase.Denied != config.AttemptsPerPhase || phaseDuration <= 0 {
		return AuthenticationRateLimitCapacityPhase{}, ErrAuthenticationRateLimitCapacityInvalid
	}
	sort.Slice(latencies, func(left, right int) bool { return latencies[left] < latencies[right] })
	phase.DurationNanoseconds = phaseDuration.Nanoseconds()
	phase.P50LatencyNanoseconds = capacityLatencyPercentile(latencies, 50).Nanoseconds()
	phase.P95LatencyNanoseconds = capacityLatencyPercentile(latencies, 95).Nanoseconds()
	phase.P99LatencyNanoseconds = capacityLatencyPercentile(latencies, 99).Nanoseconds()
	phase.MaximumLatencyNanoseconds = latencies[len(latencies)-1].Nanoseconds()
	phase.CompletedPerSecondMilli = capacityCompletedPerSecondMilli(config.AttemptsPerPhase, phaseDuration)
	phase.P99LatencyAccepted = phase.P99LatencyNanoseconds <= config.MaximumP99Latency.Nanoseconds()
	phase.MinimumThroughputAccepted = phase.CompletedPerSecondMilli >= uint64(config.MinimumThroughput)*1000
	return phase, nil
}

func capacityLatencyPercentile(sorted []time.Duration, percentile int) time.Duration {
	if len(sorted) == 0 || percentile < 1 || percentile > 100 {
		return 0
	}
	index := (len(sorted)*percentile + 99) / 100
	return sorted[index-1]
}

func capacityCompletedPerSecondMilli(attempts uint32, duration time.Duration) uint64 {
	if attempts == 0 || duration <= 0 {
		return 0
	}
	numerator := uint64(attempts) * uint64(time.Second) * 1000
	return numerator / uint64(duration)
}

func authenticationRateLimitCapacityPolicy(policy AuthenticationRateLimitPolicy) AuthenticationRateLimitCapacityPolicy {
	digest := policy.digest()
	return AuthenticationRateLimitCapacityPolicy{
		WindowNanoseconds:        policy.Window.Nanoseconds(),
		GlobalBurst:              policy.GlobalBurst,
		IsolationDomainBurst:     policy.IsolationDomainBurst,
		CredentialBurst:          policy.CredentialBurst,
		CanonicalPolicyDigestHex: hex.EncodeToString(digest[:]),
	}
}

func capacityCredentialFactory(runID string) authenticationRateLimitCapacityRequestFactory {
	domain := identity.Derived("iso", runID+"\x00credential-domain")
	credential := sha256.Sum256([]byte(runID + "\x00credential"))
	return func(int) (string, [sha256.Size]byte) { return domain, credential }
}

func capacityDomainFactory(runID string) authenticationRateLimitCapacityRequestFactory {
	domain := identity.Derived("iso", runID+"\x00domain")
	return func(index int) (string, [sha256.Size]byte) {
		return domain, sha256.Sum256([]byte(fmt.Sprintf("%s\x00domain-credential\x00%d", runID, index)))
	}
}

func capacityGlobalFactory(runID string) authenticationRateLimitCapacityRequestFactory {
	return func(index int) (string, [sha256.Size]byte) {
		seed := fmt.Sprintf("%s\x00global\x00%d", runID, index)
		return identity.Derived("iso", seed), sha256.Sum256([]byte(seed + "\x00credential"))
	}
}
