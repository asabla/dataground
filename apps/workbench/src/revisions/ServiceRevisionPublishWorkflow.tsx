import { ServiceRevisionPublish } from "@dataground/patterns";
import { Button, StatusBadge } from "@dataground/ui";
import "@dataground/patterns/styles.css";
import { useCallback, useEffect, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import type { ServiceRevisionFailure, ServiceRevisionResource } from "./client";
import {
  observeServiceRevisionPublication,
  type PublicationOperationReference,
  type PublishedServiceRevisionResource,
  publishServiceRevision,
} from "./publicationClient";

interface PublicationAttempt {
  idempotencyKey: string;
  revision: ServiceRevisionResource;
}

interface PublicationState {
  client: DataGroundClient;
  operation?: PublicationOperationReference;
  acceptedAttempt?: PublicationAttempt;
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
    client,
    confirming: false,
    recovering: false,
    scopeKey,
    submitting: false,
  });
  const requestGeneration = useRef(0);
  const feedback = useRef<HTMLElement>(null);
  const submissionLock = useRef<object | undefined>(undefined);

  useEffect(() => {
    requestGeneration.current++;
    submissionLock.current = undefined;
    setState({ client, confirming: false, recovering: false, scopeKey, submitting: false });
    return () => {
      requestGeneration.current++;
      submissionLock.current = undefined;
    };
  }, [client, scopeKey]);

  const confirm = useCallback(async () => {
    if (
      !canPublish ||
      state.scopeKey !== scopeKey ||
      state.client !== client ||
      state.operation ||
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
          current.scopeKey === scopeKey && current.client === client
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
      current.scopeKey === scopeKey && current.client === client
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
      if (result.ok && result.operation) {
        setState((current) =>
          current.scopeKey === scopeKey && current.client === client
            ? {
                ...current,
                operation: result.operation,
                acceptedAttempt: attempt,
                confirming: false,
                error: undefined,
                recovering: false,
                recoveryAttempt: undefined,
                submitting: false,
              }
            : current,
        );
      } else if (result.ok && result.revision) {
        setState((current) =>
          current.scopeKey === scopeKey && current.client === client
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
      } else if (!result.ok) {
        const recoverable = result.error.retryable && result.error.outcomeUnknown === true;
        setState((current) =>
          current.scopeKey === scopeKey && current.client === client
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

  const observe = useCallback(async () => {
    if (
      state.client !== client ||
      state.scopeKey !== scopeKey ||
      !state.operation ||
      !state.acceptedAttempt ||
      state.submitting ||
      submissionLock.current
    )
      return;
    const lock = {};
    submissionLock.current = lock;
    const generation = requestGeneration.current;
    setState((current) =>
      current.client === client && current.scopeKey === scopeKey
        ? { ...current, submitting: true, error: undefined }
        : current,
    );
    try {
      const result = await observeServiceRevisionPublication(
        client,
        state.acceptedAttempt.revision,
        state.operation,
      );
      if (generation !== requestGeneration.current) return;
      if (result.ok) {
        setState((current) =>
          current.client === client && current.scopeKey === scopeKey
            ? {
                ...current,
                operation: result.operation,
                published: result.revision,
                error: undefined,
                submitting: false,
              }
            : current,
        );
        if (result.revision) onPublished?.(result.revision);
      } else {
        setState((current) =>
          current.client === client && current.scopeKey === scopeKey
            ? { ...current, error: result.error, submitting: false }
            : current,
        );
      }
    } finally {
      if (submissionLock.current === lock) submissionLock.current = undefined;
    }
  }, [client, scopeKey, state, onPublished]);

  const visible =
    state.scopeKey === scopeKey && state.client === client
      ? state
      : { client, confirming: false, recovering: false, scopeKey, submitting: false };
  const published = visible.published;
  const operation = visible.operation;
  useEffect(() => {
    if ((operation || published) && document.activeElement === document.body)
      feedback.current?.focus();
  }, [operation, published]);
  if (operation && !published)
    return (
      <section
        ref={feedback}
        tabIndex={-1}
        aria-label="Revision publication"
        className="product-workflow__inspection"
      >
        <h2>Publication accepted</h2>
        <p>
          The revision is not ready for routing until its operation and exact published definition
          are confirmed.
        </p>
        <p>
          Revision <code>{revision.metadata.id}</code>
        </p>
        <p>
          Operation <code>{operation.id}</code>
        </p>
        <p>
          Last observed state:{" "}
          <StatusBadge
            tone={
              operation.state === "failed"
                ? "critical"
                : operation.state === "cancelled"
                  ? "neutral"
                  : "active"
            }
          >
            {operation.state}
          </StatusBadge>
        </p>
        {operation.state === "failed" && (
          <p>
            Publication failed. The accepted operation must be reconciled before this revision can
            be routed.
          </p>
        )}
        {operation.state === "cancelled" && (
          <p>Publication was cancelled. This receipt does not authorize routing.</p>
        )}
        {visible.error && (
          <div role="alert">
            <p>{visible.error.message}</p>
          </div>
        )}
        {visible.submitting && <p role="status">Checking publication…</p>}
        <Button aria-disabled={visible.submitting} onPress={() => void observe()} variant="quiet">
          Check publication
        </Button>
      </section>
    );

  return (
    <section ref={feedback} tabIndex={-1} aria-label="Revision publication">
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
            current.scopeKey === scopeKey && current.client === client && !current.submitting
              ? { ...current, confirming: false }
              : current,
          )
        }
        onRequestConfirmation={() =>
          setState((current) =>
            current.scopeKey === scopeKey &&
            current.client === client &&
            canPublish &&
            !current.submitting
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
    </section>
  );
}
