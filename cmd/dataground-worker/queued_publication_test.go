package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/persistence"
)

func queuedPublicationArguments() []string {
	args := publicationArguments()
	args[0] = "queue-development-publication"
	return append(args, "--deadline", time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano))
}
func consumerPublicationArguments() []string {
	args := publicationArguments()[:7]
	args[0] = "reconcile-development-publication"
	return append(args, "--operation-id", "op_0123456789abcdefghij")
}

func TestQueuedPublicationCommandsBindIndependentConfiguration(t *testing.T) {
	queue, err := loadDevelopmentPublication(queuedPublicationArguments(), mapEnvironment(validStrictAcceptanceEnvironment()))
	if err != nil || queue.deadline.IsZero() {
		t.Fatal(err)
	}
	consumer, err := loadDevelopmentPublication(consumerPublicationArguments(), mapEnvironment(validStrictAcceptanceEnvironment()))
	if err != nil || consumer.input.VerificationDigest != queue.input.VerificationDigest || consumer.input.ActorID != "" || consumer.input.CorrelationID != "" {
		t.Fatal("consumer configuration changed reviewed identity", err)
	}
	invalid := [][]string{append(consumerPublicationArguments(), "--actor", "replacement"), consumerPublicationArguments()[:7], queuedPublicationArguments()[:11], append(queuedPublicationArguments(), "--deadline", "2026-01-01T00:00:00Z")}
	for _, value := range []string{"", "op_other", "inv_0123456789abcdefghij", "../operation"} {
		args := consumerPublicationArguments()
		args[len(args)-1] = value
		invalid = append(invalid, args)
	}
	for _, args := range invalid {
		if _, err := loadDevelopmentPublication(args, mapEnvironment(validStrictAcceptanceEnvironment())); err == nil {
			t.Fatal("ambiguous queue or consumer command accepted", args)
		}
	}
}

type publicationConsumerStore struct {
	developmentPublicationStore
	operation   domain.Operation
	claimed     int
	completed   int
	retries     int
	verifyError bool
	reject      bool
	applied     persistence.DevelopmentPublicationInput
}

func (store *publicationConsumerStore) RequireDevelopmentPublication(context.Context, string, persistence.DevelopmentPublicationInput) error {
	if store.reject {
		return errors.New("private database detail")
	}
	return nil
}
func (store *publicationConsumerStore) GetOperation(context.Context, string, string) (domain.Operation, error) {
	return store.operation, nil
}
func (store *publicationConsumerStore) ClaimDevelopmentPublication(_ context.Context, operationID string, input persistence.DevelopmentPublicationInput, worker string, lease time.Duration) (*persistence.OperationClaim, error) {
	if operationID != store.operation.Metadata.ID || input.Target.RevisionID != store.operation.ResourceID || lease != 2*time.Minute {
		panic("consumer escaped exact scope or lease")
	}
	store.claimed++
	return &persistence.OperationClaim{Kind: store.operation.Kind, ID: operationID, IsolationDomainID: store.operation.Metadata.IsolationDomainID, ResourceID: store.operation.ResourceID, StateMachineVersion: 3, ObservedState: store.operation.ObservedState, Command: "repair", ActorID: "repair-operator", CorrelationID: "cor_0123456789abcdefghij", LeaseOwner: worker, DeadlineAt: time.Now().Add(time.Hour)}, nil
}
func (store *publicationConsumerStore) Advance(_ context.Context, _ persistence.OperationClaim, state string, _ map[string]any) error {
	store.operation.ObservedState = state
	return nil
}
func (store *publicationConsumerStore) CompleteDevelopmentPublication(ctx context.Context, _ persistence.OperationClaim, input persistence.DevelopmentPublicationInput, verify persistence.DevelopmentPublicationVerifier) error {
	store.completed++
	store.applied = input
	if _, err := verify(ctx); err != nil {
		return err
	}
	if store.verifyError {
		store.verifyError = false
		return persistence.ErrDevelopmentPublicationUnavailable
	}
	store.operation.ObservedState = "published"
	return nil
}
func (store *publicationConsumerStore) ScheduleRetry(context.Context, persistence.OperationClaim, string, string, time.Time) error {
	store.retries++
	return nil
}

func newPublicationConsumerFixture(t *testing.T) (developmentPublicationConfiguration, *publicationConsumerStore) {
	t.Helper()
	config, err := loadDevelopmentPublication(consumerPublicationArguments(), mapEnvironment(validStrictAcceptanceEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	operation := domain.Operation{Metadata: domain.ResourceMetadata{ID: config.operationID, IsolationDomainID: config.input.Target.IsolationDomainID}, Kind: persistence.OperationKindPublication, ResourceID: config.input.Target.RevisionID, ObservedState: "queued", StateMachineVersion: 3}
	return config, &publicationConsumerStore{operation: operation}
}
func TestPublicationConsumerRetriesWithCurrentActorAndReplaysTerminal(t *testing.T) {
	config, store := newPublicationConsumerFixture(t)
	store.verifyError = true
	var output bytes.Buffer
	calls := 0
	verify := func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
		calls++
		return persistence.DevelopmentPublicationEvidence{}, nil
	}
	if err := consumeDevelopmentPublication(context.Background(), store, config, "worker-a", verify, &output); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || store.retries != 1 || store.applied.ActorID != "repair-operator" || store.applied.CorrelationID != "cor_0123456789abcdefghij" {
		t.Fatal("retry or actor attribution lost")
	}
	var operation domain.Operation
	if json.Unmarshal(output.Bytes(), &operation) != nil || operation.ObservedState != "published" {
		t.Fatal("missing terminal receipt")
	}
	before := store.claimed
	output.Reset()
	if err := consumeDevelopmentPublication(context.Background(), store, config, "worker-b", verify, &output); err != nil || store.claimed != before || calls != 2 {
		t.Fatal("terminal replay acquired another lease or reverified", err)
	}
}
func TestPublicationConsumerFailsClosedBeforeVerification(t *testing.T) {
	for _, mode := range []string{"configuration", "scope", "version", "invocation", "failed", "cancelled", "context", "worker"} {
		t.Run(mode, func(t *testing.T) {
			config, store := newPublicationConsumerFixture(t)
			worker := "worker"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "configuration":
				store.reject = true
			case "scope":
				store.operation.ResourceID = "rev_99999999999999999999"
			case "version":
				store.operation.StateMachineVersion = 1
			case "invocation":
				store.operation.Kind = persistence.OperationKindInvocation
			case "failed", "cancelled":
				store.operation.ObservedState = mode
			case "context":
				cancel()
			case "worker":
				worker = ""
			}
			var output bytes.Buffer
			err := consumeDevelopmentPublication(ctx, store, config, worker, func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
				t.Fatal("invalid consumer reached verifier")
				return persistence.DevelopmentPublicationEvidence{}, nil
			}, &output)
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("invalid consumer succeeded or leaked", err)
			}
			if mode != "context" && store.claimed != 0 {
				t.Fatal("invalid consumer claimed work")
			}
		})
	}
}

type lostPublicationReceipt struct{}

func (lostPublicationReceipt) Write([]byte) (int, error) { return 0, nil }

func TestPublicationConsumerRecoversLostTerminalReceipt(t *testing.T) {
	config, store := newPublicationConsumerFixture(t)
	calls := 0
	verify := func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
		calls++
		return persistence.DevelopmentPublicationEvidence{}, nil
	}
	if err := consumeDevelopmentPublication(context.Background(), store, config, "worker-a", verify, lostPublicationReceipt{}); err == nil || store.operation.ObservedState != "published" {
		t.Fatal("lost receipt was not reported after publication", err)
	}
	before := store.claimed
	var output bytes.Buffer
	if err := consumeDevelopmentPublication(context.Background(), store, config, "replacement", verify, &output); err != nil || calls != 1 || store.claimed != before || output.Len() == 0 {
		t.Fatal("lost acknowledgement repeated publication", err)
	}
}

func (store *publicationConsumerStore) RequireAuthorizedDevelopmentPublication(ctx context.Context, id string, input persistence.DevelopmentPublicationInput) error {
	if store.operation.StateMachineVersion != 4 {
		return persistence.ErrDevelopmentPublicationUnavailable
	}
	return store.RequireDevelopmentPublication(ctx, id, input)
}
func (store *publicationConsumerStore) ClaimAuthorizedDevelopmentPublication(ctx context.Context, id string, input persistence.DevelopmentPublicationInput, worker string, lease time.Duration) (*persistence.OperationClaim, error) {
	claim, err := store.ClaimDevelopmentPublication(ctx, id, input, worker, lease)
	if claim != nil {
		claim.StateMachineVersion = 4
		claim.FencingToken = 2
	}
	return claim, err
}
func (store *publicationConsumerStore) CompleteAuthorizedDevelopmentPublication(ctx context.Context, claim persistence.OperationClaim, input persistence.DevelopmentPublicationInput, verify persistence.DevelopmentPublicationVerifier, authorize persistence.PublicationAuthorization) error {
	if claim.StateMachineVersion != 4 || authorize == nil {
		return persistence.ErrDevelopmentPublicationUnavailable
	}
	if err := authorize(ctx, authz.PublicationRequest{ActorID: claim.ActorID, CorrelationID: claim.CorrelationID, Phase: "effect", FencingToken: claim.FencingToken}); err != nil {
		return err
	}
	return store.CompleteDevelopmentPublication(ctx, claim, input, verify)
}
func TestAuthorizedPublicationConsumerRequiresAuthorityAndExactVersion(t *testing.T) {
	config, store := newPublicationConsumerFixture(t)
	config.command = "reconcile-authorized-publication"
	store.operation.StateMachineVersion = 4
	verify := func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
		return persistence.DevelopmentPublicationEvidence{}, nil
	}
	var output bytes.Buffer
	if err := consumeDevelopmentPublication(context.Background(), store, config, "worker", verify, &output); err == nil {
		t.Fatal("operator consumer selected authorized operation")
	}
	if err := consumeAuthorizedDevelopmentPublication(context.Background(), store, config, "worker", verify, nil, &output); err == nil {
		t.Fatal("missing authorizer admitted")
	}
	calls := 0
	authorize := func(_ context.Context, request authz.PublicationRequest) error {
		calls++
		if request.ActorID != "repair-operator" || request.CorrelationID != "cor_0123456789abcdefghij" || request.Phase != "effect" || request.FencingToken != 2 {
			t.Fatal("authority lost claim binding")
		}
		return nil
	}
	if err := consumeAuthorizedDevelopmentPublication(context.Background(), store, config, "worker", verify, authorize, &output); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || store.completed != 1 || store.operation.ObservedState != "published" {
		t.Fatal("authorized completion lost")
	}
	if err := consumeAuthorizedDevelopmentPublication(context.Background(), store, config, "replacement", verify, authorize, &output); err != nil || calls != 1 {
		t.Fatal("terminal replay reevaluated", err)
	}
	store.operation.StateMachineVersion = 3
	if err := consumeAuthorizedDevelopmentPublication(context.Background(), store, config, "worker", verify, authorize, &output); err == nil {
		t.Fatal("authorized consumer selected operator operation")
	}
}
