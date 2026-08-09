package persistence

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
)

const GovernedInvocationRuntimeProfile = "codex.app-server/v1"

var (
	dispatchIsolationDomainPattern = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
	dispatchServicePattern         = regexp.MustCompile(`^svc_[0-9a-z]{20,32}$`)
	dispatchRevisionPattern        = regexp.MustCompile(`^rev_[0-9a-z]{20,32}$`)
)

// InvocationDispatchTarget binds API acceptance to the single service revision
// leased by a governed worker. It is an admission constraint, not a substitute
// for the worker's effect-time policy, credential, or certification checks.
type InvocationDispatchTarget struct {
	IsolationDomainID string
	ServiceID         string
	RevisionID        string
	RuntimeProfile    string
}

func (target InvocationDispatchTarget) Valid() bool {
	return dispatchIsolationDomainPattern.MatchString(target.IsolationDomainID) &&
		dispatchServicePattern.MatchString(target.ServiceID) &&
		dispatchRevisionPattern.MatchString(target.RevisionID) &&
		target.RuntimeProfile == GovernedInvocationRuntimeProfile
}

// RequireInvocationDispatchTarget verifies that startup configuration names an
// existing published revision with the exact worker-supported runtime profile.
func (repository *Repository) RequireInvocationDispatchTarget(
	ctx context.Context,
	target InvocationDispatchTarget,
) error {
	if repository == nil || !repository.Configured() || !target.Valid() {
		return errors.New("invocation dispatch target is invalid")
	}
	var exists bool
	err := repository.pool.QueryRow(ctx, `
		SELECT true
		FROM service_revisions
		WHERE isolation_domain_id = $1
		  AND service_id = $2
		  AND id = $3
		  AND runtime_profile = $4
		  AND state = 'published'
	`, target.IsolationDomainID, target.ServiceID, target.RevisionID, target.RuntimeProfile).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return invocationDispatchMismatch()
	}
	if err != nil {
		return fmt.Errorf("verify invocation dispatch target: %w", err)
	}
	if !exists {
		return invocationDispatchMismatch()
	}
	return nil
}

func invocationDispatchMismatch() *DomainError {
	return &DomainError{
		Code:    "INVOCATION_DISPATCH_TARGET_MISMATCH",
		Message: "Invocation target is outside the configured governed dispatch scope.",
	}
}
