import { InvocationComposer } from "@dataground/patterns";
import "@dataground/patterns/styles.css";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import {
  type AgentServiceInvocationTarget,
  type InvocationFailure,
  type InvocationReference,
  type InvocationStatusResource,
  invokeAgentService,
} from "./client";
import {
  type InvocationComposerSchema,
  type InvocationInput,
  normalizeInvocationComposerSchema,
  prepareInvocationInput,
} from "./composerSchema";

interface InvocationAttempt {
  alias: string;
  idempotencyKey: string;
  input: InvocationInput;
}

interface ComposerWorkflowState {
  accepted?: InvocationStatusResource;
  alias: string;
  error?: InvocationFailure;
  recoveryAttempt?: InvocationAttempt;
  submitting: boolean;
  targetKey: string;
  validationErrors: Record<string, string>;
  values: Record<string, string>;
}

export interface InvocationComposerWorkflowProps {
  canInvoke: boolean;
  client: DataGroundClient;
  createIdempotencyKey?: () => string;
  disabledReason?: string;
  initialAlias?: string;
  inputSchema: unknown;
  onInvocationCreated?: (reference: InvocationReference) => void;
  onOpenInvocation?: (reference: InvocationReference) => void;
  target: AgentServiceInvocationTarget;
}

const aliasPattern = /^[a-z](?:[a-z0-9-]*[a-z0-9])?$/u;

function initialValues(schema?: InvocationComposerSchema): Record<string, string> {
  return Object.fromEntries(schema?.fields.map((field) => [field.key, ""]) ?? []);
}

export function validateInvocationComposerValues(
  alias: string,
  values: Readonly<Record<string, string>>,
  schema: InvocationComposerSchema,
): Record<string, string> {
  const errors = prepareInvocationInput(values, schema).errors;
  // Input properties may themselves be named alias. Reserve a key outside
  // the supported property-name grammar for the routing field's error.
  if (alias.length === 0) errors.$alias = "Alias is required.";
  else if (alias.length > 63 || !aliasPattern.test(alias)) {
    errors.$alias = "Use lowercase letters, numbers, and internal hyphens.";
  }
  return errors;
}

export function createInvocationIdempotencyKey(randomUUID: () => string): string {
  return `invoke:${randomUUID().replaceAll("-", "")}`;
}

function defaultIdempotencyKey(): string {
  if (!globalThis.crypto?.randomUUID)
    throw new Error("secure random identifier generation is unavailable");
  return createInvocationIdempotencyKey(() => globalThis.crypto.randomUUID());
}

export function invocationTargetKey(target: AgentServiceInvocationTarget): string {
  return `${target.isolationDomainId}:${target.serviceId}`;
}

export function InvocationComposerWorkflow({
  canInvoke,
  client,
  createIdempotencyKey = defaultIdempotencyKey,
  disabledReason,
  initialAlias = "stable",
  inputSchema,
  onInvocationCreated,
  onOpenInvocation,
  target,
}: InvocationComposerWorkflowProps) {
  const normalized = useMemo(() => normalizeInvocationComposerSchema(inputSchema), [inputSchema]);
  const targetKey = invocationTargetKey(target);
  const [state, setState] = useState<ComposerWorkflowState>({
    alias: initialAlias,
    submitting: false,
    targetKey,
    validationErrors: {},
    values: initialValues(normalized.ok ? normalized.schema : undefined),
  });
  const requestGeneration = useRef(0);
  const submissionLock = useRef<object | undefined>(undefined);

  useEffect(() => {
    requestGeneration.current++;
    submissionLock.current = undefined;
    setState({
      alias: initialAlias,
      submitting: false,
      targetKey,
      validationErrors: {},
      values: initialValues(normalized.ok ? normalized.schema : undefined),
    });
  }, [initialAlias, normalized, targetKey]);

  const submit = useCallback(async () => {
    if (
      !canInvoke ||
      !normalized.ok ||
      state.targetKey !== targetKey ||
      state.submitting ||
      state.accepted ||
      submissionLock.current
    )
      return;

    let attempt = state.recoveryAttempt;
    if (!attempt) {
      const errors = validateInvocationComposerValues(state.alias, state.values, normalized.schema);
      if (Object.keys(errors).length > 0) {
        setState((current) =>
          current.targetKey === targetKey ? { ...current, validationErrors: errors } : current,
        );
        return;
      }
      try {
        attempt = {
          alias: state.alias,
          idempotencyKey: createIdempotencyKey(),
          input: prepareInvocationInput(state.values, normalized.schema).input,
        };
      } catch {
        setState((current) =>
          current.targetKey === targetKey
            ? {
                ...current,
                error: {
                  code: "WORKBENCH_SECURE_RANDOM_UNAVAILABLE",
                  message:
                    "A secure invocation identifier could not be created. Refresh before retrying.",
                  retryable: false,
                },
              }
            : current,
        );
        return;
      }
    }

    const lock = {};
    submissionLock.current = lock;
    const generation = requestGeneration.current;
    setState((current) =>
      current.targetKey === targetKey
        ? {
            ...current,
            error: undefined,
            recoveryAttempt: attempt,
            submitting: true,
            validationErrors: {},
          }
        : current,
    );
    try {
      const result = await invokeAgentService(
        client,
        target,
        attempt.alias,
        attempt.input,
        attempt.idempotencyKey,
      );
      if (requestGeneration.current !== generation) return;
      if (result.ok) {
        setState((current) =>
          current.targetKey === targetKey
            ? {
                ...current,
                accepted: result.invocation,
                recoveryAttempt: undefined,
                submitting: false,
                values: {},
              }
            : current,
        );
        onInvocationCreated?.({
          invocationId: result.invocation.metadata.id,
          isolationDomainId: result.invocation.metadata.isolationDomainId,
        });
      } else {
        setState((current) =>
          current.targetKey === targetKey
            ? {
                ...current,
                error: result.error,
                recoveryAttempt: result.error.retryable ? attempt : undefined,
                submitting: false,
              }
            : current,
        );
      }
    } finally {
      if (submissionLock.current === lock) submissionLock.current = undefined;
    }
  }, [
    canInvoke,
    client,
    createIdempotencyKey,
    normalized,
    onInvocationCreated,
    state,
    target,
    targetKey,
  ]);

  const visible =
    state.targetKey === targetKey
      ? state
      : {
          alias: initialAlias,
          submitting: false,
          targetKey,
          validationErrors: {},
          values: initialValues(normalized.ok ? normalized.schema : undefined),
        };

  return (
    <InvocationComposer
      accepted={
        visible.accepted
          ? {
              invocationId: visible.accepted.metadata.id,
              operationId: visible.accepted.operationId,
              state: visible.accepted.state,
            }
          : undefined
      }
      alias={visible.alias}
      canInvoke={canInvoke}
      disabledReason={disabledReason}
      error={visible.error}
      isSubmitting={visible.submitting}
      onAliasChange={(alias) =>
        setState((current) =>
          current.targetKey === targetKey && !current.recoveryAttempt
            ? { ...current, alias, error: undefined }
            : current,
        )
      }
      onOpenInvocation={
        visible.accepted && onOpenInvocation
          ? () =>
              onOpenInvocation({
                invocationId: visible.accepted?.metadata.id ?? "",
                isolationDomainId: target.isolationDomainId,
              })
          : undefined
      }
      onSubmit={submit}
      onValueChange={(key, value) =>
        setState((current) =>
          current.targetKey === targetKey && !current.recoveryAttempt
            ? {
                ...current,
                error: undefined,
                values: { ...current.values, [key]: value },
              }
            : current,
        )
      }
      recoveryPending={visible.recoveryAttempt !== undefined}
      schema={normalized.ok ? normalized.schema : undefined}
      schemaError={normalized.ok ? undefined : normalized.error}
      target={target}
      validationErrors={visible.validationErrors}
      values={visible.recoveryAttempt ? {} : visible.values}
    />
  );
}
