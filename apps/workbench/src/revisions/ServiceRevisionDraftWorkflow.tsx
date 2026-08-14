import { ServiceRevisionDraft } from "@dataground/patterns";
import "@dataground/patterns/styles.css";
import { useCallback, useEffect, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import {
  createServiceRevision,
  type ServiceRevisionCreateRequest,
  type ServiceRevisionFailure,
  type ServiceRevisionResource,
} from "./client";

interface RevisionDraftAttempt {
  idempotencyKey: string;
  request: ServiceRevisionCreateRequest;
}

export interface ServiceRevisionDraftValues {
  inputSchema: string;
  outputSchema: string;
  requiredCapabilities: string;
  runtimeProfile: string;
}

export interface ServiceRevisionDraftValidation {
  errors: Partial<Record<keyof ServiceRevisionDraftValues, string>>;
  request?: ServiceRevisionCreateRequest;
}

interface RevisionDraftState extends ServiceRevisionDraftValues {
  created?: ServiceRevisionResource;
  error?: ServiceRevisionFailure;
  recoveryAttempt?: RevisionDraftAttempt;
  scopeKey: string;
  submitting: boolean;
  validationErrors: Partial<Record<keyof ServiceRevisionDraftValues, string>>;
}

export interface ServiceRevisionDraftWorkflowProps {
  canCreateRevision: boolean;
  client: DataGroundClient;
  createIdempotencyKey?: () => string;
  disabledReason?: string;
  initialInputSchema?: string;
  initialOutputSchema?: string;
  initialRequiredCapabilities?: string;
  initialRuntimeProfile?: string;
  isolationDomainId: string;
  onOpenRevision?: (revision: ServiceRevisionResource) => void;
  onRevisionCreated?: (revision: ServiceRevisionResource) => void;
  serviceId: string;
}

function parseSchema(
  value: string,
  field: "inputSchema" | "outputSchema",
  errors: Partial<Record<keyof ServiceRevisionDraftValues, string>>,
): Record<string, unknown> | undefined {
  if (value.trim().length === 0) return undefined;
  if (new TextEncoder().encode(value).byteLength > 1 << 20) {
    errors[field] = "Schema exceeds the API request limit.";
    return undefined;
  }
  try {
    const parsed: unknown = JSON.parse(value);
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
      errors[field] = "Schema must be a JSON object.";
      return undefined;
    }
    return parsed as Record<string, unknown>;
  } catch {
    errors[field] = "Schema must contain valid JSON.";
    return undefined;
  }
}

export function validateServiceRevisionDraft(
  values: ServiceRevisionDraftValues,
): ServiceRevisionDraftValidation {
  const errors: Partial<Record<keyof ServiceRevisionDraftValues, string>> = {};
  const encoder = new TextEncoder();
  const runtimeProfile = values.runtimeProfile.trim();
  if (runtimeProfile.length === 0) errors.runtimeProfile = "Runtime profile is required.";
  else if (encoder.encode(runtimeProfile).byteLength > 128) {
    errors.runtimeProfile = "Runtime profile must not exceed 128 bytes.";
  } else if (runtimeProfile.includes("\0")) {
    errors.runtimeProfile = "Runtime profile contains unsupported characters.";
  }

  const requiredCapabilities = values.requiredCapabilities
    .split(/[\n,]/u)
    .map((capability) => capability.trim())
    .filter(Boolean);
  const duplicateCapability = requiredCapabilities.some(
    (capability, index) => requiredCapabilities.indexOf(capability) !== index,
  );
  if (
    requiredCapabilities.some(
      (capability) => encoder.encode(capability).byteLength > 128 || capability.includes("\0"),
    )
  ) {
    errors.requiredCapabilities = "Each capability must be valid text no longer than 128 bytes.";
  } else if (duplicateCapability) {
    errors.requiredCapabilities = "Capability names must be unique.";
  }

  const inputSchema = parseSchema(values.inputSchema, "inputSchema", errors);
  const outputSchema = parseSchema(values.outputSchema, "outputSchema", errors);
  const request = {
    ...(inputSchema === undefined ? undefined : { inputSchema }),
    ...(outputSchema === undefined ? undefined : { outputSchema }),
    requiredCapabilities,
    runtimeProfile,
  };
  try {
    if (encoder.encode(JSON.stringify(request)).byteLength > 1 << 20) {
      errors.inputSchema = "The complete revision definition exceeds the API request limit.";
      errors.outputSchema = "The complete revision definition exceeds the API request limit.";
    }
  } catch {
    errors.inputSchema = "The schema could not be represented as JSON.";
    errors.outputSchema = "The schema could not be represented as JSON.";
  }
  return Object.keys(errors).length === 0 ? { errors, request } : { errors };
}

export function createRevisionIdempotencyKey(randomUUID: () => string): string {
  return `revision:${randomUUID().replaceAll("-", "")}`;
}

function defaultIdempotencyKey(): string {
  if (!globalThis.crypto?.randomUUID) {
    throw new Error("secure random identifier generation is unavailable");
  }
  return createRevisionIdempotencyKey(() => globalThis.crypto.randomUUID());
}

function initialValues({
  initialInputSchema,
  initialOutputSchema,
  initialRequiredCapabilities,
  initialRuntimeProfile,
}: Pick<
  ServiceRevisionDraftWorkflowProps,
  | "initialInputSchema"
  | "initialOutputSchema"
  | "initialRequiredCapabilities"
  | "initialRuntimeProfile"
>): ServiceRevisionDraftValues {
  return {
    inputSchema: initialInputSchema ?? "",
    outputSchema: initialOutputSchema ?? "",
    requiredCapabilities: initialRequiredCapabilities ?? "",
    runtimeProfile: initialRuntimeProfile ?? "reference/v1",
  };
}

export function ServiceRevisionDraftWorkflow({
  canCreateRevision,
  client,
  createIdempotencyKey = defaultIdempotencyKey,
  disabledReason,
  initialInputSchema,
  initialOutputSchema,
  initialRequiredCapabilities,
  initialRuntimeProfile,
  isolationDomainId,
  onOpenRevision,
  onRevisionCreated,
  serviceId,
}: ServiceRevisionDraftWorkflowProps) {
  const scopeKey = `${isolationDomainId}/${serviceId}`;
  const [state, setState] = useState<RevisionDraftState>({
    ...initialValues({
      initialInputSchema,
      initialOutputSchema,
      initialRequiredCapabilities,
      initialRuntimeProfile,
    }),
    scopeKey,
    submitting: false,
    validationErrors: {},
  });
  const requestGeneration = useRef(0);
  const submissionLock = useRef<object | undefined>(undefined);

  useEffect(() => {
    requestGeneration.current++;
    submissionLock.current = undefined;
    setState({
      ...initialValues({
        initialInputSchema,
        initialOutputSchema,
        initialRequiredCapabilities,
        initialRuntimeProfile,
      }),
      scopeKey,
      submitting: false,
      validationErrors: {},
    });
  }, [
    initialInputSchema,
    initialOutputSchema,
    initialRequiredCapabilities,
    initialRuntimeProfile,
    scopeKey,
  ]);

  const submit = useCallback(async () => {
    if (
      !canCreateRevision ||
      state.scopeKey !== scopeKey ||
      state.submitting ||
      state.created ||
      submissionLock.current
    ) {
      return;
    }
    let attempt = state.recoveryAttempt;
    if (!attempt) {
      const validation = validateServiceRevisionDraft(state);
      if (!validation.request) {
        setState((current) =>
          current.scopeKey === scopeKey
            ? { ...current, validationErrors: validation.errors }
            : current,
        );
        return;
      }
      try {
        attempt = {
          idempotencyKey: createIdempotencyKey(),
          request: validation.request,
        };
      } catch {
        setState((current) =>
          current.scopeKey === scopeKey
            ? {
                ...current,
                error: {
                  code: "WORKBENCH_SECURE_RANDOM_UNAVAILABLE",
                  message:
                    "A secure revision request identifier could not be created. Refresh before retrying.",
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
      current.scopeKey === scopeKey
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
      const result = await createServiceRevision(
        client,
        isolationDomainId,
        serviceId,
        attempt.request,
        attempt.idempotencyKey,
      );
      if (requestGeneration.current !== generation) return;
      if (result.ok) {
        setState((current) =>
          current.scopeKey === scopeKey
            ? {
                ...current,
                created: result.revision,
                inputSchema: "",
                outputSchema: "",
                recoveryAttempt: undefined,
                requiredCapabilities: "",
                runtimeProfile: "",
                submitting: false,
              }
            : current,
        );
        onRevisionCreated?.(result.revision);
      } else {
        setState((current) =>
          current.scopeKey === scopeKey
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
    canCreateRevision,
    client,
    createIdempotencyKey,
    isolationDomainId,
    onRevisionCreated,
    scopeKey,
    serviceId,
    state,
  ]);

  const visible =
    state.scopeKey === scopeKey
      ? state
      : {
          ...initialValues({
            initialInputSchema,
            initialOutputSchema,
            initialRequiredCapabilities,
            initialRuntimeProfile,
          }),
          scopeKey,
          submitting: false,
          validationErrors: {},
        };
  const createdRevision = visible.created;
  const change = (field: keyof ServiceRevisionDraftValues, value: string) =>
    setState((current) =>
      current.scopeKey === scopeKey && !current.recoveryAttempt
        ? {
            ...current,
            [field]: value,
            error: undefined,
            validationErrors: { ...current.validationErrors, [field]: undefined },
          }
        : current,
    );

  return (
    <ServiceRevisionDraft
      canCreateRevision={canCreateRevision}
      created={
        createdRevision
          ? {
              createdAt: createdRevision.metadata.createdAt,
              createdBy: createdRevision.metadata.createdBy,
              id: createdRevision.metadata.id,
              revisionNumber: createdRevision.revisionNumber,
              runtimeProfile: createdRevision.runtimeProfile,
              state: createdRevision.state,
              version: createdRevision.metadata.version,
            }
          : undefined
      }
      disabledReason={disabledReason}
      error={visible.error}
      inputSchema={visible.inputSchema}
      isolationDomainId={isolationDomainId}
      isSubmitting={visible.submitting}
      onInputSchemaChange={(value) => change("inputSchema", value)}
      onOpenRevision={
        createdRevision && onOpenRevision ? () => onOpenRevision(createdRevision) : undefined
      }
      onOutputSchemaChange={(value) => change("outputSchema", value)}
      onRequiredCapabilitiesChange={(value) => change("requiredCapabilities", value)}
      onRuntimeProfileChange={(value) => change("runtimeProfile", value)}
      onSubmit={submit}
      outputSchema={visible.outputSchema}
      recoveryPending={visible.recoveryAttempt !== undefined}
      requiredCapabilities={visible.requiredCapabilities}
      runtimeProfile={visible.runtimeProfile}
      serviceId={serviceId}
      validationErrors={visible.validationErrors}
    />
  );
}
