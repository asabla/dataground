import { Button, SelectField, StatusBadge, TextField } from "@dataground/ui";
import { useId } from "react";

export interface InvocationComposerField {
  description?: string;
  key: string;
  label: string;
  maxLength?: number;
  minLength?: number;
  type?: "string" | "number" | "integer" | "boolean";
  options?: readonly { label: string; value: string }[];
  required: boolean;
}

export interface InvocationComposerSchema {
  description?: string;
  fields: readonly InvocationComposerField[];
  title?: string;
}

export interface InvocationComposerError {
  correlationId?: string;
  message: string;
  retryable: boolean;
}

export interface AcceptedInvocation {
  invocationId: string;
  operationId: string;
  state: string;
}

export interface InvocationComposerProps {
  accepted?: AcceptedInvocation;
  alias: string;
  canInvoke: boolean;
  disabledReason?: string;
  error?: InvocationComposerError;
  isSubmitting?: boolean;
  onAliasChange?: (value: string) => void;
  onOpenInvocation?: () => void;
  onSubmit?: () => void;
  onValueChange?: (key: string, value: string) => void;
  recoveryPending?: boolean;
  schema?: InvocationComposerSchema;
  schemaError?: string;
  target: { isolationDomainId: string; serviceId: string };
  validationErrors?: Readonly<Record<string, string>>;
  values: Readonly<Record<string, string>>;
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

export function InvocationComposer({
  accepted,
  alias,
  canInvoke,
  disabledReason,
  error,
  isSubmitting = false,
  onAliasChange,
  onOpenInvocation,
  onSubmit,
  onValueChange,
  recoveryPending = false,
  schema,
  schemaError,
  target,
  validationErrors = {},
  values,
}: InvocationComposerProps) {
  const titleId = useId();
  const unavailable = schemaError !== undefined || schema === undefined;
  const disabled =
    !canInvoke || unavailable || accepted !== undefined || isSubmitting || onSubmit === undefined;
  const presentation = accepted
    ? { label: "Invocation accepted", tone: "success" as const }
    : isSubmitting
      ? {
          label: recoveryPending ? "Retrying invocation" : "Submitting invocation",
          tone: "active" as const,
        }
      : error
        ? {
            label: recoveryPending ? "Outcome unconfirmed" : "Invocation not accepted",
            tone: "critical" as const,
          }
        : unavailable
          ? { label: "Composer unavailable", tone: "critical" as const }
          : { label: "Ready to invoke", tone: "neutral" as const };

  return (
    <section
      aria-busy={isSubmitting || undefined}
      aria-labelledby={titleId}
      className="dg-invocation-composer"
    >
      <div className="dg-invocation-composer__heading">
        <div>
          <p className="dg-invocation-composer__eyebrow">Invocation command</p>
          <h2 id={titleId}>
            {schema?.title ? displayText(schema.title, 128) : "Create invocation"}
          </h2>
        </div>
        <StatusBadge tone={presentation.tone}>{presentation.label}</StatusBadge>
      </div>

      <dl className="dg-invocation-composer__scope">
        <div>
          <dt>Isolation domain</dt>
          <dd>{displayText(target.isolationDomainId, 64)}</dd>
        </div>
        <div>
          <dt>Agent service</dt>
          <dd>{displayText(target.serviceId, 64)}</dd>
        </div>
      </dl>

      {!canInvoke && (
        <p className="dg-invocation-composer__authority">
          Observer access only.{" "}
          {displayText(disabledReason ?? "Invocation authority is required.", 255)}
        </p>
      )}

      {schemaError && (
        <div className="dg-invocation-composer__error" role="alert">
          <strong>Input contract unavailable.</strong> {displayText(schemaError, 512)}
        </div>
      )}

      {error && (
        <div className="dg-invocation-composer__error" role="alert">
          <strong>
            {recoveryPending ? "Invocation outcome is uncertain." : "Invocation was not accepted."}
          </strong>{" "}
          {displayText(error.message, 512)}
          {error.correlationId && (
            <>
              {" "}
              Correlation: <code>{displayText(error.correlationId, 128)}</code>
            </>
          )}
          {recoveryPending && (
            <p>Retrying sends the exact input with its original request identifier.</p>
          )}
        </div>
      )}

      {accepted ? (
        <div className="dg-invocation-composer__accepted" aria-live="polite">
          <strong>DataGround accepted the invocation.</strong>
          <dl>
            <div>
              <dt>Invocation</dt>
              <dd>{displayText(accepted.invocationId, 64)}</dd>
            </div>
            <div>
              <dt>Operation</dt>
              <dd>{displayText(accepted.operationId, 64)}</dd>
            </div>
            <div>
              <dt>State</dt>
              <dd>{displayText(accepted.state, 64)}</dd>
            </div>
          </dl>
          {onOpenInvocation && <Button onPress={onOpenInvocation}>Open invocation</Button>}
        </div>
      ) : schema && recoveryPending ? (
        <form
          className="dg-invocation-composer__form"
          onSubmit={(event) => {
            event.preventDefault();
            if (!disabled) onSubmit?.();
          }}
        >
          <p className="dg-invocation-composer__description">
            The original alias and input are retained for this retry and are not displayed here.
          </p>
          <div className="dg-invocation-composer__actions">
            <Button isDisabled={disabled} type="submit">
              Retry invocation
            </Button>
          </div>
        </form>
      ) : schema ? (
        <form
          className="dg-invocation-composer__form"
          onSubmit={(event) => {
            event.preventDefault();
            if (!disabled) onSubmit?.();
          }}
        >
          {schema.description && (
            <p className="dg-invocation-composer__description">
              {displayText(schema.description, 512)}
            </p>
          )}
          <TextField
            errorMessage={validationErrors.$alias}
            isDisabled={!canInvoke || isSubmitting || recoveryPending}
            isRequired
            label="Alias"
            maxLength={63}
            name="alias"
            onChange={onAliasChange}
            value={alias}
          />
          {schema.fields.map((field) =>
            field.options ? (
              <SelectField
                description={field.description && displayText(field.description, 512)}
                errorMessage={
                  Object.hasOwn(validationErrors, field.key)
                    ? validationErrors[field.key]
                    : undefined
                }
                isDisabled={!canInvoke || isSubmitting || recoveryPending}
                isRequired={field.required}
                key={field.key}
                label={displayText(field.label, 128)}
                name={field.key}
                onChange={(value) => onValueChange?.(field.key, value)}
                options={field.options}
                placeholder={field.required ? "Choose an option" : "Not provided"}
                value={Object.hasOwn(values, field.key) ? (values[field.key] ?? "") : ""}
              />
            ) : (
              <TextField
                description={field.description && displayText(field.description, 512)}
                errorMessage={
                  Object.hasOwn(validationErrors, field.key)
                    ? validationErrors[field.key]
                    : undefined
                }
                isDisabled={!canInvoke || isSubmitting || recoveryPending}
                isMultiline={field.key === "prompt" && (!field.type || field.type === "string")}
                isRequired={field.required}
                key={field.key}
                label={displayText(field.label, 128)}
                inputMode={
                  field.type === "number" || field.type === "integer" ? "decimal" : undefined
                }
                maxLength={field.maxLength === undefined ? 128 : field.maxLength * 2}
                name={field.key}
                onChange={(value) => onValueChange?.(field.key, value)}
                value={Object.hasOwn(values, field.key) ? (values[field.key] ?? "") : ""}
                validationBehavior="aria"
              />
            ),
          )}
          <div className="dg-invocation-composer__actions">
            <Button isDisabled={disabled} type="submit">
              Start invocation
            </Button>
          </div>
        </form>
      ) : null}
    </section>
  );
}
