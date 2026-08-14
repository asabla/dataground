import { ServiceRevisionPublish } from "@dataground/patterns";
import "@dataground/patterns/styles.css";
import { useCallback, useEffect, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import type { ServiceRevisionFailure, ServiceRevisionResource } from "./client";
import { type PublishedServiceRevisionResource, publishServiceRevision } from "./publicationClient";

interface PublicationAttempt {
  idempotencyKey: string;
  revision: ServiceRevisionResource;
}

interface PublicationState {
  confirming: boolean;
  error?: ServiceRevisionFailure;
  published?: PublishedServiceRevisionResource;
  recovering: boolean;
  recoveryAttempt?: PublicationAttempt;
  scopeKey: string;
  submitting: boolean;
}

export interface ServiceRevisionPublishWorkflowProps {
  canPublish: boolean;
  client: DataGroundClient;
  createIdempotencyKey?: () => string;
  disabledReason?: string;
  onAssignAlias?: (revision: PublishedServiceRevisionResource) => void;
  onPublished?: (revision: PublishedServiceRevisionResource) => void;
  revision: ServiceRevisionResource;
}

export function createPublicationIdempotencyKey(randomUUID: () => string): string {
  return `publication:${randomUUID().replaceAll("-", "")}`;
}

function defaultIdempotencyKey(): string {
  if (!globalThis.crypto?.randomUUID) {
    throw new Error("secure random identifier generation is unavailable");
  }
  return createPublicationIdempotencyKey(() => globalThis.crypto.randomUUID());
}

function copySchema(
  value: Record<string, unknown> | undefined,
): Record<string, unknown> | undefined {
  if (value === undefined) return undefined;
  return JSON.parse(JSON.stringify(value)) as Record<string, unknown>;
}

function snapshotRevision(revision: ServiceRevisionResource): ServiceRevisionResource {
  return {
    ...(revision.inputSchema === undefined
      ? undefined
      : { inputSchema: copySchema(revision.inputSchema) }),
    metadata: { ...revision.metadata },
    ...(revision.outputSchema === undefined
      ? undefined
      : { outputSchema: copySchema(revision.outputSchema) }),
    requiredCapabilities: [...revision.requiredCapabilities],
    revisionNumber: revision.revisionNumber,
    runtimeProfile: revision.runtimeProfile,
    serviceId: revision.serviceId,
    state: "draft",
  };
}

function revisionScopeKey(revision: ServiceRevisionResource): string {
  return [
    revision.metadata.isolationDomainId,
    revision.metadata.id,
    revision.metadata.generation,
    revision.metadata.version,
    revision.metadata.updatedAt,
  ].join("/");
}

export function ServiceRevisionPublishWorkflow({
  canPublish,
  client,
  createIdempotencyKey = defaultIdempotencyKey,
  disabledReason,
  onAssignAlias,
  onPublished,
  revision,
}: ServiceRevisionPublishWorkflowProps) {
  const scopeKey = revisionScopeKey(revision);
  const [state, setState] = useState<PublicationState>({
    confirming: false,
    recovering: false,
    scopeKey,
    submitting: false,
  });
  const requestGeneration = useRef(0);
  const submissionLock = useRef<object | undefined>(undefined);

  useEffect(() => {
    requestGeneration.current++;
    submissionLock.current = undefined;
    setState({ confirming: false, recovering: false, scopeKey, submitting: false });
  }, [scopeKey]);

  const confirm = useCallback(async () => {
    if (
      !canPublish ||
      state.scopeKey !== scopeKey ||
      state.submitting ||
      state.published ||
      submissionLock.current
    ) {
      return;
    }
    let attempt = state.recoveryAttempt;
    const recovering = attempt !== undefined;
    if (!attempt) {
      try {
        const idempotencyKey = createIdempotencyKey();
        attempt = { idempotencyKey, revision: snapshotRevision(revision) };
      } catch {
        setState((current) =>
          current.scopeKey === scopeKey
            ? {
                ...current,
                confirming: false,
                error: {
                  code: "WORKBENCH_PUBLICATION_REQUEST_UNAVAILABLE",
                  message:
                    "A stable publication request could not be prepared. Refresh before retrying.",
                  retryable: false,
                },
                recovering: false,
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
            confirming: true,
            error: undefined,
            recovering,
            recoveryAttempt: attempt,
            submitting: true,
          }
        : current,
    );
    try {
      const result = await publishServiceRevision(client, attempt.revision, attempt.idempotencyKey);
      if (requestGeneration.current !== generation) return;
      if (result.ok) {
        setState((current) =>
          current.scopeKey === scopeKey
            ? {
                ...current,
                confirming: false,
                published: result.revision,
                recovering: false,
                recoveryAttempt: undefined,
                submitting: false,
              }
            : current,
        );
        onPublished?.(result.revision);
      } else {
        const recoverable = result.error.retryable && result.error.outcomeUnknown === true;
        setState((current) =>
          current.scopeKey === scopeKey
            ? {
                ...current,
                confirming: false,
                error: result.error,
                recovering: recoverable,
                recoveryAttempt: recoverable ? attempt : undefined,
                submitting: false,
              }
            : current,
        );
      }
    } finally {
      if (submissionLock.current === lock) submissionLock.current = undefined;
    }
  }, [canPublish, client, createIdempotencyKey, onPublished, revision, scopeKey, state]);

  const visible =
    state.scopeKey === scopeKey
      ? state
      : { confirming: false, recovering: false, scopeKey, submitting: false };
  const published = visible.published;

  return (
    <ServiceRevisionPublish
      canPublish={canPublish}
      confirmationVisible={visible.confirming}
      createdAt={revision.metadata.createdAt}
      createdBy={revision.metadata.createdBy}
      disabledReason={disabledReason}
      error={visible.error}
      hasInputSchema={revision.inputSchema !== undefined}
      hasOutputSchema={revision.outputSchema !== undefined}
      isolationDomainId={revision.metadata.isolationDomainId}
      isSubmitting={visible.submitting}
      onAssignAlias={published && onAssignAlias ? () => onAssignAlias(published) : undefined}
      onConfirm={confirm}
      onDismissConfirmation={() =>
        setState((current) =>
          current.scopeKey === scopeKey && !current.submitting
            ? { ...current, confirming: false }
            : current,
        )
      }
      onRequestConfirmation={() =>
        setState((current) =>
          current.scopeKey === scopeKey && canPublish && !current.submitting
            ? { ...current, confirming: true, error: undefined }
            : current,
        )
      }
      published={
        published
          ? { publishedAt: published.publishedAt, version: published.metadata.version }
          : undefined
      }
      recoveryPending={visible.recovering}
      requiredCapabilities={revision.requiredCapabilities}
      revisionId={revision.metadata.id}
      revisionNumber={revision.revisionNumber}
      runtimeProfile={revision.runtimeProfile}
      serviceId={revision.serviceId}
      version={revision.metadata.version}
    />
  );
}
