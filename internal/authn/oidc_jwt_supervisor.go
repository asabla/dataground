package authn

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

const (
	minimumOIDCJWTKeysetRefreshInterval = time.Second
	maximumOIDCJWTKeysetRefreshInterval = time.Hour
	minimumOIDCJWTKeysetRefreshTimeout  = 100 * time.Millisecond
	maximumOIDCJWTKeysetRefreshTimeout  = time.Minute
)

// ErrOIDCJWTKeysetRefreshAlreadyRunning prevents two deployment lifecycles
// from owning the same serving verifier.
var ErrOIDCJWTKeysetRefreshAlreadyRunning = errors.New("OIDC JWT keyset refresh is already running")

type OIDCJWTKeysetRefreshFailure string

const (
	OIDCJWTKeysetRefreshFailureNone        OIDCJWTKeysetRefreshFailure = ""
	OIDCJWTKeysetRefreshFailureInvalid     OIDCJWTKeysetRefreshFailure = "invalid"
	OIDCJWTKeysetRefreshFailureRollback    OIDCJWTKeysetRefreshFailure = "rollback"
	OIDCJWTKeysetRefreshFailureConflict    OIDCJWTKeysetRefreshFailure = "conflict"
	OIDCJWTKeysetRefreshFailureUnavailable OIDCJWTKeysetRefreshFailure = "unavailable"
	OIDCJWTKeysetRefreshFailureTimeout     OIDCJWTKeysetRefreshFailure = "timeout"
)

// OIDCJWTKeysetRefreshPolicy bounds publication polling and each source call.
// The timeout must not exceed the interval.
type OIDCJWTKeysetRefreshPolicy struct {
	Interval time.Duration
	Timeout  time.Duration
}

func (policy OIDCJWTKeysetRefreshPolicy) Valid() bool {
	return policy.Interval >= minimumOIDCJWTKeysetRefreshInterval &&
		policy.Interval <= maximumOIDCJWTKeysetRefreshInterval &&
		policy.Timeout >= minimumOIDCJWTKeysetRefreshTimeout &&
		policy.Timeout <= maximumOIDCJWTKeysetRefreshTimeout &&
		policy.Timeout <= policy.Interval
}

// OIDCJWTKeysetRefreshStatus is safe operational state. It intentionally omits
// key material, key identifiers, issuer data, source errors, and keyset digests.
type OIDCJWTKeysetRefreshStatus struct {
	Running             bool
	Refreshing          bool
	LastAttemptAt       time.Time
	LastSuccessAt       time.Time
	ConsecutiveFailures uint64
	LastFailure         OIDCJWTKeysetRefreshFailure
}

type oidcJWTKeysetRefreshTarget interface {
	Refresh(context.Context) error
	Ready(context.Context) error
}

// OIDCJWTKeysetRefreshSupervisor gives one deployment lifecycle exclusive
// ownership of periodic refreshes while retaining a still-valid generation
// across transient publication failures.
type OIDCJWTKeysetRefreshSupervisor struct {
	target oidcJWTKeysetRefreshTarget
	policy OIDCJWTKeysetRefreshPolicy
	now    func() time.Time
	wait   func(context.Context, time.Duration) error

	mu     sync.RWMutex
	status OIDCJWTKeysetRefreshStatus
}

func NewOIDCJWTKeysetRefreshSupervisor(
	target *ReloadableOIDCJWTVerifier,
	policy OIDCJWTKeysetRefreshPolicy,
) (*OIDCJWTKeysetRefreshSupervisor, error) {
	if target == nil {
		return nil, errors.New("OIDC JWT keyset refresh target is required")
	}
	if !policy.Valid() {
		return nil, errors.New("OIDC JWT keyset refresh policy is invalid")
	}
	return &OIDCJWTKeysetRefreshSupervisor{
		target: target,
		policy: policy,
		now:    time.Now,
		wait:   waitForOIDCJWTKeysetRefresh,
	}, nil
}

func (supervisor *OIDCJWTKeysetRefreshSupervisor) Run(ctx context.Context) error {
	if supervisor == nil || ctx == nil || supervisor.target == nil ||
		supervisor.now == nil || supervisor.wait == nil || !supervisor.policy.Valid() {
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	supervisor.mu.Lock()
	if supervisor.status.Running {
		supervisor.mu.Unlock()
		return ErrOIDCJWTKeysetRefreshAlreadyRunning
	}
	supervisor.status.Running = true
	supervisor.mu.Unlock()
	defer func() {
		supervisor.mu.Lock()
		supervisor.status.Running = false
		supervisor.status.Refreshing = false
		supervisor.mu.Unlock()
	}()

	for {
		if err := supervisor.wait(ctx, supervisor.policy.Interval); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return ErrUnavailable
		}
		attemptedAt := supervisor.now().UTC()
		supervisor.beginRefresh(attemptedAt)
		attemptCtx, cancel := context.WithTimeout(ctx, supervisor.policy.Timeout)
		err := supervisor.target.Refresh(attemptCtx)
		attemptErr := attemptCtx.Err()
		cancel()
		if err != nil && errors.Is(attemptErr, context.DeadlineExceeded) {
			err = context.DeadlineExceeded
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			if err == nil {
				supervisor.recordRefresh(attemptedAt, nil)
			} else {
				supervisor.finishCancelledRefresh()
			}
			return ctxErr
		}
		supervisor.recordRefresh(attemptedAt, err)
	}
}

func (supervisor *OIDCJWTKeysetRefreshSupervisor) Ready(ctx context.Context) error {
	if supervisor == nil || ctx == nil || supervisor.target == nil {
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	supervisor.mu.RLock()
	running := supervisor.status.Running
	supervisor.mu.RUnlock()
	if !running {
		return ErrUnavailable
	}
	return supervisor.target.Ready(ctx)
}

func (supervisor *OIDCJWTKeysetRefreshSupervisor) Status() OIDCJWTKeysetRefreshStatus {
	if supervisor == nil {
		return OIDCJWTKeysetRefreshStatus{}
	}
	supervisor.mu.RLock()
	defer supervisor.mu.RUnlock()
	return supervisor.status
}

func (supervisor *OIDCJWTKeysetRefreshSupervisor) beginRefresh(attemptedAt time.Time) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	supervisor.status.Refreshing = true
	supervisor.status.LastAttemptAt = attemptedAt
}

func (supervisor *OIDCJWTKeysetRefreshSupervisor) finishCancelledRefresh() {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	supervisor.status.Refreshing = false
}

func (supervisor *OIDCJWTKeysetRefreshSupervisor) recordRefresh(attemptedAt time.Time, err error) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	supervisor.status.Refreshing = false
	supervisor.status.LastAttemptAt = attemptedAt
	if err == nil {
		supervisor.status.LastSuccessAt = attemptedAt
		supervisor.status.ConsecutiveFailures = 0
		supervisor.status.LastFailure = OIDCJWTKeysetRefreshFailureNone
		return
	}
	if supervisor.status.ConsecutiveFailures < ^uint64(0) {
		supervisor.status.ConsecutiveFailures++
	}
	supervisor.status.LastFailure = classifyOIDCJWTKeysetRefreshFailure(err)
}

func classifyOIDCJWTKeysetRefreshFailure(err error) OIDCJWTKeysetRefreshFailure {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return OIDCJWTKeysetRefreshFailureTimeout
	case errors.Is(err, ErrOIDCJWTKeysetInvalid):
		return OIDCJWTKeysetRefreshFailureInvalid
	case errors.Is(err, ErrOIDCJWTKeysetRollback):
		return OIDCJWTKeysetRefreshFailureRollback
	case errors.Is(err, ErrOIDCJWTKeysetConflict):
		return OIDCJWTKeysetRefreshFailureConflict
	default:
		return OIDCJWTKeysetRefreshFailureUnavailable
	}
}

func waitForOIDCJWTKeysetRefresh(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (*OIDCJWTKeysetRefreshSupervisor) MarshalJSON() ([]byte, error) {
	return nil, errors.New("OIDC JWT keyset refresh supervisors cannot be serialized")
}

var _ json.Marshaler = (*OIDCJWTKeysetRefreshSupervisor)(nil)
