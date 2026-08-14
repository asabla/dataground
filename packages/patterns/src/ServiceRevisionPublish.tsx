import { Button, StatusBadge } from "@dataground/ui";
import { useId } from "react";

export interface PublishedServiceRevision {
  publishedAt: string;
  version: number;
}

export interface ServiceRevisionPublishError {
  correlationId?: string;
  message: string;
  outcomeUnknown?: boolean;
  retryable: boolean;
}

export interface ServiceRevisionPublishProps {
  canPublish: boolean;
  confirmationVisible?: boolean;
  createdAt: string;
  createdBy: string;
  disabledReason?: string;
  error?: ServiceRevisionPublishError;
  hasInputSchema: boolean;
  hasOutputSchema: boolean;
  isolationDomainId: string;
  isSubmitting?: boolean;
  onAssignAlias?: () => void;
  onConfirm?: () => void;
  onDismissConfirmation?: () => void;
  onRequestConfirmation?: () => void;
  published?: PublishedServiceRevision;
  recoveryPending?: boolean;
  requiredCapabilities: readonly string[];
  revisionId: string;
  revisionNumber: number;
  runtimeProfile: string;
  serviceId: string;
  version: number;
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

export function ServiceRevisionPublish({
  canPublish,
  confirmationVisible = false,
  createdAt,
  createdBy,
  disabledReason,
  error,
  hasInputSchema,
  hasOutputSchema,
  isolationDomainId,
  isSubmitting = false,
  onAssignAlias,
  onConfirm,
  onDismissConfirmation,
  onRequestConfirmation,
  published,
  recoveryPending = false,
  requiredCapabilities,
  revisionId,
  revisionNumber,
  runtimeProfile,
  serviceId,
  version,
}: ServiceRevisionPublishProps) {
  const titleId = useId();
  const confirmationId = useId();
  const presentation = published
    ? { label: "Revision published", tone: "success" as const }
    : isSubmitting
      ? {
          label: recoveryPending ? "Retrying publication" : "Publishing revision",
          tone: "active" as const,
        }
      : error
        ? {
            label:
              recoveryPending || error.outcomeUnknown
                ? "Outcome unconfirmed"
                : "Revision not published",
            tone: "critical" as const,
          }
        : confirmationVisible
          ? { label: "Confirmation required", tone: "warning" as const }
          : { label: "Draft revision", tone: "neutral" as const };
  const actionDisabled = !canPublish || isSubmitting || onConfirm === undefined;

  return (
    <section
      aria-busy={isSubmitting || undefined}
      aria-labelledby={titleId}
      className="dg-service-revision-publish"
    >
      <div className="dg-service-revision-publish__heading">
        <div>
          <p className="dg-service-revision-publish__eyebrow">Service registry</p>
          <h2 id={titleId}>Publish revision</h2>
        </div>
        <StatusBadge tone={presentation.tone}>{presentation.label}</StatusBadge>
      </div>

      <dl className="dg-service-revision-publish__scope">
        <div>
          <dt>Isolation domain</dt>
          <dd>{displayText(isolationDomainId, 64)}</dd>
        </div>
        <div>
          <dt>Agent service</dt>
          <dd>{displayText(serviceId, 64)}</dd>
        </div>
        <div>
          <dt>Revision</dt>
          <dd>{displayText(revisionId, 64)}</dd>
        </div>
      </dl>

      <dl className="dg-service-revision-publish__facts">
        <div>
          <dt>Revision number</dt>
          <dd>{revisionNumber}</dd>
        </div>
        <div>
          <dt>Expected version</dt>
          <dd>{version}</dd>
        </div>
        <div>
          <dt>Runtime profile</dt>
          <dd>{displayText(runtimeProfile, 128)}</dd>
        </div>
        <div>
          <dt>Required capabilities</dt>
          <dd>
            {requiredCapabilities.length === 0
              ? "None"
              : requiredCapabilities.map((capability) => displayText(capability, 128)).join(", ")}
          </dd>
        </div>
        <div>
          <dt>Input schema</dt>
          <dd>{hasInputSchema ? "Declared" : "Not declared"}</dd>
        </div>
        <div>
          <dt>Output schema</dt>
          <dd>{hasOutputSchema ? "Declared" : "Not declared"}</dd>
        </div>
        <div>
          <dt>Created by</dt>
          <dd>{displayText(createdBy, 128)}</dd>
        </div>
        <div>
          <dt>Created at</dt>
          <dd>{displayText(createdAt, 64)}</dd>
        </div>
      </dl>

      {!canPublish && !published && (
        <p className="dg-service-revision-publish__authority">
          Observer access only.{" "}
          {displayText(disabledReason ?? "Revision publication authority is required.", 255)}
        </p>
      )}

      {error && (
        <div className="dg-service-revision-publish__error" role="alert">
          <strong>
            {recoveryPending || error.outcomeUnknown
              ? "Revision publication outcome is uncertain."
              : "Revision was not published."}
          </strong>{" "}
          {displayText(error.message, 512)}
          {error.correlationId && (
            <>
              {" "}
              Correlation: <code>{displayText(error.correlationId, 128)}</code>
            </>
          )}
          {recoveryPending && (
            <p>Retrying uses the original expected version and request identifier.</p>
          )}
        </div>
      )}

      {published ? (
        <div className="dg-service-revision-publish__published" aria-live="polite">
          <strong>DataGround published the immutable revision.</strong>
          <dl>
            <div>
              <dt>Published at</dt>
              <dd>{displayText(published.publishedAt, 64)}</dd>
            </div>
            <div>
              <dt>Published version</dt>
              <dd>{published.version}</dd>
            </div>
          </dl>
          <p>
            Publication does not make the revision routable. Assign a service alias separately
            before invocation.
          </p>
          {onAssignAlias && <Button onPress={onAssignAlias}>Assign alias</Button>}
        </div>
      ) : confirmationVisible && !recoveryPending ? (
        <fieldset
          aria-describedby={confirmationId}
          className="dg-service-revision-publish__confirmation"
        >
          <legend>Confirm revision publication</legend>
          <p id={confirmationId}>
            Publication makes this exact version immutable and validates every required capability.
            It does not assign or move an alias.
          </p>
          <div className="dg-service-revision-publish__confirmation-actions">
            <Button isDisabled={actionDisabled} onPress={onConfirm}>
              {isSubmitting ? "Publishing revision…" : "Confirm publication"}
            </Button>
            <Button isDisabled={isSubmitting} onPress={onDismissConfirmation} variant="quiet">
              Keep as draft
            </Button>
          </div>
        </fieldset>
      ) : error?.outcomeUnknown && !recoveryPending ? (
        <p className="dg-service-revision-publish__boundary">
          Do not submit another publication request. Reconcile this revision through an authorized
          API or operator view before taking further action.
        </p>
      ) : (
        <>
          <p className="dg-service-revision-publish__boundary">
            Publishing validates the runtime profile and required capabilities against current
            platform support. A failed validation leaves the draft unpublished.
          </p>
          <div className="dg-service-revision-publish__actions">
            {recoveryPending ? (
              <Button isDisabled={actionDisabled} onPress={onConfirm}>
                {isSubmitting ? "Retrying publication…" : "Retry publication"}
              </Button>
            ) : (
              <Button
                isDisabled={!canPublish || isSubmitting || onRequestConfirmation === undefined}
                onPress={onRequestConfirmation}
              >
                Review publication
              </Button>
            )}
          </div>
        </>
      )}
    </section>
  );
}
