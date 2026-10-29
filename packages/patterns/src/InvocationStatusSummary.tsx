import { StatusBadge } from "@dataground/ui";
import type { ReactNode } from "react";
import type { InvocationStatusProps } from "./InvocationStatus";
import {
  displayText,
  formatInteger,
  type StatePresentation,
  statePresentations,
} from "./invocationPresentation";
import { useCommandResultFocus } from "./useCommandResultFocus";

interface InvocationStatusSummaryProps extends InvocationStatusProps {
  titleId: string;
  presentation: StatePresentation;
  actions: ReactNode;
  confirmation: ReactNode;
  cancellable: boolean;
  canRecoverCancellation?: boolean;
}
export function InvocationStatusSummary({
  titleId,
  presentation,
  actions,
  confirmation,
  cancellable,
  canRecoverCancellation,
  error,
  invocation,
  operation,
  reference,
  isLoading,
  isCancelling,
  canCancel,
  disabledReason,
}: InvocationStatusSummaryProps) {
  const outcome = useCommandResultFocus(
    true,
    isLoading || isCancelling
      ? undefined
      : `${invocation?.state ?? "unavailable"}/${error?.message ?? ""}`,
  );
  return (
    <section
      className="dg-run-status"
      aria-labelledby={titleId}
      aria-busy={isLoading || isCancelling || undefined}
    >
      <div className="dg-run-status__heading" ref={outcome} tabIndex={-1}>
        <h2 id={titleId}>Run outcome</h2>
        <span role="status">
          <StatusBadge tone={presentation.tone}>{presentation.label}</StatusBadge>
        </span>
        {actions}
      </div>
      {error && (
        <div role="alert" className="dg-invocation-status__error">
          <strong>State not fully confirmed. </strong>
          {displayText(error.message, 512)}
          {canRecoverCancellation && (
            <p>The cancellation outcome is uncertain. Retrying uses the same request identifier.</p>
          )}
        </div>
      )}
      {!invocation && !error && <p role="status">Reading run state…</p>}
      {invocation && (
        <>
          <dl className="dg-run-status__facts">
            <div>
              <dt>Route</dt>
              <dd>{displayText(invocation.alias, 63)}</dd>
            </div>
            <div>
              <dt>Created</dt>
              <dd>
                <time dateTime={invocation.metadata.createdAt}>
                  {invocation.metadata.createdAt}
                </time>
              </dd>
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
          {invocation.state === "waiting" && (
            <p>
              A request or runtime condition is blocking this run. Inspect Requests and Activity for
              the reported cause.
            </p>
          )}
          {invocation.state === "cancelling" && (
            <p>
              Cancellation is in progress. Refresh to confirm the outcome before taking another
              action.
            </p>
          )}
          {(!statePresentations[invocation.state] || invocation.state === "unknown") && (
            <p>Run state is unknown. Refresh before attempting a lifecycle command.</p>
          )}
          {invocation.error && (
            <div role="alert" className="dg-invocation-status__domain-error">
              <strong>{displayText(invocation.error.code, 64)}. </strong>
              {displayText(invocation.error.message, 512)}
            </div>
          )}
          {operation?.error && (
            <div role="alert" className="dg-invocation-status__domain-error">
              <strong>Operation failure: {displayText(operation.error.code, 64)}. </strong>
              {displayText(operation.error.message, 512)}
            </div>
          )}
          {operation && operation.desiredState !== operation.observedState && (
            <p>
              Requested outcome: {displayText(operation.desiredState, 64)}. Observed operation
              state: {displayText(operation.observedState, 64)}.
            </p>
          )}
          {operation?.observedState === "failed" && !operation.error && (
            <p role="alert">
              The operation failed. Inspect Activity and Audit for available evidence.
            </p>
          )}
          {!operation && <p>The run's operation has not been confirmed.</p>}
          {cancellable && !canCancel && (
            <p>{disabledReason ?? "Cancellation authority has not been granted."}</p>
          )}
        </>
      )}
      {confirmation}
      <details className="dg-run-status__details">
        <summary>Resource and operation details</summary>
        <dl>
          <div>
            <dt>Isolation domain</dt>
            <dd>{displayText(reference.isolationDomainId, 64)}</dd>
          </div>
          <div>
            <dt>Run identifier</dt>
            <dd>{displayText(reference.invocationId, 64)}</dd>
          </div>
          {invocation && (
            <>
              <div>
                <dt>Service</dt>
                <dd>{displayText(invocation.serviceId, 64)}</dd>
              </div>
              <div>
                <dt>Revision</dt>
                <dd>{displayText(invocation.revisionId, 64)}</dd>
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
                <dt>Generation / version</dt>
                <dd>
                  {invocation.metadata.generation} / {invocation.metadata.version}
                </dd>
              </div>
              <div>
                <dt>Record updated</dt>
                <dd>{invocation.metadata.updatedAt}</dd>
              </div>
              <div>
                <dt>Artifact references</dt>
                <dd>{formatInteger(invocation.artifactIds.length)}</dd>
              </div>
              {invocation.usage && (
                <>
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
                </>
              )}
            </>
          )}
          {operation && (
            <>
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
              {operation.dueAt && (
                <div>
                  <dt>Next reconciliation</dt>
                  <dd>{operation.dueAt}</dd>
                </div>
              )}
              {operation.deadlineAt && (
                <div>
                  <dt>Deadline</dt>
                  <dd>{operation.deadlineAt}</dd>
                </div>
              )}
              {operation.errorClassification && (
                <div>
                  <dt>Error classification</dt>
                  <dd>{displayText(operation.errorClassification, 64)}</dd>
                </div>
              )}
            </>
          )}
          {error?.correlationId && (
            <div>
              <dt>Read or command correlation</dt>
              <dd>{displayText(error.correlationId, 128)}</dd>
            </div>
          )}
          {invocation?.error && (
            <div>
              <dt>Run failure correlation</dt>
              <dd>{displayText(invocation.error.correlationId, 128)}</dd>
            </div>
          )}
          {operation?.error && (
            <div>
              <dt>Operation failure correlation</dt>
              <dd>{displayText(operation.error.correlationId, 128)}</dd>
            </div>
          )}
        </dl>
      </details>
    </section>
  );
}
