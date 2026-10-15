package authz

import (
	"github.com/asabla/dataground/internal/workspace"
	"regexp"
)

var sourceReadEffectPattern = regexp.MustCompile(`^eff_[0-9a-z]{20,32}$`)

// SourceReadRequest contains only stored identity, immutable source selection,
// and bounded claim facts. It contains no paths, content, or credentials.
type SourceReadRequest struct {
	Use          workspace.SourceUse
	ServiceID    string
	InvocationID string
	EffectID     string
	FencingToken int64
	PlanDigest   string
	Phase        string
}

func (r SourceReadRequest) Valid() bool {
	return r.Use.Valid() && invocationAuditServiceIDPattern.MatchString(r.ServiceID) && invocationAuditInvocationIDPattern.MatchString(r.InvocationID) && sourceReadEffectPattern.MatchString(r.EffectID) && r.FencingToken > 0 && policyDigestPattern.MatchString(r.PlanDigest) && (r.Phase == workspace.SourceReadAdmission || r.Phase == workspace.SourceReadDelivery)
}
