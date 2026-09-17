package main

import (
	"context"
	"slices"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/reconcile"
)

const waitForPublicationEnvironment = "DATAGROUND_DEVELOPMENT_WAIT_FOR_PUBLICATION"

type governedRevisionReader interface {
	GetServiceRevision(context.Context, string, string) (domain.ServiceRevision, error)
}

// Waiting owns no execution resources and grants no readiness. After publication
// is observed, normal composition must still verify current runtime acceptance,
// published scope, policy and every provider and execution dependency.
func awaitGovernedRevisionPublication(ctx context.Context, reader governedRevisionReader, target runtimeCertificationTarget) error {
	if ctx == nil || reader == nil || !target.valid() {
		return ErrRuntimeCertificationScopeMismatch
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		revision, err := reader.GetServiceRevision(ctx, target.isolationDomainID, target.revisionID)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return ErrRuntimeCertificationUnavailable
		}
		if revision.Metadata.IsolationDomainID != target.isolationDomainID || revision.Metadata.ID != target.revisionID || revision.ServiceID != target.serviceID || revision.RuntimeProfile != reconcile.CodexAppServerRuntimeProfileV1 || !slices.Equal(revision.RequiredCapabilities, []string{reconcile.CodexAppServerRuntimeProfileV1}) {
			return ErrRuntimeCertificationScopeMismatch
		}
		switch revision.State {
		case "published":
			return nil
		case "draft":
		default:
			return ErrRuntimeCertificationScopeMismatch
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
