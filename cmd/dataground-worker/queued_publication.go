package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/lifecycle/publication"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
)

var publicationOperationIDPattern = regexp.MustCompile(`^op_[0-9a-z]{20,32}$`)

type developmentPublicationStore interface {
	reconcile.Store
	FindAuthorizedDevelopmentPublication(context.Context, persistence.DevelopmentPublicationInput) (*domain.Operation, error)
	RequireAuthorizedDevelopmentPublication(context.Context, string, persistence.DevelopmentPublicationInput) error
	ClaimAuthorizedDevelopmentPublication(context.Context, string, persistence.DevelopmentPublicationInput, string, time.Duration) (*persistence.OperationClaim, error)
	CompleteAuthorizedDevelopmentPublication(context.Context, persistence.OperationClaim, persistence.DevelopmentPublicationInput, persistence.DevelopmentPublicationVerifier, persistence.PublicationAuthorization) error
	RequireDevelopmentPublication(context.Context, string, persistence.DevelopmentPublicationInput) error
	ClaimDevelopmentPublication(context.Context, string, persistence.DevelopmentPublicationInput, string, time.Duration) (*persistence.OperationClaim, error)
	CompleteDevelopmentPublication(context.Context, persistence.OperationClaim, persistence.DevelopmentPublicationInput, persistence.DevelopmentPublicationVerifier) error
	GetOperation(context.Context, string, string) (domain.Operation, error)
}

type developmentPublicationConsumer struct {
	developmentPublicationStore
	config    developmentPublicationConfiguration
	verify    persistence.DevelopmentPublicationVerifier
	authorize persistence.PublicationAuthorization
}

func (consumer *developmentPublicationConsumer) ClaimNext(ctx context.Context, kind, workerID string, _ time.Duration) (*persistence.OperationClaim, error) {
	if kind != persistence.OperationKindPublication {
		return nil, errDevelopmentPublication
	}
	// The existing signed verifier can require more than the reference lease.
	// The database caps this lease at the operation deadline; completion also
	// bounds verification by the actual stored lease, never by caller timestamps.
	if consumer.config.authorizedPublication() {
		return consumer.ClaimAuthorizedDevelopmentPublication(ctx, consumer.config.operationID, consumer.config.input, workerID, 2*time.Minute)
	}
	return consumer.ClaimDevelopmentPublication(ctx, consumer.config.operationID, consumer.config.input, workerID, 2*time.Minute)
}

func (consumer *developmentPublicationConsumer) PublishClaimed(ctx context.Context, claim persistence.OperationClaim) error {
	if claim.ID != consumer.config.operationID || claim.IsolationDomainID != consumer.config.input.Target.IsolationDomainID || claim.ResourceID != consumer.config.input.Target.RevisionID || claim.StateMachineVersion != publication.QueuedDevelopmentVersion {
		return errDevelopmentPublication
	}
	input := consumer.config.input
	input.ActorID, input.CorrelationID = claim.ActorID, claim.CorrelationID
	return consumer.CompleteDevelopmentPublication(ctx, claim, input, consumer.verify)
}

func (consumer *developmentPublicationConsumer) PublishAuthorizedClaimed(ctx context.Context, claim persistence.OperationClaim) error {
	if !consumer.config.authorizedPublication() || consumer.authorize == nil || claim.ID != consumer.config.operationID || claim.IsolationDomainID != consumer.config.input.Target.IsolationDomainID || claim.ResourceID != consumer.config.input.Target.RevisionID || claim.StateMachineVersion != publication.AuthorizedDevelopmentVersion {
		return errDevelopmentPublication
	}
	input := consumer.config.input
	input.ActorID, input.CorrelationID = claim.ActorID, claim.CorrelationID
	return consumer.CompleteAuthorizedDevelopmentPublication(ctx, claim, input, consumer.verify, consumer.authorize)
}

func (*developmentPublicationConsumer) Apply(context.Context, persistence.EffectRecord) (map[string]any, error) {
	return nil, errDevelopmentPublication
}
func (*developmentPublicationConsumer) Observe(context.Context, persistence.EffectRecord) (map[string]any, bool, error) {
	return nil, false, errDevelopmentPublication
}

func consumeDevelopmentPublication(ctx context.Context, store developmentPublicationStore, config developmentPublicationConfiguration, workerID string, verify persistence.DevelopmentPublicationVerifier, output io.Writer) error {
	if config.authorizedPublication() {
		return errDevelopmentPublication
	}
	return consumePublication(ctx, store, config, workerID, verify, nil, output)
}

func consumeAuthorizedDevelopmentPublication(ctx context.Context, store developmentPublicationStore, config developmentPublicationConfiguration, workerID string, verify persistence.DevelopmentPublicationVerifier, authorize persistence.PublicationAuthorization, output io.Writer) error {
	if !config.authorizedPublication() || authorize == nil {
		return errDevelopmentPublication
	}
	return consumePublication(ctx, store, config, workerID, verify, authorize, output)
}

func consumePublication(ctx context.Context, store developmentPublicationStore, config developmentPublicationConfiguration, workerID string, verify persistence.DevelopmentPublicationVerifier, authorize persistence.PublicationAuthorization, output io.Writer) error {
	if ctx == nil || ctx.Err() != nil || store == nil || verify == nil || workerID == "" || len(workerID) > 256 || strings.TrimSpace(workerID) != workerID || strings.ContainsAny(workerID, "\r\n\x00") || (!publicationOperationIDPattern.MatchString(config.operationID) && !(config.authorizedPublication() && config.operationID == "")) || !config.input.ValidReviewedInputs() {
		return errDevelopmentPublication
	}
	if config.authorizedPublication() && config.operationID == "" {
		operation, err := awaitAuthorizedPublication(ctx, store, config.input)
		if err != nil {
			return errDevelopmentPublication
		}
		config.operationID = operation.Metadata.ID
	}
	version := publication.QueuedDevelopmentVersion
	require := store.RequireDevelopmentPublication
	if config.authorizedPublication() {
		version = publication.AuthorizedDevelopmentVersion
		require = store.RequireAuthorizedDevelopmentPublication
	}
	if err := require(ctx, config.operationID, config.input); err != nil {
		return errDevelopmentPublication
	}
	consumer := &developmentPublicationConsumer{developmentPublicationStore: store, config: config, verify: verify, authorize: authorize}
	worker := reconcile.New(consumer, consumer, workerID)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return errDevelopmentPublication
		}
		operation, err := store.GetOperation(ctx, config.input.Target.IsolationDomainID, config.operationID)
		if err != nil || operation.Metadata.ID != config.operationID || operation.Metadata.IsolationDomainID != config.input.Target.IsolationDomainID || operation.ResourceID != config.input.Target.RevisionID || operation.Kind != persistence.OperationKindPublication || operation.StateMachineVersion != version {
			return errDevelopmentPublication
		}
		if publication.IsTerminal(publication.State(operation.ObservedState)) {
			encoded, err := json.Marshal(operation)
			if err != nil {
				return errDevelopmentPublication
			}
			if err := writeDevelopmentPublicationReceipt(output, encoded); err != nil {
				return err
			}
			if operation.ObservedState != "published" {
				return errors.New("governed publication ended without publication; inspect the recorded operation")
			}
			return nil
		}
		if operation.ObservedState != "queued" && operation.ObservedState != "validating" {
			return errDevelopmentPublication
		}
		if _, err := worker.RunOne(ctx, persistence.OperationKindPublication); err != nil && !errors.Is(err, persistence.ErrLeaseLost) {
			return errDevelopmentPublication
		}
		select {
		case <-ctx.Done():
			return errDevelopmentPublication
		case <-ticker.C:
		}
	}
}

// Waiting observes accepted state only. No verifier, authorization callback or
// claim runs before the API has durably accepted the exact reviewed request.
func awaitAuthorizedPublication(ctx context.Context, store developmentPublicationStore, input persistence.DevelopmentPublicationInput) (*domain.Operation, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil, errDevelopmentPublication
		}
		operation, err := store.FindAuthorizedDevelopmentPublication(ctx, input)
		if err != nil {
			return nil, errDevelopmentPublication
		}
		if operation != nil {
			if !publicationOperationIDPattern.MatchString(operation.Metadata.ID) || operation.Metadata.IsolationDomainID != input.Target.IsolationDomainID || operation.ResourceID != input.Target.RevisionID || operation.Kind != persistence.OperationKindPublication || operation.StateMachineVersion != publication.AuthorizedDevelopmentVersion {
				return nil, errDevelopmentPublication
			}
			return operation, nil
		}
		select {
		case <-ctx.Done():
			return nil, errDevelopmentPublication
		case <-ticker.C:
		}
	}
}
