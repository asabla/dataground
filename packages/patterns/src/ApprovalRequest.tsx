import { Button, StatusBadge, type StatusTone } from "@dataground/ui";
import { useId } from "react";

export type ApprovalDecision = "approve" | "deny";

export interface ApprovalResource {
  id: string;
  isolationDomainId: string;
  invocationId: string;
  requestedAction: string;
  state: string;
  version: number;
  decision?: ApprovalDecision;
  resolvedBy?: string;
  resolvedAt?: string;
  createdAt: string;
  updatedAt: string;
}

export interface ApprovalRequestError {
  message: string;
  correlationId?: string;
  retryable: boolean;
}

export interface ApprovalRequestProps {
  approval: ApprovalResource;
  canResolve: boolean;
  disabledReason?: string;
  error?: ApprovalRequestError;
  errorHeading?: string;
  isSubmitting?: boolean;
  onDecision?: (decision: ApprovalDecision) => void;
  onRefresh?: () => void;
  recoveryDecision?: ApprovalDecision;
}

interface StatePresentation {
  label: string;
  tone: StatusTone;
}

const statePresentations: Record<string, StatePresentation> = {
  delivered: { label: "Decision delivered", tone: "success" },
  delivering: { label: "Delivering decision", tone: "active" },
  pending: { label: "Waiting for decision", tone: "waiting" },
  resolved: { label: "Decision recorded", tone: "active" },
};

const actionLabels: Record<string, string> = {
  "process.execute": "Run a process",
  "workspace.change": "Change the workspace",
};

function statePresentation(state: string): StatePresentation {
  return statePresentations[state] ?? { label: `Unknown state: ${state}`, tone: "neutral" };
}

function decisionLabel(decision: ApprovalDecision): string {
  return decision === "approve" ? "Approved" : "Denied";
}

function submissionLabel(decision: ApprovalDecision, isSubmitting: boolean): string {
  if (decision === "approve") {
    return isSubmitting ? "Submitting approval…" : "Approve request";
  }
  return isSubmitting ? "Submitting denial…" : "Deny request";
}

export function ApprovalRequest({
  approval,
  canResolve,
  disabledReason,
  error,
  errorHeading = "Decision not confirmed.",
  isSubmitting = false,
  onDecision,
  onRefresh,
  recoveryDecision,
}: ApprovalRequestProps) {
  const titleId = useId();
  const presentation = statePresentation(approval.state);
  const pending = approval.state === "pending";
  const actionLabel = actionLabels[approval.requestedAction];
  const canSubmit = pending && actionLabel !== undefined && canResolve && onDecision !== undefined;

  return (
    <section
      aria-busy={isSubmitting || undefined}
      aria-labelledby={titleId}
      className="dg-approval-request"
    >
      <div className="dg-approval-request__heading">
        <div>
          <p className="dg-approval-request__eyebrow">Invocation approval</p>
          <h2 id={titleId}>{actionLabel ?? "Unrecognized requested action"}</h2>
        </div>
        <StatusBadge tone={presentation.tone}>{presentation.label}</StatusBadge>
      </div>

      {!actionLabel && (
        <p className="dg-approval-request__unknown">
          The server reported <code>{approval.requestedAction}</code>. Update the Workbench before
          deciding this request.
        </p>
      )}

      <dl className="dg-approval-request__facts">
        <div>
          <dt>Isolation domain</dt>
          <dd>{approval.isolationDomainId}</dd>
        </div>
        <div>
          <dt>Invocation</dt>
          <dd>{approval.invocationId}</dd>
        </div>
        <div>
          <dt>Approval</dt>
          <dd>{approval.id}</dd>
        </div>
        <div>
          <dt>Version</dt>
          <dd>{approval.version}</dd>
        </div>
        <div>
          <dt>Requested at</dt>
          <dd>
            <time dateTime={approval.createdAt}>{approval.createdAt}</time>
          </dd>
        </div>
        {approval.decision && (
          <div>
            <dt>Recorded decision</dt>
            <dd>{decisionLabel(approval.decision)}</dd>
          </div>
        )}
        {approval.resolvedBy && (
          <div>
            <dt>Decided by</dt>
            <dd>{approval.resolvedBy}</dd>
          </div>
        )}
        {approval.resolvedAt && (
          <div>
            <dt>Decided at</dt>
            <dd>
              <time dateTime={approval.resolvedAt}>{approval.resolvedAt}</time>
            </dd>
          </div>
        )}
        <div>
          <dt>Last observed</dt>
          <dd>
            <time dateTime={approval.updatedAt}>{approval.updatedAt}</time>
          </dd>
        </div>
      </dl>

      <p className="dg-approval-request__authority">
        A submitted decision does not bypass policy. DataGround rechecks authorization and
        enforcement before delivering it to the runtime.
      </p>

      {error && (
        <div className="dg-approval-request__error" role="alert">
          <strong>{errorHeading}</strong> {error.message}
          {error.correlationId && (
            <span>
              {" "}
              Correlation: <code>{error.correlationId}</code>
            </span>
          )}
          {error.retryable && recoveryDecision && (
            <p>Retrying the same decision must reuse its original idempotency key.</p>
          )}
        </div>
      )}

      {pending && !canSubmit && (
        <p className="dg-approval-request__observer">
          {disabledReason ?? "This view can observe the request but cannot submit a decision."}
        </p>
      )}

      <div className="dg-approval-request__actions">
        {canSubmit && recoveryDecision ? (
          <Button
            isDisabled={isSubmitting}
            onPress={() => onDecision(recoveryDecision)}
            variant={recoveryDecision === "approve" ? "primary" : "secondary"}
          >
            {isSubmitting
              ? submissionLabel(recoveryDecision, true)
              : `Retry ${recoveryDecision === "approve" ? "approval" : "denial"}`}
          </Button>
        ) : (
          canSubmit && (
            <>
              <Button
                isDisabled={isSubmitting}
                onPress={() => onDecision("approve")}
                variant="primary"
              >
                {submissionLabel("approve", isSubmitting)}
              </Button>
              <Button
                isDisabled={isSubmitting}
                onPress={() => onDecision("deny")}
                variant="secondary"
              >
                {submissionLabel("deny", isSubmitting)}
              </Button>
            </>
          )
        )}
        {onRefresh && (
          <Button isDisabled={isSubmitting} onPress={onRefresh} variant="quiet">
            Refresh state
          </Button>
        )}
      </div>
    </section>
  );
}
