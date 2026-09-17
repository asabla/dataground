package persistence

import (
	"context"
	"slices"

	"github.com/asabla/dataground/internal/domain"
)

// FindAuthorizedDevelopmentPublication observes only the immutable request for
// one independently reviewed revision. A valid draft without a request returns
// nil; missing scope, changed pins and operator-only requests are failures. It
// neither accepts publication nor grants a lease or publication authority.
func (repository *Repository) FindAuthorizedDevelopmentPublication(ctx context.Context, input DevelopmentPublicationInput) (*domain.Operation, error) {
	if repository == nil || !repository.Configured() || ctx == nil || ctx.Err() != nil || !input.ValidReviewedInputs() {
		return nil, ErrDevelopmentPublicationUnavailable
	}
	var service, profile, state string
	var capabilities []string
	var version int
	var operationID *string
	err := repository.pool.QueryRow(ctx, `SELECT revision.service_id,revision.runtime_profile,revision.required_capabilities,revision.version,revision.state,request.operation_id
 FROM service_revisions revision LEFT JOIN governed_publication_requests request
 ON request.isolation_domain_id=revision.isolation_domain_id AND request.revision_id=revision.id
 WHERE revision.isolation_domain_id=$1 AND revision.id=$2`, input.Target.IsolationDomainID, input.Target.RevisionID).Scan(&service, &profile, &capabilities, &version, &state, &operationID)
	if err != nil || service != input.Target.ServiceID || profile != input.Target.RuntimeProfile || !slices.Equal(capabilities, []string{GovernedInvocationRuntimeProfile}) {
		return nil, ErrDevelopmentPublicationUnavailable
	}
	if operationID == nil {
		if state != "draft" || version != input.ExpectedVersion {
			return nil, ErrDevelopmentPublicationUnavailable
		}
		return nil, nil
	}
	// Request rows cannot be changed or removed. Match every reviewed pin before
	// reading current lifecycle state, including historical terminal recovery.
	if err := repository.RequireAuthorizedDevelopmentPublication(ctx, *operationID, input); err != nil {
		return nil, ErrDevelopmentPublicationUnavailable
	}
	operation, err := repository.GetOperation(ctx, input.Target.IsolationDomainID, *operationID)
	if err != nil {
		return nil, ErrDevelopmentPublicationUnavailable
	}
	return &operation, nil
}
