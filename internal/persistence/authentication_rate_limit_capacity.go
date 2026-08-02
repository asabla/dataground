package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	authenticationRateLimitCapacityRequestContract  = "dataground.authentication-rate-limit-capacity/v1"
	authenticationRateLimitCapacityEvidenceContract = "dataground.authentication-rate-limit-capacity-evidence/v1"
	maximumAuthenticationRateLimitCapacityAttempts  = 100_000
	maximumAuthenticationRateLimitCapacityWorkers   = 256
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
}

func (config AuthenticationRateLimitCapacityConfig) Valid() bool {
	return config.Contract == authenticationRateLimitCapacityRequestContract &&
		authenticationRateLimitCapacityRunPattern.MatchString(config.RunID) &&
		authenticationRateLimitRevisionPattern.MatchString(config.SourceRevision) &&
		validAuthenticationRateLimitDeploymentProfile(config.DeploymentProfile) &&
		validAuthenticationRateLimitCapacityDatabaseName(config.DatabaseName) &&
		config.Policy.Valid() &&
		config.AttemptsPerPhase > 0 &&
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

type AuthenticationRateLimitCapacityEvidence struct {
	Contract                     string                                 `json:"contract"`
	RunID                        string                                 `json:"runId"`
	SourceRevision               string                                 `json:"sourceRevision"`
	DeploymentProfile            string                                 `json:"deploymentProfile"`
	DatabaseName                 string                                 `json:"databaseName"`
	GoVersion                    string                                 `json:"goVersion"`
	PostgreSQLServerVersion      int                                    `json:"postgresqlServerVersion"`
	PostgreSQLMaxConnections     int                                    `json:"postgresqlMaxConnections"`
	RecordedAt                   string                                 `json:"recordedAt"`
	Accepted                     bool                                   `json:"accepted"`
	Policy                       AuthenticationRateLimitCapacityPolicy  `json:"policy"`
	MaximumP99LatencyNanoseconds int64                                  `json:"maximumP99LatencyNanoseconds"`
	MinimumThroughputPerSecond   uint32                                 `json:"minimumThroughputPerSecond"`
	Phases                       []AuthenticationRateLimitCapacityPhase `json:"phases"`
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
// reports whether every operator-supplied latency and throughput threshold was
// met; dependency errors and cancellation return no partial record.
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
		if !phase.P99LatencyAccepted || !phase.MinimumThroughputAccepted {
			evidence.Accepted = false
		}
	}
	evidence.RecordedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return evidence, nil
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
	)
	err := repository.pool.QueryRow(ctx, `
		SELECT
			current_database(),
			current_setting('server_version_num'),
			current_setting('max_connections'),
			(SELECT count(*) FROM authentication_rate_limit_policy_activations),
			(SELECT count(*) FROM authentication_rate_limit_buckets)
	`).Scan(&actualName, &serverVersionRaw, &maxConnectionsRaw, &activations, &buckets)
	if err != nil {
		return 0, 0, fmt.Errorf("inspect authentication rate limit capacity database: %w", err)
	}
	serverVersion, err := strconv.Atoi(serverVersionRaw)
	maximumConnections, connectionsErr := strconv.Atoi(maxConnectionsRaw)
	if err != nil || connectionsErr != nil || serverVersion <= 0 || maximumConnections <= 0 ||
		actualName != expectedName || activations != 0 || buckets != 0 {
		return 0, 0, ErrAuthenticationRateLimitCapacityInvalid
	}
	return serverVersion, maximumConnections, nil
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
