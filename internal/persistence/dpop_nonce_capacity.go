package persistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/identity"
)

type authenticationRateLimitCapacityDPoPNonceAttempt struct {
	challenged bool
	validated  bool
	latency    time.Duration
	err        error
}

type authenticationRateLimitCapacityDPoPNonceRequestFactory func(int) authn.DPoPNonceRequest

func (repository *Repository) measureAuthenticationRateLimitCapacityDPoPNonce(
	ctx context.Context,
	runID string,
	config AuthenticationRateLimitCapacityDPoPNonceConfig,
) ([]AuthenticationRateLimitCapacityDPoPNoncePhase, bool, error) {
	if repository == nil || repository.pool == nil || ctx == nil || runID == "" || !config.Valid() || !config.Enabled {
		return nil, false, ErrAuthenticationRateLimitCapacityInvalid
	}
	type definition struct {
		name    string
		prepare func(context.Context) (authenticationRateLimitCapacityDPoPNonceRequestFactory, string, error)
	}
	definitions := []definition{
		{
			name: "nonce-issue-shared-key",
			prepare: func(context.Context) (authenticationRateLimitCapacityDPoPNonceRequestFactory, string, error) {
				return capacityDPoPNonceSharedKeyFactory(runID, config)
			},
		},
		{
			name: "nonce-issue-distinct-keys",
			prepare: func(context.Context) (authenticationRateLimitCapacityDPoPNonceRequestFactory, string, error) {
				return capacityDPoPNonceDistinctKeyFactory(runID, config)
			},
		},
		{
			name: "nonce-validate",
			prepare: func(ctx context.Context) (authenticationRateLimitCapacityDPoPNonceRequestFactory, string, error) {
				return repository.capacityDPoPNonceValidationFactory(ctx, runID, config)
			},
		},
	}
	phases := make([]AuthenticationRateLimitCapacityDPoPNoncePhase, 0, len(definitions))
	accepted := true
	for _, definition := range definitions {
		factory, domainID, err := definition.prepare(ctx)
		if err != nil {
			return nil, false, err
		}
		phase, err := repository.measureAuthenticationRateLimitCapacityDPoPNoncePhase(
			ctx,
			definition.name,
			domainID,
			config,
			factory,
		)
		if err != nil {
			return nil, false, err
		}
		phases = append(phases, phase)
		if !phase.P99LatencyAccepted || !phase.MinimumThroughputAccepted ||
			!phase.LifetimeAccepted || !phase.ActiveRowsAccepted {
			accepted = false
		}
	}
	return phases, accepted, nil
}

func (repository *Repository) measureAuthenticationRateLimitCapacityDPoPNoncePhase(
	ctx context.Context,
	name string,
	domainID string,
	config AuthenticationRateLimitCapacityDPoPNonceConfig,
	factory authenticationRateLimitCapacityDPoPNonceRequestFactory,
) (AuthenticationRateLimitCapacityDPoPNoncePhase, error) {
	phase := AuthenticationRateLimitCapacityDPoPNoncePhase{
		Name: name, Attempts: config.AttemptsPerPhase, Workers: config.Workers,
	}
	if factory == nil || domainID == "" {
		return AuthenticationRateLimitCapacityDPoPNoncePhase{}, ErrAuthenticationRateLimitCapacityInvalid
	}
	phaseCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	results := make(chan authenticationRateLimitCapacityDPoPNonceAttempt, config.Workers)
	var workers sync.WaitGroup
	for worker := uint32(0); worker < config.Workers; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				request := factory(index)
				started := time.Now()
				decision, err := repository.EvaluateDPoPNonce(phaseCtx, request)
				attempt := authenticationRateLimitCapacityDPoPNonceAttempt{
					latency: time.Since(started),
					err:     err,
				}
				if err == nil {
					attempt.validated = decision.Accepted()
					attempt.challenged = !decision.Accepted() && decision.Challenge() != ""
					if !decision.Valid() || attempt.validated == attempt.challenged {
						attempt.err = ErrAuthenticationRateLimitCapacityInvalid
					}
				}
				results <- attempt
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
		if result.challenged {
			phase.Challenges++
		}
		if result.validated {
			phase.Validated++
		}
	}
	phaseDuration := time.Since(started)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return AuthenticationRateLimitCapacityDPoPNoncePhase{}, ctxErr
	}
	if firstError != nil {
		return AuthenticationRateLimitCapacityDPoPNoncePhase{}, fmt.Errorf("measure DPoP nonce capacity: %w", firstError)
	}
	if len(latencies) != int(config.AttemptsPerPhase) ||
		phase.Challenges+phase.Validated != config.AttemptsPerPhase || phaseDuration <= 0 {
		return AuthenticationRateLimitCapacityDPoPNoncePhase{}, ErrAuthenticationRateLimitCapacityInvalid
	}
	activeRows, err := repository.countAuthenticationRateLimitCapacityActiveDPoPNonces(ctx, domainID)
	if err != nil {
		return AuthenticationRateLimitCapacityDPoPNoncePhase{}, err
	}
	phase.ActiveRows = activeRows
	sort.Slice(latencies, func(left, right int) bool { return latencies[left] < latencies[right] })
	phase.DurationNanoseconds = phaseDuration.Nanoseconds()
	phase.P50LatencyNanoseconds = capacityLatencyPercentile(latencies, 50).Nanoseconds()
	phase.P95LatencyNanoseconds = capacityLatencyPercentile(latencies, 95).Nanoseconds()
	phase.P99LatencyNanoseconds = capacityLatencyPercentile(latencies, 99).Nanoseconds()
	phase.MaximumLatencyNanoseconds = latencies[len(latencies)-1].Nanoseconds()
	phase.CompletedPerSecondMilli = capacityCompletedPerSecondMilli(config.AttemptsPerPhase, phaseDuration)
	phase.P99LatencyAccepted = phase.P99LatencyNanoseconds <= config.MaximumP99Latency.Nanoseconds()
	phase.MinimumThroughputAccepted = phase.CompletedPerSecondMilli >= uint64(config.MinimumThroughput)*1000
	phase.LifetimeAccepted = phaseDuration < config.Lifetime
	phase.ActiveRowsAccepted = activeRows == expectedAuthenticationRateLimitCapacityDPoPNonceRows(name, config)
	return phase, nil
}

func expectedAuthenticationRateLimitCapacityDPoPNonceRows(
	name string,
	config AuthenticationRateLimitCapacityDPoPNonceConfig,
) uint32 {
	switch name {
	case "nonce-issue-shared-key":
		return min(config.AttemptsPerPhase, config.MaximumActivePerKey)
	case "nonce-issue-distinct-keys":
		return config.AttemptsPerPhase
	case "nonce-validate":
		return config.Workers
	default:
		return 0
	}
}

func (repository *Repository) countAuthenticationRateLimitCapacityActiveDPoPNonces(
	ctx context.Context,
	domainID string,
) (uint32, error) {
	var count int64
	if err := repository.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM oidc_dpop_nonces
		WHERE isolation_domain_id = $1
		  AND expires_at > clock_timestamp()
	`, domainID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active DPoP nonce capacity rows: %w", err)
	}
	if count < 0 || count > int64(maximumAuthenticationRateLimitCapacityAttempts) {
		return 0, ErrAuthenticationRateLimitCapacityInvalid
	}
	return uint32(count), nil
}

func capacityDPoPNonceSharedKeyFactory(
	runID string,
	config AuthenticationRateLimitCapacityDPoPNonceConfig,
) (authenticationRateLimitCapacityDPoPNonceRequestFactory, string, error) {
	domainID := identity.Derived("iso", runID+"\x00nonce-shared-domain")
	keyDigest := sha256.Sum256([]byte(runID + "\x00nonce-shared-key"))
	request, err := authn.NewDPoPNonceRequest(
		domainID,
		keyDigest,
		[sha256.Size]byte{},
		config.Lifetime,
		config.MaximumActivePerKey,
	)
	if err != nil {
		return nil, "", ErrAuthenticationRateLimitCapacityInvalid
	}
	return func(int) authn.DPoPNonceRequest { return request }, domainID, nil
}

func capacityDPoPNonceDistinctKeyFactory(
	runID string,
	config AuthenticationRateLimitCapacityDPoPNonceConfig,
) (authenticationRateLimitCapacityDPoPNonceRequestFactory, string, error) {
	domainID := identity.Derived("iso", runID+"\x00nonce-distinct-domain")
	return func(index int) authn.DPoPNonceRequest {
		keyDigest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00nonce-distinct-key\x00%d", runID, index)))
		request, err := authn.NewDPoPNonceRequest(
			domainID,
			keyDigest,
			[sha256.Size]byte{},
			config.Lifetime,
			config.MaximumActivePerKey,
		)
		if err != nil {
			return authn.DPoPNonceRequest{}
		}
		return request
	}, domainID, nil
}

func (repository *Repository) capacityDPoPNonceValidationFactory(
	ctx context.Context,
	runID string,
	config AuthenticationRateLimitCapacityDPoPNonceConfig,
) (authenticationRateLimitCapacityDPoPNonceRequestFactory, string, error) {
	domainID := identity.Derived("iso", runID+"\x00nonce-validation-domain")
	requests := make([]authn.DPoPNonceRequest, 0, config.Workers)
	for index := uint32(0); index < config.Workers; index++ {
		keyDigest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00nonce-validation-key\x00%d", runID, index)))
		issue, err := authn.NewDPoPNonceRequest(
			domainID,
			keyDigest,
			[sha256.Size]byte{},
			config.Lifetime,
			config.MaximumActivePerKey,
		)
		if err != nil {
			return nil, "", ErrAuthenticationRateLimitCapacityInvalid
		}
		decision, err := repository.EvaluateDPoPNonce(ctx, issue)
		if err != nil {
			return nil, "", err
		}
		if !decision.Valid() || decision.Accepted() || decision.Challenge() == "" {
			return nil, "", ErrAuthenticationRateLimitCapacityInvalid
		}
		presentedDigest := sha256.Sum256([]byte(decision.Challenge()))
		request, err := authn.NewDPoPNonceRequest(
			domainID,
			keyDigest,
			presentedDigest,
			config.Lifetime,
			config.MaximumActivePerKey,
		)
		if err != nil {
			return nil, "", ErrAuthenticationRateLimitCapacityInvalid
		}
		requests = append(requests, request)
	}
	if len(requests) == 0 {
		return nil, "", errors.New("DPoP nonce validation capacity requests are empty")
	}
	return func(index int) authn.DPoPNonceRequest {
		return requests[index%len(requests)]
	}, domainID, nil
}
