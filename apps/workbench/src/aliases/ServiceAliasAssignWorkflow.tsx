import { ServiceAliasAssign } from "@dataground/patterns";
import "@dataground/patterns/styles.css";
import { useCallback, useEffect, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import type { PublishedServiceRevisionResource } from "../revisions/publicationClient";
import {
  assignServiceAlias,
  isServiceAliasAssignmentScopeValid,
  type ServiceAliasFailure,
  type ServiceAliasResource,
} from "./aliasClient";

interface AliasAttempt {
  aliasName: string;
  current?: ServiceAliasResource;
  idempotencyKey: string;
  revision: PublishedServiceRevisionResource;
}

interface AliasState {
  aliasName: string;
  aliasValidationError?: string;
  assigned?: ServiceAliasResource;
  confirming: boolean;
  error?: ServiceAliasFailure;
  recoveryAttempt?: AliasAttempt;
  scopeKey: string;
  submitting: boolean;
}

export interface ServiceAliasAssignWorkflowProps {
  canAssign: boolean;
  client: DataGroundClient;
  createIdempotencyKey?: () => string;
  currentAlias?: ServiceAliasResource;
  disabledReason?: string;
  initialAliasName?: string;
  onAliasAssigned?: (alias: ServiceAliasResource) => void;
  onComposeInvocation?: (alias: ServiceAliasResource) => void;
  revision: PublishedServiceRevisionResource;
}

const aliasPattern = /^[a-z](?:[a-z0-9-]*[a-z0-9])?$/u;

export function validateServiceAliasName(value: string): string | undefined {
  const normalized = value.trim();
  if (normalized.length === 0) return "Alias is required.";
  if (new TextEncoder().encode(normalized).byteLength > 63) {
    return "Alias must not exceed 63 bytes.";
  }
  if (!aliasPattern.test(normalized)) {
    return "Alias must use lowercase letters, numbers, or internal hyphens.";
  }
  return undefined;
}

export function createAliasIdempotencyKey(randomUUID: () => string): string {
  return `alias:${randomUUID().replaceAll("-", "")}`;
}

function defaultIdempotencyKey(): string {
  if (!globalThis.crypto?.randomUUID) {
    throw new Error("secure random identifier generation is unavailable");
  }
  return createAliasIdempotencyKey(() => globalThis.crypto.randomUUID());
}

function copySchema(
  value: Record<string, unknown> | undefined,
): Record<string, unknown> | undefined {
  if (value === undefined) return undefined;
  return JSON.parse(JSON.stringify(value)) as Record<string, unknown>;
}

function snapshotRevision(
  revision: PublishedServiceRevisionResource,
): PublishedServiceRevisionResource {
  return {
    ...(revision.inputSchema === undefined
      ? undefined
      : { inputSchema: copySchema(revision.inputSchema) }),
    metadata: { ...revision.metadata },
    ...(revision.outputSchema === undefined
      ? undefined
      : { outputSchema: copySchema(revision.outputSchema) }),
    publishedAt: revision.publishedAt,
    requiredCapabilities: [...revision.requiredCapabilities],
    revisionNumber: revision.revisionNumber,
    runtimeProfile: revision.runtimeProfile,
    serviceId: revision.serviceId,
    state: "published",
  };
}

function snapshotAlias(alias: ServiceAliasResource | undefined): ServiceAliasResource | undefined {
  return alias
    ? {
        metadata: { ...alias.metadata },
        name: alias.name,
        revisionId: alias.revisionId,
        serviceId: alias.serviceId,
      }
    : undefined;
}

function aliasScopeKey(
  revision: PublishedServiceRevisionResource,
  current: ServiceAliasResource | undefined,
): string {
  return JSON.stringify([
    revision.metadata.isolationDomainId,
    revision.serviceId,
    revision.metadata.id,
    revision.metadata.generation,
    revision.metadata.version,
    revision.metadata.updatedAt,
    current?.metadata.id ?? "new",
    current?.name ?? "",
    current?.revisionId ?? "",
    current?.metadata.generation ?? 0,
    current?.metadata.version ?? 0,
    current?.metadata.updatedAt ?? "",
  ]);
}

export function ServiceAliasAssignWorkflow({
  canAssign,
  client,
  createIdempotencyKey = defaultIdempotencyKey,
  currentAlias,
  disabledReason,
  initialAliasName,
  onAliasAssigned,
  onComposeInvocation,
  revision,
}: ServiceAliasAssignWorkflowProps) {
  const scopeKey = aliasScopeKey(revision, currentAlias);
  const defaultAliasName = currentAlias?.name ?? initialAliasName ?? "stable";
  const observedScopeValid = isServiceAliasAssignmentScopeValid(
    revision,
    currentAlias?.name ?? "stable",
    currentAlias,
  );
  const [state, setState] = useState<AliasState>({
    aliasName: defaultAliasName,
    confirming: false,
    scopeKey,
    submitting: false,
  });
  const requestGeneration = useRef(0);
  const submissionLock = useRef<object | undefined>(undefined);
  const activeScopeKey = useRef(scopeKey);
  activeScopeKey.current = scopeKey;

  useEffect(() => {
    requestGeneration.current++;
    submissionLock.current = undefined;
    setState({
      aliasName: defaultAliasName,
      confirming: false,
      scopeKey,
      submitting: false,
    });
  }, [defaultAliasName, scopeKey]);

  const confirm = useCallback(async () => {
    if (
      !canAssign ||
      !observedScopeValid ||
      state.scopeKey !== scopeKey ||
      state.submitting ||
      state.assigned ||
      currentAlias?.revisionId === revision.metadata.id ||
      submissionLock.current
    ) {
      return;
    }
    let attempt = state.recoveryAttempt;
    if (!attempt) {
      const aliasName = currentAlias?.name ?? state.aliasName.trim();
      const aliasValidationError = validateServiceAliasName(aliasName);
      if (aliasValidationError) {
        setState((current) =>
          current.scopeKey === scopeKey
            ? { ...current, aliasValidationError, confirming: false }
            : current,
        );
        return;
      }
      try {
        attempt = {
          aliasName,
          current: snapshotAlias(currentAlias),
          idempotencyKey: createIdempotencyKey(),
          revision: snapshotRevision(revision),
        };
      } catch {
        setState((current) =>
          current.scopeKey === scopeKey
            ? {
                ...current,
                confirming: false,
                error: {
                  code: "WORKBENCH_ALIAS_REQUEST_UNAVAILABLE",
                  message:
                    "A stable routing request could not be prepared. Refresh before retrying.",
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
            aliasName: attempt.aliasName,
            aliasValidationError: undefined,
            error: undefined,
            recoveryAttempt: attempt,
            submitting: true,
          }
        : current,
    );
    try {
      const result = await assignServiceAlias(
        client,
        attempt.revision,
        attempt.aliasName,
        attempt.current,
        attempt.idempotencyKey,
      );
      if (requestGeneration.current !== generation || activeScopeKey.current !== scopeKey) return;
      if (result.ok) {
        setState((current) =>
          current.scopeKey === scopeKey
            ? {
                ...current,
                assigned: result.alias,
                confirming: false,
                recoveryAttempt: undefined,
                submitting: false,
              }
            : current,
        );
        onAliasAssigned?.(result.alias);
      } else {
        setState((current) =>
          current.scopeKey === scopeKey
            ? {
                ...current,
                confirming: false,
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
    canAssign,
    client,
    createIdempotencyKey,
    currentAlias,
    observedScopeValid,
    onAliasAssigned,
    revision,
    scopeKey,
    state,
  ]);

  const visible =
    state.scopeKey === scopeKey
      ? state
      : {
          aliasName: defaultAliasName,
          confirming: false,
          scopeKey,
          submitting: false,
        };
  const routedAlias =
    visible.assigned ??
    (observedScopeValid && currentAlias?.revisionId === revision.metadata.id
      ? currentAlias
      : undefined);

  return (
    <ServiceAliasAssign
      aliasName={visible.aliasName}
      aliasValidationError={visible.aliasValidationError}
      assigned={
        visible.assigned
          ? {
              generation: visible.assigned.metadata.generation,
              id: visible.assigned.metadata.id,
              name: visible.assigned.name,
              revisionId: visible.assigned.revisionId,
              updatedAt: visible.assigned.metadata.updatedAt,
              version: visible.assigned.metadata.version,
            }
          : undefined
      }
      blockedReason={
        observedScopeValid
          ? undefined
          : "The published revision or observed alias state could not be verified. Refresh from an authorized source before changing routing."
      }
      canAssign={canAssign}
      confirmationVisible={visible.confirming}
      currentAliasId={currentAlias?.metadata.id}
      currentRevisionId={currentAlias?.revisionId}
      currentVersion={currentAlias?.metadata.version}
      disabledReason={disabledReason}
      error={visible.error}
      isolationDomainId={revision.metadata.isolationDomainId}
      isSubmitting={visible.submitting}
      onAliasNameChange={(value) =>
        setState((current) =>
          current.scopeKey === scopeKey && !current.recoveryAttempt && currentAlias === undefined
            ? {
                ...current,
                aliasName: value,
                aliasValidationError: undefined,
                confirming: false,
                error: undefined,
              }
            : current,
        )
      }
      onComposeInvocation={
        routedAlias && onComposeInvocation ? () => onComposeInvocation(routedAlias) : undefined
      }
      onConfirm={confirm}
      onDismissConfirmation={() =>
        setState((current) =>
          current.scopeKey === scopeKey && !current.submitting
            ? { ...current, confirming: false }
            : current,
        )
      }
      onRequestConfirmation={() => {
        const aliasName = currentAlias?.name ?? visible.aliasName.trim();
        const aliasValidationError = validateServiceAliasName(aliasName);
        setState((current) =>
          current.scopeKey === scopeKey && canAssign && observedScopeValid && !current.submitting
            ? {
                ...current,
                aliasName,
                aliasValidationError,
                confirming: aliasValidationError === undefined,
                error: undefined,
              }
            : current,
        );
      }}
      recoveryPending={visible.recoveryAttempt !== undefined}
      revisionNumber={revision.revisionNumber}
      serviceId={revision.serviceId}
      targetRevisionId={revision.metadata.id}
      targetVersion={revision.metadata.version}
    />
  );
}
