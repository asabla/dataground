package authn

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestOIDCJWTKeysetRefreshPolicyRejectsUnsafeBounds(t *testing.T) {
	t.Parallel()

	valid := OIDCJWTKeysetRefreshPolicy{Interval: time.Minute, Timeout: 5 * time.Second}
	if !valid.Valid() {
		t.Fatal("valid refresh policy was rejected")
	}
	for name, policy := range map[string]OIDCJWTKeysetRefreshPolicy{
		"zero interval":     {Timeout: time.Second},
		"short interval":    {Interval: time.Millisecond, Timeout: time.Millisecond},
		"long interval":     {Interval: time.Hour + time.Second, Timeout: time.Second},
		"zero timeout":      {Interval: time.Minute},
		"short timeout":     {Interval: time.Minute, Timeout: time.Millisecond},
		"long timeout":      {Interval: time.Hour, Timeout: time.Minute + time.Second},
		"timeout after tick": {Interval: time.Second, Timeout: 2 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if policy.Valid() {
				t.Fatal("invalid refresh policy was accepted")
			}
		})
	}
}

func TestOIDCJWTKeysetRefreshSupervisorRecordsSafeOutcomes(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	target := &supervisorRefreshTarget{refreshErrors: []error{ErrUnavailable, nil}}
	times := []time.Time{
		time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC),
		time.Date(2026, time.August, 1, 12, 1, 0, 0, time.UTC),
	}
	waits := 0
	supervisor := &OIDCJWTKeysetRefreshSupervisor{
		target: target,
		policy: OIDCJWTKeysetRefreshPolicy{Interval: time.Minute, Timeout: time.Second},
		now: func() time.Time {
			return times[target.refreshCount()]
		},
		wait: func(ctx context.Context, _ time.Duration) error {
			waits++
			if waits == 3 {
				cancel()
				return ctx.Err()
			}
			return nil
		},
	}
	if err := supervisor.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh supervisor exit = %v", err)
	}
	status := supervisor.Status()
	if status.Running {
		t.Fatal("stopped supervisor remained ready")
	}
	if status.LastAttemptAt != times[1] || status.LastSuccessAt != times[1] {
		t.Fatalf("refresh timestamps = %#v", status)
	}
	if status.ConsecutiveFailures != 0 || status.LastFailure != OIDCJWTKeysetRefreshFailureNone {
		t.Fatalf("successful refresh did not clear failure state: %#v", status)
	}
	if target.refreshCount() != 2 {
		t.Fatalf("refresh calls = %d, want 2", target.refreshCount())
	}
	if _, err := json.Marshal(supervisor); err == nil {
		t.Fatal("refresh supervisor serialization succeeded")
	}
}

func TestOIDCJWTKeysetRefreshSupervisorRejectsConcurrentOwnership(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	supervisor := &OIDCJWTKeysetRefreshSupervisor{
		target: &supervisorRefreshTarget{},
		policy: OIDCJWTKeysetRefreshPolicy{Interval: time.Minute, Timeout: time.Second},
		now:    time.Now,
		wait: func(ctx context.Context, _ time.Duration) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	first := make(chan error, 1)
	go func() { first <- supervisor.Run(ctx) }()
	<-started
	if err := supervisor.Run(context.Background()); !errors.Is(err, ErrOIDCJWTKeysetRefreshAlreadyRunning) {
		t.Fatalf("concurrent supervisor error = %v", err)
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("owned supervisor exit = %v", err)
	}
}

func TestOIDCJWTKeysetRefreshSupervisorReadinessRequiresRunningValidGeneration(t *testing.T) {
	t.Parallel()

	target := &supervisorRefreshTarget{}
	supervisor := &OIDCJWTKeysetRefreshSupervisor{target: target}
	if err := supervisor.Ready(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stopped supervisor readiness = %v", err)
	}
	supervisor.status.Running = true
	if err := supervisor.Ready(context.Background()); err != nil {
		t.Fatalf("running supervisor readiness = %v", err)
	}
	target.readyError = ErrUnavailable
	if err := supervisor.Ready(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expired target readiness = %v", err)
	}
}

func TestOIDCJWTKeysetRefreshFailureClassesDoNotExposeDependencyErrors(t *testing.T) {
	t.Parallel()

	for name, fixture := range map[string]struct {
		err  error
		want OIDCJWTKeysetRefreshFailure
	}{
		"timeout":     {context.DeadlineExceeded, OIDCJWTKeysetRefreshFailureTimeout},
		"invalid":     {ErrOIDCJWTKeysetInvalid, OIDCJWTKeysetRefreshFailureInvalid},
		"rollback":    {ErrOIDCJWTKeysetRollback, OIDCJWTKeysetRefreshFailureRollback},
		"conflict":    {ErrOIDCJWTKeysetConflict, OIDCJWTKeysetRefreshFailureConflict},
		"unavailable": {errors.New("private source detail"), OIDCJWTKeysetRefreshFailureUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := classifyOIDCJWTKeysetRefreshFailure(fixture.err); got != fixture.want {
				t.Fatalf("failure class = %q, want %q", got, fixture.want)
			}
		})
	}
}

func TestReloadableOIDCJWTVerifierReadinessTracksExpiry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	verifier := &ReloadableOIDCJWTVerifier{
		now:       func() time.Time { return now },
		verifier:  &lifecycleTokenVerifier{},
		expiresAt: now.Add(time.Minute),
	}
	if err := verifier.Ready(context.Background()); err != nil {
		t.Fatalf("valid generation readiness = %v", err)
	}
	now = now.Add(time.Minute)
	if err := verifier.Ready(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expired generation readiness = %v", err)
	}
}

func TestReloadableOIDCJWTVerifierSerializesSourceAccess(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	verifier := &ReloadableOIDCJWTVerifier{
		source: blockingSupervisorKeysetSource{started: started, release: release},
		now:    time.Now,
	}
	refreshed := make(chan error, 1)
	go func() { refreshed <- verifier.Refresh(context.Background()) }()
	<-started
	if verifier.refreshMu.TryLock() {
		verifier.refreshMu.Unlock()
		t.Fatal("refresh source was not exclusively owned")
	}
	close(release)
	if err := <-refreshed; !errors.Is(err, ErrUnavailable) {
		t.Fatalf("blocked refresh = %v", err)
	}
}

type supervisorRefreshTarget struct {
	mu            sync.Mutex
	refreshErrors []error
	refreshes     int
	readyError    error
}

func (target *supervisorRefreshTarget) Refresh(context.Context) error {
	target.mu.Lock()
	defer target.mu.Unlock()
	index := target.refreshes
	target.refreshes++
	if index < len(target.refreshErrors) {
		return target.refreshErrors[index]
	}
	return nil
}

func (target *supervisorRefreshTarget) Ready(context.Context) error {
	return target.readyError
}

func (target *supervisorRefreshTarget) refreshCount() int {
	target.mu.Lock()
	defer target.mu.Unlock()
	return target.refreshes
}

type blockingSupervisorKeysetSource struct {
	started chan struct{}
	release chan struct{}
}

func (source blockingSupervisorKeysetSource) Load(context.Context) (OIDCJWTKeysetSnapshot, error) {
	close(source.started)
	<-source.release
	return OIDCJWTKeysetSnapshot{}, errors.New("source unavailable")
}

var _ oidcJWTKeysetRefreshTarget = (*supervisorRefreshTarget)(nil)
var _ OIDCJWTKeysetSource = blockingSupervisorKeysetSource{}
