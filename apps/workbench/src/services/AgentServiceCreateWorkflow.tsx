import { AgentServiceCreate } from "@dataground/patterns";
import "@dataground/patterns/styles.css";
import { useCallback, useEffect, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import {
  type AgentServiceCreateRequest,
  type AgentServiceFailure,
  type AgentServiceResource,
  createAgentService,
} from "./client";

interface ServiceCreationAttempt {
  idempotencyKey: string;
  request: AgentServiceCreateRequest;
}

interface ServiceCreateState {
  created?: AgentServiceResource;
  description: string;
  error?: AgentServiceFailure;
  name: string;
  recoveryAttempt?: ServiceCreationAttempt;
  scopeKey: string;
  submitting: boolean;
  validationErrors: { description?: string; name?: string };
}

export interface AgentServiceCreateWorkflowProps {
  canCreate: boolean;
  client: DataGroundClient;
  createIdempotencyKey?: () => string;
  disabledReason?: string;
  initialDescription?: string;
  initialName?: string;
  isolationDomainId: string;
  onOpenService?: (service: AgentServiceResource) => void;
  onServiceCreated?: (service: AgentServiceResource) => void;
}

export function validateAgentServiceCreateRequest(
  name: string,
  description: string,
): { description?: string; name?: string } {
  const errors: { description?: string; name?: string } = {};
  const encoder = new TextEncoder();
  const normalizedName = name.trim();
  if (normalizedName.length === 0) errors.name = "Service name is required.";
  else if (encoder.encode(normalizedName).byteLength > 128) {
    errors.name = "Service name must not exceed 128 bytes.";
  }
  if (encoder.encode(description).byteLength > 2048) {
    errors.description = "Description must not exceed 2,048 bytes.";
  }
  if (name.includes("\0")) errors.name = "Service name contains unsupported characters.";
  if (description.includes("\0")) {
    errors.description = "Description contains unsupported characters.";
  }
  return errors;
}

export function createServiceIdempotencyKey(randomUUID: () => string): string {
  return `service:${randomUUID().replaceAll("-", "")}`;
}

function defaultIdempotencyKey(): string {
  if (!globalThis.crypto?.randomUUID) {
    throw new Error("secure random identifier generation is unavailable");
  }
  return createServiceIdempotencyKey(() => globalThis.crypto.randomUUID());
}

export function AgentServiceCreateWorkflow({
  canCreate,
  client,
  createIdempotencyKey = defaultIdempotencyKey,
  disabledReason,
  initialDescription = "",
  initialName = "",
  isolationDomainId,
  onOpenService,
  onServiceCreated,
}: AgentServiceCreateWorkflowProps) {
  const [state, setState] = useState<ServiceCreateState>({
    description: initialDescription,
    name: initialName,
    scopeKey: isolationDomainId,
    submitting: false,
    validationErrors: {},
  });
  const requestGeneration = useRef(0);
  const submissionLock = useRef<object | undefined>(undefined);

  useEffect(() => {
    requestGeneration.current++;
    submissionLock.current = undefined;
    setState({
      description: initialDescription,
      name: initialName,
      scopeKey: isolationDomainId,
      submitting: false,
      validationErrors: {},
    });
  }, [initialDescription, initialName, isolationDomainId]);

  const submit = useCallback(async () => {
    if (
      !canCreate ||
      state.scopeKey !== isolationDomainId ||
      state.submitting ||
      state.created ||
      submissionLock.current
    ) {
      return;
    }
    let attempt = state.recoveryAttempt;
    if (!attempt) {
      const validationErrors = validateAgentServiceCreateRequest(state.name, state.description);
      if (Object.keys(validationErrors).length > 0) {
        setState((current) =>
          current.scopeKey === isolationDomainId ? { ...current, validationErrors } : current,
        );
        return;
      }
      try {
        attempt = {
          idempotencyKey: createIdempotencyKey(),
          request: {
            ...(state.description.length === 0 ? undefined : { description: state.description }),
            name: state.name.trim(),
          },
        };
      } catch {
        setState((current) =>
          current.scopeKey === isolationDomainId
            ? {
                ...current,
                error: {
                  code: "WORKBENCH_SECURE_RANDOM_UNAVAILABLE",
                  message:
                    "A secure service request identifier could not be created. Refresh before retrying.",
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
      current.scopeKey === isolationDomainId
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
      const result = await createAgentService(
        client,
        isolationDomainId,
        attempt.request,
        attempt.idempotencyKey,
      );
      if (requestGeneration.current !== generation) return;
      if (result.ok) {
        setState((current) =>
          current.scopeKey === isolationDomainId
            ? {
                ...current,
                created: result.service,
                description: "",
                name: "",
                recoveryAttempt: undefined,
                submitting: false,
              }
            : current,
        );
        onServiceCreated?.(result.service);
      } else {
        setState((current) =>
          current.scopeKey === isolationDomainId
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
  }, [canCreate, client, createIdempotencyKey, isolationDomainId, onServiceCreated, state]);

  const visible =
    state.scopeKey === isolationDomainId
      ? state
      : {
          description: initialDescription,
          name: initialName,
          scopeKey: isolationDomainId,
          submitting: false,
          validationErrors: {},
        };
  const createdService = visible.created;

  return (
    <AgentServiceCreate
      canCreate={canCreate}
      created={
        createdService
          ? {
              createdAt: createdService.metadata.createdAt,
              createdBy: createdService.metadata.createdBy,
              id: createdService.metadata.id,
              name: createdService.name,
              version: createdService.metadata.version,
            }
          : undefined
      }
      description={visible.description}
      disabledReason={disabledReason}
      error={visible.error}
      isolationDomainId={isolationDomainId}
      isSubmitting={visible.submitting}
      name={visible.name}
      onDescriptionChange={(description) =>
        setState((current) =>
          current.scopeKey === isolationDomainId && !current.recoveryAttempt
            ? {
                ...current,
                description,
                error: undefined,
                validationErrors: { ...current.validationErrors, description: undefined },
              }
            : current,
        )
      }
      onNameChange={(name) =>
        setState((current) =>
          current.scopeKey === isolationDomainId && !current.recoveryAttempt
            ? {
                ...current,
                error: undefined,
                name,
                validationErrors: { ...current.validationErrors, name: undefined },
              }
            : current,
        )
      }
      onOpenService={
        createdService && onOpenService ? () => onOpenService(createdService) : undefined
      }
      onSubmit={submit}
      recoveryPending={visible.recoveryAttempt !== undefined}
      validationErrors={visible.validationErrors}
    />
  );
}
