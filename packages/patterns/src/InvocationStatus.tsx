import { Button, StatusBadge, type StatusTone } from "@dataground/ui";
import { useId } from "react";

export interface InvocationStatusMetadata {
  createdAt: string;
  createdBy: string;
  generation: number;
  id: string;
  isolationDomainId: string;
  updatedAt: string;
  version: number;
}

export interface InvocationDomainError {
  code: string;
  correlationId: string;
  message: string;
  retryable: boolean;
}

export interface InvocationStatusResource {
  alias: string;
  artifactIds: readonly string[];
  completedAt?: string;
  correlationId: string;
  error?: InvocationDomainError;
  metadata: InvocationStatusMetadata;
  operationId: string;
  revisionId: string;
  serviceId: string;
  state: string;
  usage?: {
    inputTokens: number;
    outputTokens: number;
    totalTokens: number;
  };
}

export interface InvocationOperationResource {
  attempt: number;
  command: string;
  correlationId: string;
  deadlineAt?: string;
  desiredState: string;
  dueAt?: string;
  errorClassification?: string;
  error?: InvocationDomainError;
  kind: string;
  metadata: InvocationStatusMetadata;
  observedState: string;
  stateMachineVersion: number;
}

export interface InvocationStatusReference {
  invocationId: string;
  isolationDomainId: string;
}

export interface InvocationStatusError {
  correlationId?: string;
  message: string;
  retryable: boolean;
}

export interface InvocationStatusProps {
  canCancel: boolean;
  cancellationConfirmationVisible?: boolean;
  cancellationRecovery?: boolean;
  disabledReason?: string;
  error?: InvocationStatusError;
  invocation?: InvocationStatusResource;
  isCancelling?: boolean;
  isLoading?: boolean;
  onConfirmCancellation?: () => void;
  onDismissCancellation?: () => void;
  onInspectArtifact?: (artifactId: string) => void;
  onRefresh?: () => void;
  onRequestCancellation?: () => void;
  operation?: InvocationOperationResource;
  reference: InvocationStatusReference;
}

interface StatePresentation {
  label: string;
  tone: StatusTone;
}

const statePresentations: Record<string, StatePresentation> = {
  accepted: { label: "Invocation accepted", tone: "active" },
  cancelled: { label: "Invocation cancelled", tone: "warning" },
  cancelling: { label: "Cancellation in progress", tone: "warning" },
  failed: { label: "Invocation failed", tone: "critical" },
  running: { label: "Invocation running", tone: "active" },
  succeeded: { label: "Invocation succeeded", tone: "success" },
  unknown: { label: "Invocation state unknown", tone: "neutral" },
  waiting: { label: "Invocation waiting", tone: "waiting" },
};

const cancellableStates = new Set(["accepted", "running", "waiting"]);
const maximumVisibleArtifacts = 64;

function displayText(value: string, maximum = 255): string {
  const normalized = Array.from(value, (character) => {
    const codePoint = character.codePointAt(0) ?? 0;
    return codePoint <= 8 ||
      codePoint === 11 ||
      codePoint === 12 ||
      (codePoint >= 14 && codePoint <= 31) ||
      (codePoint >= 127 && codePoint <= 159)
      ? "�"
      : character;
  })
    .join("")
    .replaceAll(/\s+/gu, " ")
    .trim();
  return normalized.length > maximum ? `${normalized.slice(0, maximum)}…` : normalized;
}

function statePresentation(state: string): StatePresentation {
  return (
    statePresentations[state] ?? {
      label: `Unknown state: ${displayText(state, 64)}`,
      tone: "neutral",
    }
  );
}

function operationPresentation(operation: InvocationOperationResource): StatePresentation {
  if (operation.observedState === "failed") {
    return { label: "Operation failed", tone: "critical" };
  }
  if (
    operation.desiredState === operation.observedState &&
    (operation.observedState === "succeeded" || operation.observedState === "cancelled")
  ) {
    return { label: "Desired state observed", tone: "success" };
  }
  if (operation.desiredState === "unknown" || operation.observedState === "unknown") {
    return { label: "Operation state unknown", tone: "neutral" };
  }
  return operation.desiredState === operation.observedState
    ? { label: "Operation state aligned", tone: "neutral" }
    : { label: "Reconciliation in progress", tone: "active" };
}

function formatInteger(value: number): string {
  return new Intl.NumberFormat("en-US").format(value);
}

export function isInvocationCancellable(state: string): boolean {
  return cancellableStates.has(state);
}

export function InvocationStatus({
  canCancel,
  cancellationConfirmationVisible = false,
  cancellationRecovery = false,
  disabledReason,
  error,
  invocation,
  isCancelling = false,
  isLoading = false,
  onConfirmCancellation,
  onDismissCancellation,
  onInspectArtifact,
  onRefresh,
  onRequestCancellation,
  operation,
  reference,
}: InvocationStatusProps) {
  const titleId = useId();
  const confirmationId = useId();
  const cancellable = invocation ? isInvocationCancellable(invocation.state) : false;
  const canRecoverCancellation = cancellationRecovery && error?.retryable;
  const presentation = isCancelling
    ? { label: "Submitting cancellation", tone: "warning" as const }
    : isLoading
      ? { label: invocation ? "Refreshing state" : "Loading invocation", tone: "active" as const }
      : error && invocation
        ? { label: "State degraded", tone: "warning" as const }
        : invocation
          ? statePresentation(invocation.state)
          : { label: "Invocation unavailable", tone: "critical" as const };
  const cancellationActionAvailable =
    invocation &&
    cancellable &&
    canCancel &&
    (error === undefined || canRecoverCancellation) &&
    !isLoading &&
    !isCancelling &&
    onRequestCancellation !== undefined &&
    onConfirmCancellation !== undefined &&
    onDismissCancellation !== undefined;
  const operationStatus = operation ? operationPresentation(operation) : undefined;
  const visibleArtifactIds = invocation?.artifactIds.slice(0, maximumVisibleArtifacts) ?? [];

  return (
    <section
      aria-busy={isLoading || isCancelling || undefined}
      aria-labelledby={titleId}
      className="dg-invocation-status"
    >
      <div className="dg-invocation-status__heading">
        <div>
          <p className="dg-invocation-status__eyebrow">Invocation control</p>
          <h2 id={titleId}>Invocation state</h2>
        </div>
        <StatusBadge tone={presentation.tone}>{presentation.label}</StatusBadge>
      </div>

      <dl className="dg-invocation-status__scope">
        <div>
          <dt>Isolation domain</dt>
          <dd>{displayText(reference.isolationDomainId, 64)}</dd>
        </div>
        <div>
          <dt>Invocation</dt>
          <dd>{displayText(reference.invocationId, 64)}</dd>
        </div>
      </dl>

      {error && (
        <div className="dg-invocation-status__error" role="alert">
          <strong>
            {invocation ? "Authoritative state not fully confirmed." : "Invocation unavailable."}
          </strong>{" "}
          {displayText(error.message, 512)}
          {error.correlationId && (
            <span>
              {" "}
              Correlation: <code>{displayText(error.correlationId, 128)}</code>
            </span>
          )}
          {canRecoverCancellation && (
            <p>The cancellation outcome is uncertain. Retrying uses the same request identifier.</p>
          )}
        </div>
      )}

      {!invocation && !error && (
        <p aria-live="polite" className="dg-invocation-status__empty">
          Retrieving authoritative invocation and operation state.
        </p>
      )}

      {invocation && (
        <>
          <dl className="dg-invocation-status__facts">
            <div>
              <dt>Observed invocation state</dt>
              <dd>{displayText(invocation.state, 64)}</dd>
            </div>
            <div>
              <dt>Service</dt>
              <dd>{displayText(invocation.serviceId, 64)}</dd>
            </div>
            <div>
              <dt>Revision</dt>
              <dd>{displayText(invocation.revisionId, 64)}</dd>
            </div>
            <div>
              <dt>Alias</dt>
              <dd>{displayText(invocation.alias, 63)}</dd>
            </div>
            <div>
              <dt>Generation / version</dt>
              <dd>
                {invocation.metadata.generation} / {invocation.metadata.version}
              </dd>
            </div>
            <div>
              <dt>Last observed</dt>
              <dd>
                <time dateTime={invocation.metadata.updatedAt}>
                  {invocation.metadata.updatedAt}
                </time>
              </dd>
            </div>
            <div>
              <dt>Operation</dt>
              <dd>{displayText(invocation.operationId, 64)}</dd>
            </div>
            <div>
              <dt>Correlation</dt>
              <dd>{displayText(invocation.correlationId, 128)}</dd>
            </div>
            <div>
              <dt>Artifacts</dt>
              <dd>{formatInteger(invocation.artifactIds.length)}</dd>
            </div>
            {invocation.completedAt && (
              <div>
                <dt>Completed</dt>
                <dd>
                  <time dateTime={invocation.completedAt}>{invocation.completedAt}</time>
                </dd>
              </div>
            )}
          </dl>

          {invocation.usage && (
            <dl className="dg-invocation-status__usage" aria-label="Invocation token usage">
              <div>
                <dt>Input tokens</dt>
                <dd>{formatInteger(invocation.usage.inputTokens)}</dd>
              </div>
              <div>
                <dt>Output tokens</dt>
                <dd>{formatInteger(invocation.usage.outputTokens)}</dd>
              </div>
              <div>
                <dt>Total tokens</dt>
                <dd>{formatInteger(invocation.usage.totalTokens)}</dd>
              </div>
            </dl>
          )}

          {visibleArtifactIds.length > 0 && (
            <section
              aria-labelledby={`${titleId}-artifacts`}
              className="dg-invocation-status__artifacts"
            >
              <div className="dg-invocation-status__artifacts-heading">
                <div>
                  <h3 id={`${titleId}-artifacts`}>Governed artifacts</h3>
                  <p>
                    Inspect platform metadata without exposing artifact content or runtime storage.
                  </p>
                </div>
                <StatusBadge tone="neutral">
                  {formatInteger(invocation.artifactIds.length)} recorded
                </StatusBadge>
              </div>
              <ul>
                {visibleArtifactIds.map((artifactId) => (
                  <li key={artifactId}>
                    <code>{displayText(artifactId, 64)}</code>
                    {onInspectArtifact && (
                      <Button
                        aria-label={`Inspect metadata for ${displayText(artifactId, 64)}`}
                        onPress={() => onInspectArtifact(artifactId)}
                        variant="quiet"
                      >
                        Inspect metadata
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
              {invocation.artifactIds.length > visibleArtifactIds.length && (
                <p className="dg-invocation-status__notice">
                  Only the first {formatInteger(maximumVisibleArtifacts)} artifact references are
                  shown. Use an authorized API client to inspect the remaining metadata.
                </p>
              )}
            </section>
          )}

          {invocation.error && (
            <div className="dg-invocation-status__domain-error" role="alert">
              <strong>Invocation failure: {displayText(invocation.error.code, 64)}.</strong>{" "}
              {displayText(invocation.error.message, 512)} Correlation:{" "}
              <code>{displayText(invocation.error.correlationId, 128)}</code>. The reported failure
              is {invocation.error.retryable ? "retryable" : "not retryable"}.
            </div>
          )}

          {operation ? (
            <section
              aria-labelledby={`${titleId}-operation`}
              className="dg-invocation-status__operation"
            >
              <div className="dg-invocation-status__operation-heading">
                <h3 id={`${titleId}-operation`}>Durable operation</h3>
                <StatusBadge tone={operationStatus?.tone}>{operationStatus?.label}</StatusBadge>
              </div>
              <dl className="dg-invocation-status__operation-facts">
                <div>
                  <dt>Kind</dt>
                  <dd>{displayText(operation.kind, 64)}</dd>
                </div>
                <div>
                  <dt>Command</dt>
                  <dd>{displayText(operation.command, 64)}</dd>
                </div>
                <div>
                  <dt>Desired state</dt>
                  <dd>{displayText(operation.desiredState, 64)}</dd>
                </div>
                <div>
                  <dt>Observed state</dt>
                  <dd>{displayText(operation.observedState, 64)}</dd>
                </div>
                <div>
                  <dt>Attempt</dt>
                  <dd>{formatInteger(operation.attempt)}</dd>
                </div>
                <div>
                  <dt>State machine version</dt>
                  <dd>{formatInteger(operation.stateMachineVersion)}</dd>
                </div>
                {operation.errorClassification && (
                  <div>
                    <dt>Error classification</dt>
                    <dd>{displayText(operation.errorClassification, 64)}</dd>
                  </div>
                )}
                {operation.dueAt && (
                  <div>
                    <dt>Next reconciliation</dt>
                    <dd>
                      <time dateTime={operation.dueAt}>{operation.dueAt}</time>
                    </dd>
                  </div>
                )}
                {operation.deadlineAt && (
                  <div>
                    <dt>Deadline</dt>
                    <dd>
                      <time dateTime={operation.deadlineAt}>{operation.deadlineAt}</time>
                    </dd>
                  </div>
                )}
              </dl>
              {operation.error && (
                <div className="dg-invocation-status__domain-error" role="alert">
                  <strong>Operation failure: {displayText(operation.error.code, 64)}.</strong>{" "}
                  {displayText(operation.error.message, 512)} Correlation:{" "}
                  <code>{displayText(operation.error.correlationId, 128)}</code>. The reported
                  failure is {operation.error.retryable ? "retryable" : "not retryable"}.
                </div>
              )}
            </section>
          ) : (
            <p className="dg-invocation-status__notice">
              Invocation state is available, but its durable operation has not been confirmed.
            </p>
          )}

          {!statePresentations[invocation.state] && (
            <p className="dg-invocation-status__notice">
              This invocation state is not understood by this Workbench version. Refresh or update
              the Workbench before attempting a lifecycle command.
            </p>
          )}

          {cancellable && !canCancel && (
            <p className="dg-invocation-status__observer">
              {disabledReason ??
                "This view can observe the invocation but has not been granted cancellation authority."}
            </p>
          )}

          {cancellationActionAvailable &&
            cancellationConfirmationVisible &&
            !canRecoverCancellation && (
              <fieldset className="dg-invocation-status__confirmation">
                <legend id={confirmationId}>Confirm invocation cancellation</legend>
                <p>
                  Cancellation is durable and cannot be withdrawn. DataGround may still need to
                  reconcile in-flight effects before the invocation becomes cancelled.
                </p>
                <div className="dg-invocation-status__confirmation-actions">
                  <Button
                    isDisabled={isCancelling}
                    onPress={onConfirmCancellation}
                    variant="danger"
                  >
                    {isCancelling ? "Submitting cancellation…" : "Confirm cancellation"}
                  </Button>
                  <Button isDisabled={isCancelling} onPress={onDismissCancellation} variant="quiet">
                    Keep running
                  </Button>
                </div>
              </fieldset>
            )}
        </>
      )}

      <div className="dg-invocation-status__actions">
        {cancellationActionAvailable && canRecoverCancellation && (
          <Button isDisabled={isCancelling} onPress={onConfirmCancellation} variant="danger">
            {isCancelling ? "Retrying cancellation…" : "Retry cancellation"}
          </Button>
        )}
        {cancellationActionAvailable &&
          !cancellationConfirmationVisible &&
          !canRecoverCancellation && (
            <Button onPress={onRequestCancellation} variant="danger">
              Request cancellation
            </Button>
          )}
        {onRefresh && (
          <Button isDisabled={isLoading || isCancelling} onPress={onRefresh} variant="secondary">
            {isLoading ? "Refreshing state…" : "Refresh state"}
          </Button>
        )}
      </div>
    </section>
  );
}
