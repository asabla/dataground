import { Button, StatusBadge, TextField } from "@dataground/ui";
import { useId } from "react";

export interface CreatedAgentService {
  createdAt: string;
  createdBy: string;
  id: string;
  name: string;
  version: number;
}

export interface AgentServiceCreateError {
  correlationId?: string;
  message: string;
  retryable: boolean;
}

export interface AgentServiceCreateProps {
  canCreate: boolean;
  created?: CreatedAgentService;
  description: string;
  disabledReason?: string;
  error?: AgentServiceCreateError;
  isolationDomainId: string;
  isSubmitting?: boolean;
  name: string;
  onDescriptionChange?: (value: string) => void;
  onNameChange?: (value: string) => void;
  onOpenService?: () => void;
  onSubmit?: () => void;
  recoveryPending?: boolean;
  validationErrors?: Readonly<{ description?: string; name?: string }>;
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

export function AgentServiceCreate({
  canCreate,
  created,
  description,
  disabledReason,
  error,
  isolationDomainId,
  isSubmitting = false,
  name,
  onDescriptionChange,
  onNameChange,
  onOpenService,
  onSubmit,
  recoveryPending = false,
  validationErrors = {},
}: AgentServiceCreateProps) {
  const titleId = useId();
  const disabled = !canCreate || created !== undefined || isSubmitting || onSubmit === undefined;
  const presentation = created
    ? { label: "Service created", tone: "success" as const }
    : isSubmitting
      ? {
          label: recoveryPending ? "Retrying creation" : "Creating service",
          tone: "active" as const,
        }
      : error
        ? {
            label: recoveryPending ? "Outcome unconfirmed" : "Service not created",
            tone: "critical" as const,
          }
        : { label: "Ready to create", tone: "neutral" as const };

  return (
    <section
      aria-busy={isSubmitting || undefined}
      aria-labelledby={titleId}
      className="dg-agent-service-create"
    >
      <div className="dg-agent-service-create__heading">
        <div>
          <p className="dg-agent-service-create__eyebrow">Service registry</p>
          <h2 id={titleId}>Create agent service</h2>
        </div>
        <StatusBadge tone={presentation.tone}>{presentation.label}</StatusBadge>
      </div>

      <dl className="dg-agent-service-create__scope">
        <div>
          <dt>Isolation domain</dt>
          <dd>{displayText(isolationDomainId, 64)}</dd>
        </div>
      </dl>

      {!canCreate && (
        <p className="dg-agent-service-create__authority">
          Observer access only.{" "}
          {displayText(disabledReason ?? "Service creation authority is required.", 255)}
        </p>
      )}

      {error && (
        <div className="dg-agent-service-create__error" role="alert">
          <strong>
            {recoveryPending
              ? "Service creation outcome is uncertain."
              : "Service was not created."}
          </strong>{" "}
          {displayText(error.message, 512)}
          {error.correlationId && (
            <>
              {" "}
              Correlation: <code>{displayText(error.correlationId, 128)}</code>
            </>
          )}
          {recoveryPending && (
            <p>Retrying sends the exact service definition with its original request identifier.</p>
          )}
        </div>
      )}

      {created ? (
        <div className="dg-agent-service-create__created" aria-live="polite">
          <strong>DataGround created the service.</strong>
          <dl>
            <div>
              <dt>Service</dt>
              <dd>{displayText(created.id, 64)}</dd>
            </div>
            <div>
              <dt>Name</dt>
              <dd>{displayText(created.name, 128)}</dd>
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
          {onOpenService && <Button onPress={onOpenService}>Open service</Button>}
        </div>
      ) : recoveryPending ? (
        <form
          className="dg-agent-service-create__form"
          onSubmit={(event) => {
            event.preventDefault();
            if (!disabled) onSubmit?.();
          }}
        >
          <p className="dg-agent-service-create__retained">
            The original name and description are retained for this retry and are not displayed
            here.
          </p>
          <div className="dg-agent-service-create__actions">
            <Button isDisabled={disabled} type="submit">
              Retry service creation
            </Button>
          </div>
        </form>
      ) : (
        <form
          className="dg-agent-service-create__form"
          onSubmit={(event) => {
            event.preventDefault();
            if (!disabled) onSubmit?.();
          }}
        >
          <TextField
            description="A human-readable product name; runtime and provider identities are configured in revisions."
            errorMessage={validationErrors.name}
            isDisabled={!canCreate || isSubmitting}
            isRequired
            label="Service name"
            maxLength={128}
            minLength={1}
            name="service-name"
            onChange={onNameChange}
            value={name}
          />
          <TextField
            description="Optional operator-facing context for this service."
            errorMessage={validationErrors.description}
            isDisabled={!canCreate || isSubmitting}
            isMultiline
            label="Description"
            maxLength={2048}
            name="service-description"
            onChange={onDescriptionChange}
            value={description}
          />
          <div className="dg-agent-service-create__actions">
            <Button isDisabled={disabled} type="submit">
              Create service
            </Button>
          </div>
        </form>
      )}
    </section>
  );
}
