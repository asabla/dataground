package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
)

const maximumInvocationRuntimeArtifacts = 32

var invocationRuntimeArtifactIDPattern = regexp.MustCompile("^[a-z][a-z0-9-]{0,62}$")

var (
	ErrInvocationRuntimeDenied              = errors.New("invocation runtime denied")
	ErrInvocationRuntimeTargetMismatch      = errors.New("invocation runtime target does not match durable effect")
	ErrInvocationRuntimeExecutionNotReady   = errors.New("invocation runtime execution is not ready")
	ErrInvocationRuntimeExecutionTerminated = errors.New("invocation runtime execution terminated before the turn")
	ErrInvocationRuntimeExecutionUnknown    = errors.New("invocation runtime cannot resolve the admitted execution")
)

type InvocationRuntimeStore interface {
	GetClaimedInvocationRuntimeTarget(context.Context, persistence.OperationClaim) (persistence.InvocationRuntimeTarget, error)
	GetInvocationRuntimeAttempt(context.Context, string, string) (persistence.InvocationRuntimeAttempt, error)
	BeginInvocationRuntimeAttempt(context.Context, persistence.OperationClaim, persistence.EffectRecord) (persistence.InvocationRuntimeAttempt, error)
	CompleteInvocationRuntimeAttempt(context.Context, persistence.OperationClaim, persistence.EffectRecord, map[string]any) (persistence.InvocationRuntimeAttempt, error)
	FailInvocationRuntimeAttempt(context.Context, persistence.OperationClaim, persistence.EffectRecord, map[string]any) (persistence.InvocationRuntimeAttempt, error)
	RecordInvocationRuntimeEvent(context.Context, persistence.OperationClaim, persistence.InvocationRuntimeEvent) (domain.EventEnvelope, error)
	RenewLease(context.Context, persistence.OperationClaim, time.Duration) (persistence.OperationClaim, error)
}

type InvocationRuntimeApprovalStore interface {
	RecordInvocationRuntimeApprovalRequest(
		context.Context,
		persistence.OperationClaim,
		persistence.EffectRecord,
		persistence.InvocationRuntimeTarget,
		persistence.InvocationRuntimeApprovalRequest,
	) (persistence.InvocationRuntimeApproval, error)
	GetInvocationRuntimeApproval(
		context.Context,
		string,
		string,
	) (persistence.InvocationRuntimeApproval, error)
	BeginInvocationRuntimeApprovalDelivery(
		context.Context,
		persistence.OperationClaim,
		persistence.EffectRecord,
		string,
		string,
	) (persistence.InvocationRuntimeApproval, error)
	CompleteInvocationRuntimeApprovalDelivery(
		context.Context,
		persistence.OperationClaim,
		persistence.EffectRecord,
		string,
	) (persistence.InvocationRuntimeApproval, error)
}

type InvocationRuntimeAuthorizer interface {
	AuthorizeInvocationRuntime(
		context.Context,
		persistence.InvocationRuntimeTarget,
		dgruntime.StartRequest,
	) error
}

type InvocationRuntimeRequestBuilder interface {
	BuildInvocationRuntimeRequest(persistence.InvocationRuntimeTarget) (dgruntime.StartRequest, error)
}

type InvocationRuntimeRequestBuilderFunc func(persistence.InvocationRuntimeTarget) (dgruntime.StartRequest, error)

func (builder InvocationRuntimeRequestBuilderFunc) BuildInvocationRuntimeRequest(
	target persistence.InvocationRuntimeTarget,
) (dgruntime.StartRequest, error) {
	return builder(target)
}

type invocationRuntimeProvider interface {
	Observe(context.Context, execution.ExecutionRef) (execution.Observation, error)
	StartRuntime(context.Context, execution.ExecutionRef) (execution.RuntimeSession, error)
	Export(context.Context, execution.ExportRequest) (execution.ExportResult, error)
}

type InvocationRuntimeArtifactFinalizer interface {
	Finalize(context.Context, artifact.Finalization) (artifact.Record, error)
}

type InvocationRuntimeAdapter interface {
	Start(context.Context, dgruntime.StartRequest) (dgruntime.Turn, error)
	Close() error
}

type InvocationRuntimeAdapterFactory interface {
	New(execution.RuntimeSession) (InvocationRuntimeAdapter, error)
}

type CodexInvocationRuntimeAdapterFactory struct{}

func (CodexInvocationRuntimeAdapterFactory) New(
	session execution.RuntimeSession,
) (InvocationRuntimeAdapter, error) {
	return codex.New(session)
}

type InvocationRuntimeDriverConfig struct {
	LeaseDuration      time.Duration
	RenewInterval      time.Duration
	Readiness          func(context.Context) error
	ApprovalStore      InvocationRuntimeApprovalStore
	ApprovalAuthorizer InvocationApprovalAuthorizer
}

// InvocationRuntimeDriver is the claim-bound bridge from one durable runtime
// effect to one native agent turn. It is deliberately opt-in and does not
// change the default worker composition.
type InvocationRuntimeDriver struct {
	store              InvocationRuntimeStore
	authorizer         InvocationRuntimeAuthorizer
	requests           InvocationRuntimeRequestBuilder
	executions         executionByOperationSource
	provider           invocationRuntimeProvider
	adapters           InvocationRuntimeAdapterFactory
	artifacts          InvocationRuntimeArtifactFinalizer
	leaseDuration      time.Duration
	renewInterval      time.Duration
	readiness          func(context.Context) error
	approvalStore      InvocationRuntimeApprovalStore
	approvalAuthorizer InvocationApprovalAuthorizer
}

func NewInvocationRuntimeDriver(
	store InvocationRuntimeStore,
	authorizer InvocationRuntimeAuthorizer,
	requests InvocationRuntimeRequestBuilder,
	executions executionByOperationSource,
	provider invocationRuntimeProvider,
	adapters InvocationRuntimeAdapterFactory,
	artifacts InvocationRuntimeArtifactFinalizer,
	config InvocationRuntimeDriverConfig,
) (*InvocationRuntimeDriver, error) {
	if governedInvocationDependencyMissing(store) ||
		governedInvocationDependencyMissing(authorizer) ||
		governedInvocationDependencyMissing(requests) ||
		governedInvocationDependencyMissing(executions) ||
		governedInvocationDependencyMissing(provider) ||
		governedInvocationDependencyMissing(adapters) ||
		governedInvocationDependencyMissing(artifacts) {
		return nil, errors.New("invocation runtime driver dependencies are required")
	}
	if governedInvocationDependencyMissing(config.ApprovalStore) !=
		governedInvocationDependencyMissing(config.ApprovalAuthorizer) {
		return nil, errors.New("runtime approval store and authorizer must be configured together")
	}
	if config.LeaseDuration <= 0 ||
		config.RenewInterval <= 0 ||
		config.RenewInterval >= config.LeaseDuration {
		return nil, errors.New("runtime lease duration and shorter renewal interval are required")
	}
	return &InvocationRuntimeDriver{
		store: store, authorizer: authorizer, requests: requests, executions: executions,
		provider: provider, adapters: adapters, artifacts: artifacts, leaseDuration: config.LeaseDuration,
		renewInterval: config.RenewInterval, readiness: config.Readiness,
		approvalStore: config.ApprovalStore, approvalAuthorizer: config.ApprovalAuthorizer,
	}, nil
}

func (driver *InvocationRuntimeDriver) Observe(
	context.Context,
	persistence.EffectRecord,
) (map[string]any, bool, error) {
	return nil, false, ErrEffectClaimRequired
}

func (driver *InvocationRuntimeDriver) Apply(
	context.Context,
	persistence.EffectRecord,
) (map[string]any, error) {
	return nil, ErrEffectClaimRequired
}

func (driver *InvocationRuntimeDriver) ObserveClaimed(
	ctx context.Context,
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
) (map[string]any, bool, error) {
	if !invocationRuntimeEffectMatchesClaim(claim, effect) {
		return nil, false, errors.Join(ErrEffectInvalid, ErrInvocationRuntimeTargetMismatch)
	}
	if err := driver.ready(ctx); err != nil {
		return nil, false, err
	}
	attempt, err := driver.store.GetInvocationRuntimeAttempt(
		ctx,
		effect.IsolationDomainID,
		effect.OperationID,
	)
	if errors.Is(err, persistence.ErrInvocationRuntimeAttemptMissing) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if attempt.EffectID != effect.EffectID {
		return nil, false, errors.Join(
			ErrEffectInvalid,
			persistence.ErrInvocationRuntimeAttemptConflict,
		)
	}
	switch attempt.Status {
	case "succeeded":
		return attempt.Result, true, nil
	case "failed":
		return attempt.Result, false, errors.Join(ErrEffectTerminal, dgruntime.ErrTurnFailed)
	case "reserved":
		return nil, false, errors.Join(
			ErrAmbiguousEffect,
			persistence.ErrInvocationRuntimeAttemptAmbiguous,
		)
	default:
		return nil, false, persistence.ErrInvocationRuntimeAttemptConflict
	}
}

func (driver *InvocationRuntimeDriver) ApplyClaimed(
	ctx context.Context,
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
) (map[string]any, error) {
	if !invocationRuntimeEffectMatchesClaim(claim, effect) {
		return nil, errors.Join(ErrEffectInvalid, ErrInvocationRuntimeTargetMismatch)
	}
	if err := driver.ready(ctx); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithDeadline(ctx, claim.DeadlineAt)
	defer cancel()
	target, err := driver.store.GetClaimedInvocationRuntimeTarget(runCtx, claim)
	if err != nil {
		if errors.Is(err, persistence.ErrInvocationRuntimeTargetMissing) {
			return nil, errors.Join(ErrEffectInvalid, err)
		}
		return nil, err
	}
	if !invocationRuntimeTargetMatchesClaim(target, claim) {
		return nil, errors.Join(ErrEffectInvalid, ErrInvocationRuntimeTargetMismatch)
	}
	value, err := driver.executions.GetExecutionByOperation(
		runCtx,
		target.IsolationDomainID,
		target.OperationID,
	)
	if errors.Is(err, execution.ErrExecutionMissing) {
		return nil, errors.Join(ErrAmbiguousEffect, ErrInvocationRuntimeExecutionUnknown)
	}
	if err != nil {
		return nil, err
	}
	if value.IsolationDomainID != target.IsolationDomainID || value.ID == "" {
		return nil, errors.Join(ErrEffectInvalid, ErrInvocationRuntimeTargetMismatch)
	}
	ref := execution.ExecutionRef{IsolationDomainID: target.IsolationDomainID, ID: value.ID}
	observation, err := driver.provider.Observe(runCtx, ref)
	if err != nil {
		return nil, err
	}
	if observation.IsolationDomainID != ref.IsolationDomainID ||
		observation.ExecutionID != ref.ID ||
		observation.State == "" {
		return nil, errors.Join(ErrAmbiguousEffect, ErrInvocationRuntimeTargetMismatch)
	}
	switch observation.State {
	case "ready":
	case "terminated":
		return nil, errors.Join(ErrEffectTerminal, ErrInvocationRuntimeExecutionTerminated)
	default:
		return nil, ErrInvocationRuntimeExecutionNotReady
	}
	request, err := driver.requests.BuildInvocationRuntimeRequest(target)
	if err != nil {
		return nil, errors.Join(ErrEffectInvalid, err)
	}
	if err := driver.validateInvocationRuntimeRequest(request); err != nil {
		return nil, errors.Join(ErrEffectInvalid, err)
	}
	output, err := newInvocationRuntimeOutput(request.OutputSchema)
	if err != nil {
		return nil, errors.Join(ErrEffectInvalid, err)
	}
	claim, err = driver.store.RenewLease(runCtx, claim, driver.leaseDuration)
	if err != nil {
		return nil, err
	}
	if err := driver.authorizer.AuthorizeInvocationRuntime(runCtx, target, request); err != nil {
		if errors.Is(err, ErrInvocationRuntimeDenied) {
			return nil, errors.Join(ErrEffectDenied, err)
		}
		return nil, err
	}
	if err := driver.ready(runCtx); err != nil {
		return nil, err
	}
	session, err := driver.provider.StartRuntime(runCtx, ref)
	if err != nil {
		return nil, err
	}
	adapter, err := driver.adapters.New(session)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	defer adapter.Close()
	if _, err := driver.store.BeginInvocationRuntimeAttempt(runCtx, claim, effect); err != nil {
		return nil, err
	}
	turn, err := adapter.Start(runCtx, request)
	if err != nil {
		return nil, errors.Join(ErrAmbiguousEffect, err)
	}
	defer turn.Close()
	return driver.runTurn(
		runCtx,
		claim,
		effect,
		turn,
		output,
		target,
		ref,
		request.Artifacts,
	)
}

func (driver *InvocationRuntimeDriver) runTurn(
	ctx context.Context,
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
	turn dgruntime.Turn,
	output *invocationRuntimeOutput,
	target persistence.InvocationRuntimeTarget,
	ref execution.ExecutionRef,
	declarations []dgruntime.ArtifactDeclaration,
) (map[string]any, error) {
	runCtx, cancel := context.WithDeadline(ctx, claim.DeadlineAt)
	defer cancel()
	waited := make(chan error, 1)
	go func() { waited <- turn.Wait(runCtx) }()
	ticker := time.NewTicker(driver.renewInterval)
	defer ticker.Stop()
	events := turn.Events()
	for {
		select {
		case event, open := <-events:
			if !open {
				events = nil
				continue
			}
			renewed, err := driver.recordRuntimeEvent(
				runCtx, claim, effect, target, turn, event,
			)
			if err != nil {
				_ = turn.Interrupt(context.Background())
				return nil, errors.Join(ErrAmbiguousEffect, err)
			}
			claim = renewed
			output.Observe(event)
		case waitErr := <-waited:
			if err := driver.ready(runCtx); err != nil {
				return nil, errors.Join(ErrAmbiguousEffect, err)
			}
			renewed, err := driver.drainRuntimeEvents(
				runCtx, claim, effect, target, turn, events, output,
			)
			if err != nil {
				return nil, errors.Join(ErrAmbiguousEffect, err)
			}
			claim = renewed
			if waitErr != nil {
				if errors.Is(waitErr, dgruntime.ErrTurnFailed) {
					result := map[string]any{"code": "RUNTIME_TURN_FAILED", "status": "failed"}
					if _, err := driver.store.FailInvocationRuntimeAttempt(
						runCtx,
						claim,
						effect,
						result,
					); err != nil {
						return nil, errors.Join(ErrAmbiguousEffect, waitErr, err)
					}
					return nil, errors.Join(ErrEffectTerminal, waitErr)
				}
				return nil, errors.Join(ErrAmbiguousEffect, waitErr)
			}
			result, resultErr := output.Result()
			if resultErr != nil {
				failure := map[string]any{"code": "RUNTIME_OUTPUT_INVALID", "status": "failed"}
				if _, err := driver.store.FailInvocationRuntimeAttempt(
					runCtx,
					claim,
					effect,
					failure,
				); err != nil {
					return nil, errors.Join(ErrAmbiguousEffect, resultErr, err)
				}
				return nil, errors.Join(ErrEffectTerminal, resultErr)
			}
			renewedClaim, publishErr := driver.publishInvocationArtifacts(
				runCtx,
				claim,
				effect,
				target,
				ref,
				declarations,
			)
			if publishErr != nil {
				return nil, errors.Join(ErrAmbiguousEffect, publishErr)
			}
			claim = renewedClaim
			if err := driver.ready(runCtx); err != nil {
				return nil, errors.Join(ErrAmbiguousEffect, err)
			}
			if _, err := driver.store.CompleteInvocationRuntimeAttempt(
				runCtx,
				claim,
				effect,
				result,
			); err != nil {
				return nil, errors.Join(ErrAmbiguousEffect, err)
			}
			return result, nil
		case <-ticker.C:
			if err := driver.ready(runCtx); err != nil {
				_ = turn.Interrupt(context.Background())
				return nil, errors.Join(ErrAmbiguousEffect, err)
			}
			renewed, err := driver.store.RenewLease(runCtx, claim, driver.leaseDuration)
			if err != nil {
				_ = turn.Interrupt(context.Background())
				return nil, errors.Join(ErrAmbiguousEffect, err)
			}
			claim = renewed
		case <-runCtx.Done():
			_ = turn.Interrupt(context.Background())
			return nil, errors.Join(ErrAmbiguousEffect, runCtx.Err())
		}
	}
}

func (driver *InvocationRuntimeDriver) publishInvocationArtifacts(
	ctx context.Context,
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
	target persistence.InvocationRuntimeTarget,
	ref execution.ExecutionRef,
	declarations []dgruntime.ArtifactDeclaration,
) (persistence.OperationClaim, error) {
	for _, declaration := range declarations {
		if err := driver.ready(ctx); err != nil {
			return claim, err
		}
		renewed, err := driver.store.RenewLease(ctx, claim, driver.leaseDuration)
		if err != nil {
			return claim, err
		}
		claim = renewed
		exported, err := driver.provider.Export(ctx, execution.ExportRequest{
			IsolationDomainID: ref.IsolationDomainID,
			ExecutionID:       ref.ID,
			SandboxPath:       declaration.SandboxPath,
		})
		if err != nil {
			return claim, err
		}
		if exported.IsolationDomainID != ref.IsolationDomainID ||
			exported.ExecutionID != ref.ID {
			return claim, ErrInvocationRuntimeTargetMismatch
		}
		content := exported.Content
		digest := sha256.Sum256(content)
		record := artifact.Record{
			SchemaVersion:     artifact.InvocationArtifactSchemaV1,
			IsolationDomainID: target.IsolationDomainID,
			ID: identity.Derived(
				"art",
				target.IsolationDomainID+":"+target.InvocationID+":"+declaration.ID,
			),
			InvocationID: target.InvocationID,
			OperationID:  target.OperationID,
			EffectID:     effect.EffectID,
			Name:         declaration.Name,
			Kind:         declaration.Kind,
			MediaType:    declaration.MediaType,
			SizeBytes:    int64(len(content)),
			Digest:       "sha256:" + hex.EncodeToString(digest[:]),
			Sensitive:    true,
		}
		if _, err := driver.artifacts.Finalize(ctx, artifact.Finalization{
			Binding: artifact.Binding{
				Record:              record,
				ActorID:             claim.ActorID,
				CorrelationID:       claim.CorrelationID,
				LeaseOwner:          claim.LeaseOwner,
				FencingToken:        claim.FencingToken,
				StateMachineVersion: claim.StateMachineVersion,
			},
			Content: content,
		}); err != nil {
			return claim, err
		}
	}
	return claim, nil
}

func (driver *InvocationRuntimeDriver) ready(ctx context.Context) error {
	if driver.readiness == nil {
		return nil
	}
	return driver.readiness(ctx)
}

func (driver *InvocationRuntimeDriver) drainRuntimeEvents(
	ctx context.Context,
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
	target persistence.InvocationRuntimeTarget,
	turn dgruntime.Turn,
	events <-chan dgruntime.Event,
	output *invocationRuntimeOutput,
) (persistence.OperationClaim, error) {
	for events != nil {
		select {
		case event, open := <-events:
			if !open {
				return claim, nil
			}
			renewed, err := driver.recordRuntimeEvent(
				ctx, claim, effect, target, turn, event,
			)
			if err != nil {
				return claim, err
			}
			claim = renewed
			output.Observe(event)
		default:
			return claim, nil
		}
	}
	return claim, nil
}

func (driver *InvocationRuntimeDriver) recordRuntimeEvent(
	ctx context.Context,
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
	target persistence.InvocationRuntimeTarget,
	turn dgruntime.Turn,
	event dgruntime.Event,
) (persistence.OperationClaim, error) {
	if event.Type != "interaction.approval.requested" {
		_, err := driver.store.RecordInvocationRuntimeEvent(ctx, claim, persistence.InvocationRuntimeEvent{
			SourceSequence: event.Sequence,
			Type:           event.Type,
			Payload:        event.Payload,
		})
		return claim, err
	}
	if governedInvocationDependencyMissing(driver.approvalStore) ||
		governedInvocationDependencyMissing(driver.approvalAuthorizer) {
		return claim, ErrInvocationApprovalUnavailable
	}
	adapterApprovalID, idOK := event.Payload["approvalId"].(string)
	requestedAction, actionOK := event.Payload["action"].(string)
	if !idOK || adapterApprovalID == "" || !actionOK || len(event.Payload) != 2 {
		return claim, ErrInvocationApprovalInvalid
	}
	approval, err := driver.approvalStore.RecordInvocationRuntimeApprovalRequest(
		ctx, claim, effect, target, persistence.InvocationRuntimeApprovalRequest{
			SourceSequence: event.Sequence, RequestedAction: requestedAction,
		},
	)
	if err != nil {
		return claim, err
	}
	// The durable approval store atomically publishes the sanitized platform
	// event with the pending record. The adapter-local identifier stays only
	// in this stack frame for the eventual single-use delivery.
	ticker := time.NewTicker(driver.renewInterval)
	defer ticker.Stop()
	for {
		approval, err = driver.approvalStore.GetInvocationRuntimeApproval(
			ctx, approval.IsolationDomainID, approval.ID,
		)
		if err != nil {
			return claim, err
		}
		switch approval.State {
		case "pending":
		case "resolved":
			effectiveDecision := approval.Decision
			authorizationErr := driver.approvalAuthorizer.AuthorizeInvocationApproval(
				ctx, approval, InvocationApprovalPhaseEffect,
			)
			if errors.Is(authorizationErr, ErrInvocationApprovalDenied) {
				effectiveDecision = string(dgruntime.ApprovalDeny)
			} else if authorizationErr != nil {
				return claim, authorizationErr
			}
			if _, err := driver.approvalStore.BeginInvocationRuntimeApprovalDelivery(
				ctx, claim, effect, approval.ID, effectiveDecision,
			); err != nil {
				return claim, err
			}
			if err := turn.ResolveApproval(
				ctx,
				adapterApprovalID,
				dgruntime.ApprovalDecision(effectiveDecision),
			); err != nil {
				return claim, err
			}
			if _, err := driver.approvalStore.CompleteInvocationRuntimeApprovalDelivery(
				ctx, claim, effect, approval.ID,
			); err != nil {
				return claim, err
			}
			return claim, nil
		case "delivering":
			return claim, persistence.ErrInvocationRuntimeApprovalDeliveryAmbiguous
		case "delivered":
			return claim, persistence.ErrInvocationRuntimeApprovalConflict
		default:
			return claim, ErrInvocationApprovalInvalid
		}
		select {
		case <-ctx.Done():
			return claim, ctx.Err()
		case <-ticker.C:
			renewed, err := driver.store.RenewLease(ctx, claim, driver.leaseDuration)
			if err != nil {
				return claim, err
			}
			claim = renewed
		}
	}
}

func invocationRuntimeEffectMatchesClaim(
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
) bool {
	return effectMatchesClaim(effect, claim) &&
		effect.Phase == "run-invocation" &&
		effect.EffectID != "" &&
		(effect.Status == "prepared" || effect.Status == "failed" || effect.Status == "unknown")
}

func invocationRuntimeTargetMatchesClaim(
	target persistence.InvocationRuntimeTarget,
	claim persistence.OperationClaim,
) bool {
	return target.IsolationDomainID == claim.IsolationDomainID &&
		target.OperationID == claim.ID &&
		target.InvocationID == claim.ResourceID &&
		target.ActorID == claim.ActorID &&
		target.CorrelationID == claim.CorrelationID &&
		target.ServiceID != "" &&
		target.RevisionID != "" &&
		target.RuntimeProfile != "" &&
		target.Input != nil &&
		target.StateMachineVersion == claim.StateMachineVersion
}

func (driver *InvocationRuntimeDriver) validateInvocationRuntimeRequest(
	request dgruntime.StartRequest,
) error {
	if err := validateInvocationRuntimeRequest(request); err != nil {
		return err
	}
	if request.ApprovalMode == dgruntime.ApprovalInteractive &&
		(governedInvocationDependencyMissing(driver.approvalStore) ||
			governedInvocationDependencyMissing(driver.approvalAuthorizer)) {
		return errors.New("interactive invocation runtime approvals require durable mediation")
	}
	return nil
}

func validateInvocationRuntimeRequest(
	request dgruntime.StartRequest,
) error {
	if request.Prompt == "" {
		return errors.New("invocation runtime prompt is required")
	}
	if request.ApprovalMode != "" &&
		request.ApprovalMode != dgruntime.ApprovalLocked &&
		request.ApprovalMode != dgruntime.ApprovalInteractive {
		return errors.New("invocation runtime approval mode is invalid")
	}
	if request.SandboxMode != "" &&
		request.SandboxMode != dgruntime.SandboxReadOnly &&
		request.SandboxMode != dgruntime.SandboxWorkspaceWrite {
		return errors.New("invocation runtime sandbox mode is invalid")
	}
	if request.WorkingDir != "" &&
		(!path.IsAbs(request.WorkingDir) ||
			path.Clean(request.WorkingDir) != request.WorkingDir ||
			strings.ContainsRune(request.WorkingDir, '\x00')) {
		return errors.New("invocation runtime working directory must be a clean absolute sandbox path")
	}
	if strings.ContainsRune(request.Model, '\x00') {
		return errors.New("invocation runtime model is invalid")
	}
	if err := validateInvocationRuntimeArtifacts(request.Artifacts); err != nil {
		return err
	}
	if len(request.Artifacts) > 0 && request.SandboxMode != dgruntime.SandboxWorkspaceWrite {
		return errors.New("invocation runtime artifacts require workspace-write sandboxing")
	}
	return nil
}

func validateInvocationRuntimeArtifacts(artifacts []dgruntime.ArtifactDeclaration) error {
	if len(artifacts) > maximumInvocationRuntimeArtifacts {
		return errors.New("invocation runtime artifact declarations exceed the limit")
	}
	seenIDs := make(map[string]struct{}, len(artifacts))
	seenPaths := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		if !invocationRuntimeArtifactIDPattern.MatchString(artifact.ID) ||
			!validInvocationRuntimeArtifactText(artifact.Name, 255) ||
			!validInvocationRuntimeArtifactText(artifact.MediaType, 255) ||
			!validInvocationRuntimeArtifactKind(artifact.Kind) ||
			!path.IsAbs(artifact.SandboxPath) ||
			path.Clean(artifact.SandboxPath) != artifact.SandboxPath ||
			strings.ContainsRune(artifact.SandboxPath, '\x00') {
			return errors.New("invocation runtime artifact declaration is invalid")
		}
		if _, found := seenIDs[artifact.ID]; found {
			return errors.New("invocation runtime artifact identifiers must be unique")
		}
		if _, found := seenPaths[artifact.SandboxPath]; found {
			return errors.New("invocation runtime artifact paths must be unique")
		}
		seenIDs[artifact.ID] = struct{}{}
		seenPaths[artifact.SandboxPath] = struct{}{}
	}
	return nil
}

func validInvocationRuntimeArtifactText(value string, maximumBytes int) bool {
	return value != "" &&
		len(value) <= maximumBytes &&
		utf8.ValidString(value) &&
		strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func validInvocationRuntimeArtifactKind(kind string) bool {
	switch kind {
	case "file", "structured-output", "event-payload", "log", "other":
		return true
	default:
		return false
	}
}

var _ EffectDriver = (*InvocationRuntimeDriver)(nil)
var _ ClaimedEffectDriver = (*InvocationRuntimeDriver)(nil)
