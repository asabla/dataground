package reconcile

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

var invocationApprovalIDPattern = regexp.MustCompile(`^apr_[0-9a-z]{20,32}$`)

var adapterApprovalIDPattern = regexp.MustCompile(`^approval-[1-9][0-9]{0,19}$`)

// Routing authority belongs to one live turn. Its adapter-local handle never
// enters persistence and cannot be reconstructed after a worker replacement.
type invocationRuntimeApprovals struct {
	driver    *InvocationRuntimeDriver
	target    persistence.InvocationRuntimeTarget
	effect    persistence.EffectRecord
	turn      dgruntime.ApprovalTurn
	pending   *persistence.InvocationRuntimeApproval
	adapterID string
}

func (approvals *invocationRuntimeApprovals) record(ctx context.Context, claim persistence.OperationClaim, event dgruntime.Event, ended bool) (bool, error) {
	if !strings.HasPrefix(event.Type, "interaction.approval.") {
		return false, nil
	}
	if event.Type != "interaction.approval.requested" || governedInvocationDependencyMissing(approvals.turn) || governedInvocationDependencyMissing(approvals.driver.approvalStore) || governedInvocationDependencyMissing(approvals.driver.approvalAuthorizer) {
		return true, ErrInvocationApprovalUnavailable
	}
	ctx, cancel := context.WithDeadline(ctx, claim.DeadlineAt)
	defer cancel()
	if err := approvals.driver.ready(ctx); err != nil {
		return true, err
	}
	if approvals.pending != nil {
		active, err := approvals.turn.ApprovalPending(ctx, approvals.adapterID)
		if err != nil {
			return true, err
		}
		if active {
			return true, persistence.ErrInvocationRuntimeApprovalConflict
		}
		if err := approvals.close(ctx, claim, "runtime-request-cleared"); err != nil {
			return true, err
		}
	}
	id, idOK := event.Payload["approvalId"].(string)
	action, actionOK := event.Payload["action"].(string)
	if !idOK || !adapterApprovalIDPattern.MatchString(id) || !actionOK || (action != "process.execute" && action != "workspace.change") || len(event.Payload) != 2 || event.Sequence == 0 {
		return true, ErrInvocationApprovalInvalid
	}
	value, err := approvals.driver.approvalStore.RecordInvocationRuntimeApprovalRequest(ctx, claim, approvals.effect, approvals.target, persistence.InvocationRuntimeApprovalRequest{SourceSequence: event.Sequence, RequestedAction: action})
	if err != nil {
		return true, err
	}
	if value.Contract != persistence.InvocationRuntimeApprovalContract || !invocationApprovalIDPattern.MatchString(value.ID) || value.IsolationDomainID != approvals.target.IsolationDomainID || value.OperationID != claim.ID || value.InvocationID != approvals.target.InvocationID || value.ServiceID != approvals.target.ServiceID || value.RevisionID != approvals.target.RevisionID || value.EffectID != approvals.effect.EffectID || value.SourceSequence != event.Sequence || value.RequestedAction != action || value.State != "pending" || value.Version != 1 || !value.ExpiresAt.After(value.CreatedAt) || value.ExpiresAt.After(value.CreatedAt.Add(15*time.Minute)) || value.ExpiresAt.After(claim.DeadlineAt) {
		return true, persistence.ErrInvocationRuntimeApprovalConflict
	}
	approvals.pending = &value
	approvals.adapterID = id
	// A completion can precede draining its queued request event. Retain that
	// request as closed evidence without asking a controller to decide it.
	if ended {
		return true, approvals.close(ctx, claim, "runtime-ended")
	}
	return true, nil
}

func (approvals *invocationRuntimeApprovals) close(ctx context.Context, claim persistence.OperationClaim, reason string) error {
	if approvals.pending == nil {
		return nil
	}
	_, err := approvals.driver.approvalStore.CloseInvocationRuntimeApproval(ctx, claim, approvals.effect, approvals.pending.ID, reason)
	if err == nil {
		approvals.pending = nil
		approvals.adapterID = ""
	}
	return err
}

func (approvals *invocationRuntimeApprovals) poll(ctx context.Context, claim persistence.OperationClaim) error {
	if approvals.pending == nil {
		return nil
	}
	original := *approvals.pending
	ctx, cancel := context.WithDeadline(ctx, original.ExpiresAt)
	defer cancel()
	if err := approvals.driver.ready(ctx); err != nil {
		return err
	}
	active, err := approvals.turn.ApprovalPending(ctx, approvals.adapterID)
	if err != nil {
		return err
	}
	if !active {
		return approvals.close(ctx, claim, "runtime-request-cleared")
	}
	value, err := approvals.driver.approvalStore.GetInvocationRuntimeApproval(ctx, original.IsolationDomainID, original.ID)
	if err != nil {
		return err
	}
	if !sameRuntimeApprovalRequest(value, original) {
		return persistence.ErrInvocationRuntimeApprovalConflict
	}
	switch value.State {
	case "pending":
		if value.Version != 1 {
			return persistence.ErrInvocationRuntimeApprovalConflict
		}
		return nil
	case "closed", "expired":
		return persistence.ErrInvocationRuntimeApprovalExpired
	case "delivering", "delivery_unknown", "delivered":
		return persistence.ErrInvocationRuntimeApprovalDeliveryAmbiguous
	case "resolved":
		if value.Version != 2 || (value.Decision != "approve" && value.Decision != "deny") || value.ResolvedBy == "" || value.ResolutionCorrelationID == "" || value.ResolvedAt.IsZero() {
			return persistence.ErrInvocationRuntimeApprovalConflict
		}
	default:
		return ErrInvocationApprovalInvalid
	}
	accepted := value
	effective := value.Decision
	err = approvals.driver.approvalAuthorizer.AuthorizeInvocationApproval(ctx, value, InvocationApprovalPhaseEffect)
	if errors.Is(err, ErrInvocationApprovalDenied) {
		effective = string(dgruntime.ApprovalDeny)
	} else if err != nil {
		return err
	}
	if err := approvals.driver.ready(ctx); err != nil {
		return err
	}
	active, err = approvals.turn.ApprovalPending(ctx, approvals.adapterID)
	if err != nil {
		return err
	}
	if !active {
		return approvals.close(ctx, claim, "runtime-request-cleared")
	}
	value, err = approvals.driver.approvalStore.BeginInvocationRuntimeApprovalDelivery(ctx, claim, approvals.effect, value.ID, effective)
	if err != nil {
		return err
	}
	if !sameRuntimeApprovalRequest(value, accepted) || value.State != "delivering" || value.Version != 3 || value.EffectiveDecision != effective || value.Decision != accepted.Decision || value.ResolvedBy != accepted.ResolvedBy || value.ResolutionCorrelationID != accepted.ResolutionCorrelationID || !value.ResolvedAt.Equal(accepted.ResolvedAt) {
		return persistence.ErrInvocationRuntimeApprovalConflict
	}
	// Reservation stays single-use even when readiness, clearance or a deadline
	// changes before the guarded native write. Cleanup records that uncertainty.
	deliveryCtx, stop := context.WithDeadline(ctx, claim.LeaseExpiresAt)
	err = approvals.driver.ready(deliveryCtx)
	if err == nil {
		err = approvals.turn.ResolveApproval(deliveryCtx, approvals.adapterID, dgruntime.ApprovalDecision(effective))
	}
	stop()
	if err != nil {
		return errors.Join(persistence.ErrInvocationRuntimeApprovalDeliveryAmbiguous, err)
	}
	if err := approvals.driver.ready(ctx); err != nil {
		return err
	}
	if _, err := approvals.driver.approvalStore.CompleteInvocationRuntimeApprovalDelivery(ctx, claim, approvals.effect, value.ID); err != nil {
		return errors.Join(persistence.ErrInvocationRuntimeApprovalDeliveryAmbiguous, err)
	}
	approvals.pending = nil
	approvals.adapterID = ""
	return nil
}

func sameRuntimeApprovalRequest(left, right persistence.InvocationRuntimeApproval) bool {
	return left.Contract == right.Contract && left.ID == right.ID && left.IsolationDomainID == right.IsolationDomainID && left.OperationID == right.OperationID && left.InvocationID == right.InvocationID && left.ServiceID == right.ServiceID && left.RevisionID == right.RevisionID && left.EffectID == right.EffectID && left.SourceSequence == right.SourceSequence && left.RequestedAction == right.RequestedAction && left.CreatedAt.Equal(right.CreatedAt) && left.ExpiresAt.Equal(right.ExpiresAt)
}

// The bound matches the pinned adapter's event buffer, allowing independent
// native requests without an unbounded in-memory routing table.
const maximumPendingRuntimeApprovals = 256

type invocationRuntimeApprovalSet struct {
	template invocationRuntimeApprovals
	pending  []*invocationRuntimeApprovals
	next     int
}

func (set *invocationRuntimeApprovalSet) record(ctx context.Context, claim persistence.OperationClaim, event dgruntime.Event, ended bool) (bool, error) {
	if !strings.HasPrefix(event.Type, "interaction.approval.") {
		return false, nil
	}
	if !ended && len(set.pending) >= maximumPendingRuntimeApprovals {
		return true, ErrInvocationApprovalUnavailable
	}
	id, _ := event.Payload["approvalId"].(string)
	for _, current := range set.pending {
		if current.adapterID == id || current.pending.SourceSequence == event.Sequence {
			return true, persistence.ErrInvocationRuntimeApprovalConflict
		}
	}
	current := set.template
	handled, err := current.record(ctx, claim, event, ended)
	if current.pending != nil {
		for _, existing := range set.pending {
			if existing.pending.ID == current.pending.ID {
				return true, persistence.ErrInvocationRuntimeApprovalConflict
			}
		}
		set.pending = append(set.pending, &current)
	}
	return handled, err
}

func (set *invocationRuntimeApprovalSet) poll(ctx context.Context, claim persistence.OperationClaim) error {
	if len(set.pending) == 0 {
		return nil
	}
	if set.next >= len(set.pending) {
		set.next = 0
	}
	current := set.pending[set.next]
	// One request per tick leaves events, lease renewal and questions a scheduling
	// opportunity even when many controllers are deciding independently.
	ctx, cancel := context.WithDeadline(ctx, claim.LeaseExpiresAt)
	defer cancel()
	if err := current.poll(ctx, claim); err != nil {
		return err
	}
	if current.pending == nil {
		set.pending = append(set.pending[:set.next], set.pending[set.next+1:]...)
	} else {
		set.next++
	}
	return nil
}

func (set *invocationRuntimeApprovalSet) close(ctx context.Context, claim persistence.OperationClaim, reason string) error {
	var result error
	remaining := set.pending[:0]
	for _, current := range set.pending {
		if err := current.close(ctx, claim, reason); err != nil {
			result = errors.Join(result, err)
			remaining = append(remaining, current)
		}
	}
	set.pending = remaining
	set.next = 0
	return result
}
