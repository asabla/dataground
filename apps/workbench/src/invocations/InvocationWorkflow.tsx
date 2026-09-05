import { InvocationStatus, isInvocationCancellable } from "@dataground/patterns";
import "@dataground/patterns/styles.css";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { InvocationArtifactReference } from "../artifacts";
import type { DataGroundClient } from "../contracts/client";
import {
  cancelInvocation,
  type InvocationFailure,
  type InvocationOperationResource,
  type InvocationReference,
  type InvocationStatusResource,
  type InvocationStatusResult,
  readInvocationStatus,
} from "./client";

import { InvocationResultWorkflow } from "./InvocationResultWorkflow";

interface CancellationAttempt {
  idempotencyKey: string;
}

interface InvocationWorkflowState {
  cancellationConfirmationVisible: boolean;
  cancelling: boolean;
  error?: InvocationFailure;
  errorContext?: "cancellation" | "operation" | "read";
  invocation?: InvocationStatusResource;
  loading: boolean;
  operation?: InvocationOperationResource;
  recoveryAttempt?: CancellationAttempt;
  referenceKey: string;
}

type InvocationWorkflowAction =
  | { referenceKey: string; type: "load-started" }
  | { referenceKey: string; result: InvocationStatusResult; type: "load-finished" }
  | { referenceKey: string; type: "cancellation-requested" }
  | { referenceKey: string; type: "cancellation-dismissed" }
  | { attempt: CancellationAttempt; referenceKey: string; type: "cancellation-started" }
  | { error: InvocationFailure; referenceKey: string; type: "cancellation-blocked" }
  | {
      attempt: CancellationAttempt;
      referenceKey: string;
      result: InvocationStatusResult;
      type: "cancellation-finished";
    };

export interface InvocationWorkflowProps {
  canCancel: boolean;
  client: DataGroundClient;
  createIdempotencyKey?: () => string;
  disabledReason?: string;
  onInspectArtifact?: (reference: InvocationArtifactReference) => void;
  reference: InvocationReference;
}

function applySnapshot(
  state: InvocationWorkflowState,
  result: Extract<InvocationStatusResult, { ok: true }>,
): InvocationWorkflowState {
  return {
    cancellationConfirmationVisible: false,
    cancelling: false,
    error: result.operationError,
    errorContext: result.operationError ? "operation" : undefined,
    invocation: result.invocation,
    loading: false,
    operation: result.operation,
    referenceKey: state.referenceKey,
    recoveryAttempt: undefined,
  };
}

function operationSupportsNewCancellation(operation: InvocationOperationResource): boolean {
  return operation.kind === "invocation-execution" && operation.command === "invoke";
}

export function invocationWorkflowReducer(
  state: InvocationWorkflowState,
  action: InvocationWorkflowAction,
): InvocationWorkflowState {
  switch (action.type) {
    case "load-started":
      if (state.referenceKey !== action.referenceKey) {
        return {
          cancellationConfirmationVisible: false,
          cancelling: false,
          loading: true,
          referenceKey: action.referenceKey,
        };
      }
      return {
        ...state,
        cancellationConfirmationVisible: false,
        error: undefined,
        errorContext: undefined,
        loading: true,
      };
    case "load-finished":
      if (state.referenceKey !== action.referenceKey) {
        return state;
      }
      return action.result.ok
        ? applySnapshot(state, action.result)
        : {
            ...state,
            error: action.result.error,
            errorContext: "read",
            loading: false,
          };
    case "cancellation-requested":
      if (
        state.referenceKey !== action.referenceKey ||
        !state.invocation ||
        !state.operation ||
        !operationSupportsNewCancellation(state.operation) ||
        !isInvocationCancellable(state.invocation.state) ||
        state.loading ||
        state.cancelling ||
        state.error
      ) {
        return state;
      }
      return { ...state, cancellationConfirmationVisible: true };
    case "cancellation-dismissed":
      if (state.referenceKey !== action.referenceKey || state.cancelling) {
        return state;
      }
      return { ...state, cancellationConfirmationVisible: false };
    case "cancellation-started":
      if (state.referenceKey !== action.referenceKey) {
        return state;
      }
      return {
        ...state,
        cancellationConfirmationVisible: false,
        cancelling: true,
        error: undefined,
        errorContext: undefined,
        recoveryAttempt: action.attempt,
      };
    case "cancellation-blocked":
      if (state.referenceKey !== action.referenceKey) {
        return state;
      }
      return {
        ...state,
        cancellationConfirmationVisible: false,
        cancelling: false,
        error: action.error,
        errorContext: "cancellation",
        recoveryAttempt: undefined,
      };
    case "cancellation-finished":
      if (state.referenceKey !== action.referenceKey) {
        return state;
      }
      if (action.result.ok) {
        return applySnapshot(state, action.result);
      }
      return {
        ...state,
        cancellationConfirmationVisible: false,
        cancelling: false,
        error: action.result.error,
        errorContext: "cancellation",
        recoveryAttempt: action.result.error.retryable ? action.attempt : undefined,
      };
  }
}

export function createCancellationIdempotencyKey(randomUUID: () => string): string {
  return `cancel:${randomUUID().replaceAll("-", "")}`;
}

function defaultIdempotencyKey(): string {
  if (!globalThis.crypto?.randomUUID) {
    throw new Error("secure random identifier generation is unavailable");
  }
  return createCancellationIdempotencyKey(() => globalThis.crypto.randomUUID());
}

export function invocationReferenceKey(reference: InvocationReference): string {
  return `${reference.isolationDomainId}:${reference.invocationId}`;
}

export function InvocationWorkflow({
  canCancel,
  client,
  createIdempotencyKey = defaultIdempotencyKey,
  disabledReason,
  onInspectArtifact,
  reference,
}: InvocationWorkflowProps) {
  const currentReferenceKey = invocationReferenceKey(reference);
  const [state, setState] = useState<InvocationWorkflowState>({
    cancellationConfirmationVisible: false,
    cancelling: false,
    loading: true,
    referenceKey: currentReferenceKey,
  });
  const requestGeneration = useRef(0);
  const cancellationLock = useRef<object | undefined>(undefined);
  const stableReference = useMemo(
    () => ({
      invocationId: reference.invocationId,
      isolationDomainId: reference.isolationDomainId,
    }),
    [reference.invocationId, reference.isolationDomainId],
  );

  const dispatch = useCallback((action: InvocationWorkflowAction) => {
    setState((current) => invocationWorkflowReducer(current, action));
  }, []);

  const refresh = useCallback(async () => {
    const generation = ++requestGeneration.current;
    const referenceKey = invocationReferenceKey(stableReference);
    dispatch({ referenceKey, type: "load-started" });
    const result = await readInvocationStatus(client, stableReference);
    if (requestGeneration.current === generation) {
      dispatch({ referenceKey, result, type: "load-finished" });
    }
  }, [client, dispatch, stableReference]);

  useEffect(() => {
    void refresh();
    return () => {
      requestGeneration.current++;
      cancellationLock.current = undefined;
    };
  }, [refresh]);

  const submitCancellation = useCallback(async () => {
    const referenceKey = invocationReferenceKey(stableReference);
    if (
      state.referenceKey !== referenceKey ||
      !state.invocation ||
      !state.operation ||
      (!state.recoveryAttempt && !operationSupportsNewCancellation(state.operation)) ||
      !isInvocationCancellable(state.invocation.state) ||
      state.loading ||
      state.cancelling ||
      cancellationLock.current !== undefined ||
      (state.error !== undefined && state.recoveryAttempt === undefined)
    ) {
      return;
    }
    const lock = {};
    cancellationLock.current = lock;
    let attempt: CancellationAttempt;
    try {
      attempt = state.recoveryAttempt ?? { idempotencyKey: createIdempotencyKey() };
    } catch {
      dispatch({
        error: {
          code: "WORKBENCH_SECURE_RANDOM_UNAVAILABLE",
          message:
            "A secure cancellation identifier could not be created. Refresh before retrying.",
          retryable: false,
        },
        referenceKey,
        type: "cancellation-blocked",
      });
      if (cancellationLock.current === lock) {
        cancellationLock.current = undefined;
      }
      return;
    }
    const generation = requestGeneration.current;
    dispatch({ attempt, referenceKey, type: "cancellation-started" });
    try {
      const result = await cancelInvocation(client, stableReference, attempt.idempotencyKey);
      if (requestGeneration.current === generation) {
        dispatch({ attempt, referenceKey, result, type: "cancellation-finished" });
      }
    } finally {
      if (cancellationLock.current === lock) {
        cancellationLock.current = undefined;
      }
    }
  }, [
    client,
    createIdempotencyKey,
    dispatch,
    stableReference,
    state.cancelling,
    state.error,
    state.invocation,
    state.loading,
    state.operation,
    state.recoveryAttempt,
    state.referenceKey,
  ]);

  const stateMatchesReference = state.referenceKey === currentReferenceKey;
  const visibleInvocation = stateMatchesReference ? state.invocation : undefined;
  const visibleOperation = stateMatchesReference ? state.operation : undefined;
  const cancellationRecovery =
    stateMatchesReference &&
    state.errorContext === "cancellation" &&
    state.recoveryAttempt !== undefined;
  const stateConfirmed =
    stateMatchesReference &&
    visibleOperation !== undefined &&
    operationSupportsNewCancellation(visibleOperation) &&
    (state.error === undefined || cancellationRecovery);
  let effectiveDisabledReason = disabledReason;
  if (canCancel && !stateConfirmed && !cancellationRecovery) {
    if (state.errorContext === "operation") {
      effectiveDisabledReason =
        "The durable operation must be confirmed before cancellation is available.";
    } else if (visibleOperation && !operationSupportsNewCancellation(visibleOperation)) {
      effectiveDisabledReason =
        "The durable operation is not eligible for a new cancellation command.";
    } else {
      effectiveDisabledReason = "Refresh authoritative state before attempting cancellation.";
    }
  }

  return (
    <>
      <InvocationStatus
        canCancel={canCancel && (stateConfirmed || cancellationRecovery)}
        cancellationConfirmationVisible={
          stateMatchesReference && state.cancellationConfirmationVisible
        }
        cancellationRecovery={cancellationRecovery}
        disabledReason={effectiveDisabledReason}
        error={stateMatchesReference ? state.error : undefined}
        invocation={visibleInvocation}
        isCancelling={stateMatchesReference && state.cancelling}
        isLoading={!stateMatchesReference || state.loading}
        onConfirmCancellation={() => void submitCancellation()}
        onDismissCancellation={() =>
          dispatch({ referenceKey: currentReferenceKey, type: "cancellation-dismissed" })
        }
        onInspectArtifact={
          onInspectArtifact
            ? (artifactId) => onInspectArtifact({ ...stableReference, artifactId })
            : undefined
        }
        onRefresh={() => void refresh()}
        onRequestCancellation={() =>
          dispatch({ referenceKey: currentReferenceKey, type: "cancellation-requested" })
        }
        operation={visibleOperation}
        reference={reference}
      />
      {visibleInvocation?.state === "succeeded" && !state.loading && !state.error ? (
        <InvocationResultWorkflow
          client={client}
          reference={{
            ...stableReference,
            serviceId: visibleInvocation.serviceId,
            revisionId: visibleInvocation.revisionId,
          }}
        />
      ) : null}
    </>
  );
}
