import { Button, StatusBadge, TextField } from "@dataground/ui";
import { useId } from "react";

export interface CreatedServiceRevision {
  createdAt: string;
  createdBy: string;
  id: string;
  revisionNumber: number;
  runtimeProfile: string;
  state: "draft";
  version: number;
}

export interface ServiceRevisionDraftError {
  correlationId?: string;
  message: string;
  retryable: boolean;
}

export interface ServiceRevisionDraftProps {
  canCreateRevision: boolean;
  created?: CreatedServiceRevision;
  disabledReason?: string;
  error?: ServiceRevisionDraftError;
  inputSchema: string;
  isolationDomainId: string;
  isSubmitting?: boolean;
  onInputSchemaChange?: (value: string) => void;
  onOpenRevision?: () => void;
  onOutputSchemaChange?: (value: string) => void;
  onRequiredCapabilitiesChange?: (value: string) => void;
  onRuntimeProfileChange?: (value: string) => void;
  onSubmit?: () => void;
  outputSchema: string;
  recoveryPending?: boolean;
  requiredCapabilities: string;
  runtimeProfile: string;
  serviceId: string;
  validationErrors?: Readonly<{
    inputSchema?: string;
    outputSchema?: string;
    requiredCapabilities?: string;
    runtimeProfile?: string;
  }>;
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

export function ServiceRevisionDraft({
  canCreateRevision,
  created,
  disabledReason,
  error,
  inputSchema,
  isolationDomainId,
  isSubmitting = false,
  onInputSchemaChange,
  onOpenRevision,
  onOutputSchemaChange,
  onRequiredCapabilitiesChange,
  onRuntimeProfileChange,
  onSubmit,
  outputSchema,
  recoveryPending = false,
  requiredCapabilities,
  runtimeProfile,
  serviceId,
  validationErrors = {},
}: ServiceRevisionDraftProps) {
  const titleId = useId();
  const disabled =
    !canCreateRevision || created !== undefined || isSubmitting || onSubmit === undefined;
  const presentation = created
    ? { label: "Revision draft created", tone: "success" as const }
    : isSubmitting
      ? {
          label: recoveryPending ? "Retrying draft creation" : "Creating revision draft",
          tone: "active" as const,
        }
      : error
        ? {
            label: recoveryPending ? "Outcome unconfirmed" : "Revision not created",
            tone: "critical" as const,
          }
        : { label: "Ready to create", tone: "neutral" as const };

  return (
    <section
      aria-busy={isSubmitting || undefined}
      aria-labelledby={titleId}
      className="dg-service-revision-draft"
    >
      <div className="dg-service-revision-draft__heading">
        <div>
          <p className="dg-service-revision-draft__eyebrow">Service registry</p>
          <h2 id={titleId}>Create revision draft</h2>
        </div>
        <StatusBadge tone={presentation.tone}>{presentation.label}</StatusBadge>
      </div>

      <dl className="dg-service-revision-draft__scope">
        <div>
          <dt>Isolation domain</dt>
          <dd>{displayText(isolationDomainId, 64)}</dd>
        </div>
        <div>
          <dt>Agent service</dt>
          <dd>{displayText(serviceId, 64)}</dd>
        </div>
      </dl>

      {!canCreateRevision && (
        <p className="dg-service-revision-draft__authority">
          Observer access only.{" "}
          {displayText(disabledReason ?? "Revision creation authority is required.", 255)}
        </p>
      )}

      {error && (
        <div className="dg-service-revision-draft__error" role="alert">
          <strong>
            {recoveryPending
              ? "Revision creation outcome is uncertain."
              : "Revision draft was not created."}
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
              Retrying sends the exact revision definition with its original request identifier.
            </p>
          )}
        </div>
      )}

      {created ? (
        <div className="dg-service-revision-draft__created" aria-live="polite">
          <strong>DataGround created an unpublished revision draft.</strong>
          <dl>
            <div>
              <dt>Revision</dt>
              <dd>{displayText(created.id, 64)}</dd>
            </div>
            <div>
              <dt>Revision number</dt>
              <dd>{created.revisionNumber}</dd>
            </div>
            <div>
              <dt>State</dt>
              <dd>{created.state}</dd>
            </div>
            <div>
              <dt>Runtime profile</dt>
              <dd>{displayText(created.runtimeProfile, 128)}</dd>
            </div>
            <div>
              <dt>Version</dt>
              <dd>{created.version}</dd>
            </div>
            <div>
              <dt>Created by</dt>
              <dd>{displayText(created.createdBy, 128)}</dd>
            </div>
            <div>
              <dt>Created at</dt>
              <dd>{displayText(created.createdAt, 64)}</dd>
            </div>
          </dl>
          <p className="dg-service-revision-draft__boundary">
            This draft is not published, routable, or invocable.
          </p>
          {onOpenRevision && <Button onPress={onOpenRevision}>Open revision</Button>}
        </div>
      ) : recoveryPending ? (
        <form
          className="dg-service-revision-draft__form"
          onSubmit={(event) => {
            event.preventDefault();
            if (!disabled) onSubmit?.();
          }}
        >
          <p className="dg-service-revision-draft__retained">
            The original runtime, capabilities, and schemas are retained for this retry and are not
            displayed here.
          </p>
          <div className="dg-service-revision-draft__actions">
            <Button isDisabled={disabled} type="submit">
              Retry revision creation
            </Button>
          </div>
        </form>
      ) : (
        <form
          className="dg-service-revision-draft__form"
          onSubmit={(event) => {
            event.preventDefault();
            if (!disabled) onSubmit?.();
          }}
        >
          <TextField
            description="Provider-neutral runtime profile recorded in the immutable revision."
            errorMessage={validationErrors.runtimeProfile}
            isDisabled={!canCreateRevision || isSubmitting}
            isRequired
            label="Runtime profile"
            maxLength={128}
            minLength={1}
            name="revision-runtime-profile"
            onChange={onRuntimeProfileChange}
            value={runtimeProfile}
          />
          <TextField
            description="Optional capability names separated by commas or new lines. Publication fails closed if the selected runtime cannot satisfy one."
            errorMessage={validationErrors.requiredCapabilities}
            isDisabled={!canCreateRevision || isSubmitting}
            isMultiline
            label="Required capabilities"
            name="revision-required-capabilities"
            onChange={onRequiredCapabilitiesChange}
            value={requiredCapabilities}
          />
          <TextField
            description="Optional JSON object describing accepted invocation input."
            errorMessage={validationErrors.inputSchema}
            isDisabled={!canCreateRevision || isSubmitting}
            isMultiline
            label="Input schema"
            name="revision-input-schema"
            onChange={onInputSchemaChange}
            value={inputSchema}
          />
          <TextField
            description="Optional JSON object describing structured runtime output."
            errorMessage={validationErrors.outputSchema}
            isDisabled={!canCreateRevision || isSubmitting}
            isMultiline
            label="Output schema"
            name="revision-output-schema"
            onChange={onOutputSchemaChange}
            value={outputSchema}
          />
          <p className="dg-service-revision-draft__boundary">
            Creating this immutable draft does not publish it or assign an alias.
          </p>
          <div className="dg-service-revision-draft__actions">
            <Button isDisabled={disabled} type="submit">
              Create revision draft
            </Button>
          </div>
        </form>
      )}
    </section>
  );
}
