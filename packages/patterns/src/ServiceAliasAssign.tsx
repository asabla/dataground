import { Button, StatusBadge, TextField } from "@dataground/ui";
import { useId } from "react";

export interface AssignedServiceAlias {
  generation: number;
  id: string;
  name: string;
  revisionId: string;
  updatedAt: string;
  version: number;
}

export interface ServiceAliasAssignError {
  correlationId?: string;
  message: string;
  outcomeUnknown?: boolean;
  retryable: boolean;
}

export interface ServiceAliasAssignProps {
  aliasName: string;
  aliasValidationError?: string;
  assigned?: AssignedServiceAlias;
  blockedReason?: string;
  canAssign: boolean;
  confirmationVisible?: boolean;
  currentAliasId?: string;
  currentRevisionId?: string;
  currentVersion?: number;
  disabledReason?: string;
  error?: ServiceAliasAssignError;
  isolationDomainId: string;
  isSubmitting?: boolean;
  onAliasNameChange?: (value: string) => void;
  onComposeInvocation?: () => void;
  onConfirm?: () => void;
  onDismissConfirmation?: () => void;
  onRequestConfirmation?: () => void;
  recoveryPending?: boolean;
  revisionNumber: number;
  serviceId: string;
  targetRevisionId: string;
  targetVersion: number;
}

function displayText(value: string, maximum = 255): string {
  const normalized = Array.from(value, (character) => {
    const point = character.codePointAt(0) ?? 0;
    return point <= 8 ||
      point === 11 ||
      point === 12 ||
      (point >= 14 && point <= 31) ||
      (point >= 127 && point <= 159)
      ? "�"
      : character;
  })
    .join("")
    .replaceAll(/\s+/gu, " ")
    .trim();
  return normalized.length > maximum ? `${normalized.slice(0, maximum)}…` : normalized;
}

export function ServiceAliasAssign({
  aliasName,
  aliasValidationError,
  assigned,
  blockedReason,
  canAssign,
  confirmationVisible = false,
  currentAliasId,
  currentRevisionId,
  currentVersion,
  disabledReason,
  error,
  isolationDomainId,
  isSubmitting = false,
  onAliasNameChange,
  onComposeInvocation,
  onConfirm,
  onDismissConfirmation,
  onRequestConfirmation,
  recoveryPending = false,
  revisionNumber,
  serviceId,
  targetRevisionId,
  targetVersion,
}: ServiceAliasAssignProps) {
  const titleId = useId();
  const confirmationId = useId();
  const alreadyAssigned =
    blockedReason === undefined &&
    currentRevisionId === targetRevisionId &&
    currentVersion !== undefined;
  const moving = currentRevisionId !== undefined && !alreadyAssigned;
  const presentation = assigned
    ? { label: "Alias routed", tone: "success" as const }
    : alreadyAssigned
      ? { label: "Already routed", tone: "success" as const }
      : isSubmitting
        ? {
            label: recoveryPending ? "Retrying route change" : "Changing route",
            tone: "active" as const,
          }
        : error
          ? {
              label:
                recoveryPending && error.outcomeUnknown ? "Outcome unconfirmed" : "Route unchanged",
              tone: "critical" as const,
            }
          : confirmationVisible
            ? { label: "Confirmation required", tone: "warning" as const }
            : {
                label: moving ? "Ready to move" : "Ready to assign",
                tone: "neutral" as const,
              };
  const actionDisabled =
    !canAssign || blockedReason !== undefined || isSubmitting || onConfirm === undefined;

  return (
    <section
      aria-busy={isSubmitting || undefined}
      aria-labelledby={titleId}
      className="dg-service-alias-assign"
    >
      <div className="dg-service-alias-assign__heading">
        <div>
          <p className="dg-service-alias-assign__eyebrow">Service routing</p>
          <h2 id={titleId}>{moving ? "Move service alias" : "Assign service alias"}</h2>
        </div>
        <StatusBadge tone={presentation.tone}>{presentation.label}</StatusBadge>
      </div>

      <dl className="dg-service-alias-assign__scope">
        <div>
          <dt>Isolation domain</dt>
          <dd>{displayText(isolationDomainId, 64)}</dd>
        </div>
        <div>
          <dt>Agent service</dt>
          <dd>{displayText(serviceId, 64)}</dd>
        </div>
        <div>
          <dt>Published revision</dt>
          <dd>{displayText(targetRevisionId, 64)}</dd>
        </div>
      </dl>

      <dl className="dg-service-alias-assign__facts">
        <div>
          <dt>Revision number</dt>
          <dd>{revisionNumber}</dd>
        </div>
        <div>
          <dt>Published version</dt>
          <dd>{targetVersion}</dd>
        </div>
        {currentAliasId && (
          <div>
            <dt>Current alias resource</dt>
            <dd>{displayText(currentAliasId, 64)}</dd>
          </div>
        )}
        {currentRevisionId && (
          <div>
            <dt>Current revision</dt>
            <dd>{displayText(currentRevisionId, 64)}</dd>
          </div>
        )}
        <div>
          <dt>Expected alias version</dt>
          <dd>{currentVersion ?? 0}</dd>
        </div>
      </dl>

      {!canAssign && !blockedReason && !assigned && !alreadyAssigned && (
        <p className="dg-service-alias-assign__authority">
          Observer access only.{" "}
          {displayText(disabledReason ?? "Service routing authority is required.", 255)}
        </p>
      )}

      {blockedReason && !assigned && (
        <p className="dg-service-alias-assign__blocked" role="alert">
          <strong>Routing unavailable.</strong> {displayText(blockedReason, 255)}
        </p>
      )}

      {error && (
        <div className="dg-service-alias-assign__error" role="alert">
          <strong>
            {error.outcomeUnknown
              ? "Service routing outcome is uncertain."
              : "Service route was not changed."}
          </strong>{" "}
          {displayText(error.message, 512)}
          {error.correlationId && (
            <>
              {" "}
              Correlation: <code>{displayText(error.correlationId, 128)}</code>
            </>
          )}
          {recoveryPending && (
            <p>
              Retrying uses the original alias, target revision, expected version, and request
              identifier.
            </p>
          )}
        </div>
      )}

      {assigned ? (
        <div className="dg-service-alias-assign__assigned" aria-live="polite">
          <strong>DataGround routed the alias to the published revision.</strong>
          <dl>
            <div>
              <dt>Alias</dt>
              <dd>{displayText(assigned.name, 63)}</dd>
            </div>
            <div>
              <dt>Alias resource</dt>
              <dd>{displayText(assigned.id, 64)}</dd>
            </div>
            <div>
              <dt>Revision</dt>
              <dd>{displayText(assigned.revisionId, 64)}</dd>
            </div>
            <div>
              <dt>Generation</dt>
              <dd>{assigned.generation}</dd>
            </div>
            <div>
              <dt>Version</dt>
              <dd>{assigned.version}</dd>
            </div>
            <div>
              <dt>Updated at</dt>
              <dd>{displayText(assigned.updatedAt, 64)}</dd>
            </div>
          </dl>
          <p>New invocations can use this alias. Existing invocations remain revision-pinned.</p>
          {onComposeInvocation && <Button onPress={onComposeInvocation}>Compose invocation</Button>}
        </div>
      ) : alreadyAssigned ? (
        <div className="dg-service-alias-assign__assigned" aria-live="polite">
          <strong>The alias already targets this published revision.</strong>
          <p>No routing command is needed. Existing invocations remain revision-pinned.</p>
          {onComposeInvocation && <Button onPress={onComposeInvocation}>Compose invocation</Button>}
        </div>
      ) : confirmationVisible && !recoveryPending ? (
        <fieldset
          aria-describedby={confirmationId}
          className="dg-service-alias-assign__confirmation"
        >
          <legend>Confirm service route change</legend>
          <p id={confirmationId}>
            {moving
              ? "This moves the alias for new invocations using the observed alias version. Existing invocations remain pinned to their selected revision."
              : "This creates a routable alias only if it does not already exist. Existing invocations are unaffected."}
          </p>
          <div className="dg-service-alias-assign__confirmation-actions">
            <Button isDisabled={actionDisabled} onPress={onConfirm}>
              {isSubmitting ? "Changing route…" : "Confirm route change"}
            </Button>
            <Button isDisabled={isSubmitting} onPress={onDismissConfirmation} variant="quiet">
              Keep current route
            </Button>
          </div>
        </fieldset>
      ) : recoveryPending ? (
        <div className="dg-service-alias-assign__actions">
          <Button isDisabled={actionDisabled} onPress={onConfirm}>
            {isSubmitting ? "Retrying route change…" : "Retry route change"}
          </Button>
        </div>
      ) : error ? (
        <p className="dg-service-alias-assign__boundary">
          Refresh the published revision and current alias state before preparing another routing
          command.
        </p>
      ) : (
        <>
          {moving ? (
            <p className="dg-service-alias-assign__alias-name">
              Alias: <code>{displayText(aliasName, 63)}</code>
            </p>
          ) : (
            <TextField
              description="Stable lowercase route name for new invocations."
              errorMessage={aliasValidationError}
              isDisabled={!canAssign || blockedReason !== undefined || isSubmitting}
              isRequired
              label="Alias"
              maxLength={63}
              minLength={1}
              name="service-alias"
              onChange={onAliasNameChange}
              value={aliasName}
            />
          )}
          <p className="dg-service-alias-assign__boundary">
            Routing changes select a published revision for new invocations. They do not modify the
            revision or retarget work that already exists.
          </p>
          <div className="dg-service-alias-assign__actions">
            <Button
              isDisabled={
                !canAssign ||
                blockedReason !== undefined ||
                isSubmitting ||
                onRequestConfirmation === undefined
              }
              onPress={onRequestConfirmation}
            >
              Review routing
            </Button>
          </div>
        </>
      )}
    </section>
  );
}
