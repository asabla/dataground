import {
  type ApprovalDecision,
  ApprovalRequest,
  type ApprovalResource,
} from "@dataground/patterns";
import "@dataground/patterns/styles.css";
import { Button, StatusBadge } from "@dataground/ui";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import {
  type ApprovalFailure,
  type InvocationApproval,
  type InvocationApprovalReference,
  readInvocationApproval,
  resolveInvocationApproval,
} from "./client";

interface ApprovalAttempt {
  decision: ApprovalDecision;
  idempotencyKey: string;
}

interface ApprovalWorkflowState {
  approval?: InvocationApproval;
  error?: ApprovalFailure;
  errorContext?: "read" | "submission";
  loading: boolean;
  referenceKey: string;
  recoveryAttempt?: ApprovalAttempt;
  submitting?: ApprovalDecision;
}

type ApprovalWorkflowAction =
  | { referenceKey: string; type: "load-started" }
  | {
      referenceKey: string;
      result: Awaited<ReturnType<typeof readInvocationApproval>>;
      type: "load-finished";
    }
  | { attempt: ApprovalAttempt; referenceKey: string; type: "submission-started" }
  | { error: ApprovalFailure; referenceKey: string; type: "submission-blocked" }
  | {
      attempt: ApprovalAttempt;
      referenceKey: string;
      result: Awaited<ReturnType<typeof resolveInvocationApproval>>;
      type: "submission-finished";
    };

export interface ApprovalWorkflowProps {
  canResolve: boolean;
  client: DataGroundClient;
  initialApproval?: InvocationApproval;
  reference: InvocationApprovalReference;
  createIdempotencyKey?: () => string;
}

export function approvalWorkflowReducer(
  state: ApprovalWorkflowState,
  action: ApprovalWorkflowAction,
): ApprovalWorkflowState {
  switch (action.type) {
    case "load-started":
      if (state.referenceKey !== action.referenceKey) {
        return { loading: true, referenceKey: action.referenceKey };
      }
      return { ...state, error: undefined, errorContext: undefined, loading: true };
    case "load-finished":
      if (state.referenceKey !== action.referenceKey) {
        return state;
      }
      if (!action.result.ok) {
        return {
          ...state,
          error: action.result.error,
          errorContext: "read",
          loading: false,
        };
      }
      return {
        approval: action.result.approval,
        errorContext: undefined,
        loading: false,
        referenceKey: action.referenceKey,
        recoveryAttempt:
          action.result.approval.state === "pending" ? state.recoveryAttempt : undefined,
      };
    case "submission-started":
      if (state.referenceKey !== action.referenceKey) {
        return state;
      }
      return {
        ...state,
        error: undefined,
        errorContext: undefined,
        recoveryAttempt: action.attempt,
        submitting: action.attempt.decision,
      };
    case "submission-blocked":
      if (state.referenceKey !== action.referenceKey) {
        return state;
      }
      return {
        ...state,
        error: action.error,
        errorContext: "submission",
        recoveryAttempt: undefined,
        submitting: undefined,
      };
    case "submission-finished":
      if (state.referenceKey !== action.referenceKey) {
        return state;
      }
      if (!action.result.ok) {
        return {
          ...state,
          error: action.result.error,
          errorContext: "submission",
          recoveryAttempt: action.result.error.retryable ? action.attempt : undefined,
          submitting: undefined,
        };
      }
      return {
        approval: action.result.approval,
        errorContext: undefined,
        loading: false,
        referenceKey: action.referenceKey,
      };
  }
}

export function createApprovalIdempotencyKey(randomUUID: () => string): string {
  return `approval:${randomUUID().replaceAll("-", "")}`;
}

function defaultIdempotencyKey(): string {
  if (!globalThis.crypto?.randomUUID) {
    throw new Error("secure random identifier generation is unavailable");
  }
  return createApprovalIdempotencyKey(() => globalThis.crypto.randomUUID());
}

function sameReference(left: InvocationApproval, right: InvocationApprovalReference): boolean {
  return (
    left.schemaVersion === "dataground.invocation-approval/v1" &&
    left.id === right.approvalId &&
    left.invocationId === right.invocationId &&
    left.isolationDomainId === right.isolationDomainId
  );
}

export function approvalReferenceKey(reference: InvocationApprovalReference): string {
  return `${reference.isolationDomainId}:${reference.invocationId}:${reference.approvalId}`;
}

export function ApprovalWorkflow({
  canResolve,
  client,
  createIdempotencyKey = defaultIdempotencyKey,
  initialApproval,
  reference,
}: ApprovalWorkflowProps) {
  const currentReferenceKey = approvalReferenceKey(reference);
  const validInitialApproval =
    initialApproval && sameReference(initialApproval, reference) ? initialApproval : undefined;
  const [state, setState] = useState<ApprovalWorkflowState>({
    approval: validInitialApproval,
    loading: validInitialApproval === undefined,
    referenceKey: currentReferenceKey,
  });
  const requestGeneration = useRef(0);
  const submissionLock = useRef<object | undefined>(undefined);
  const stableReference = useMemo(
    () => ({
      approvalId: reference.approvalId,
      invocationId: reference.invocationId,
      isolationDomainId: reference.isolationDomainId,
    }),
    [reference.approvalId, reference.invocationId, reference.isolationDomainId],
  );

  const dispatch = useCallback((action: ApprovalWorkflowAction) => {
    setState((current) => approvalWorkflowReducer(current, action));
  }, []);

  const refresh = useCallback(async () => {
    const generation = ++requestGeneration.current;
    const referenceKey = approvalReferenceKey(stableReference);
    dispatch({ referenceKey, type: "load-started" });
    const result = await readInvocationApproval(client, stableReference);
    if (requestGeneration.current === generation) {
      dispatch({ referenceKey, result, type: "load-finished" });
    }
  }, [client, dispatch, stableReference]);

  useEffect(() => {
    void refresh();
    return () => {
      requestGeneration.current++;
      submissionLock.current = undefined;
    };
  }, [refresh]);

  const submitDecision = useCallback(
    async (decision: ApprovalDecision) => {
      const current = state.approval;
      const referenceKey = approvalReferenceKey(stableReference);
      if (
        state.referenceKey !== referenceKey ||
        !current ||
        current.state !== "pending" ||
        current.version !== 1 ||
        state.submitting ||
        submissionLock.current !== undefined
      ) {
        return;
      }
      const lock = {};
      submissionLock.current = lock;
      let attempt: ApprovalAttempt;
      try {
        attempt =
          state.recoveryAttempt?.decision === decision
            ? state.recoveryAttempt
            : { decision, idempotencyKey: createIdempotencyKey() };
      } catch {
        dispatch({
          error: {
            code: "WORKBENCH_SECURE_RANDOM_UNAVAILABLE",
            message: "A secure request identifier could not be created. Refresh before retrying.",
            retryable: false,
          },
          referenceKey,
          type: "submission-blocked",
        });
        if (submissionLock.current === lock) {
          submissionLock.current = undefined;
        }
        return;
      }
      const generation = requestGeneration.current;
      dispatch({ attempt, referenceKey, type: "submission-started" });
      try {
        const result = await resolveInvocationApproval(
          client,
          stableReference,
          decision,
          attempt.idempotencyKey,
        );
        if (requestGeneration.current === generation) {
          dispatch({ attempt, referenceKey, result, type: "submission-finished" });
        }
      } finally {
        if (submissionLock.current === lock) {
          submissionLock.current = undefined;
        }
      }
    },
    [
      client,
      createIdempotencyKey,
      dispatch,
      stableReference,
      state.approval,
      state.recoveryAttempt,
      state.referenceKey,
      state.submitting,
    ],
  );

  const stateMatchesReference = state.referenceKey === currentReferenceKey;
  const visibleApproval = stateMatchesReference ? state.approval : undefined;

  if (!stateMatchesReference || (state.loading && !visibleApproval)) {
    return (
      <section aria-live="polite" className="approval-workflow-state">
        <StatusBadge tone="active">Loading approval</StatusBadge>
        <p>Retrieving the authoritative approval state.</p>
      </section>
    );
  }

  if (!visibleApproval) {
    return (
      <section
        aria-live="polite"
        className="approval-workflow-state approval-workflow-state--error"
      >
        <StatusBadge tone="critical">Approval unavailable</StatusBadge>
        <p>{state.error?.message ?? "The approval could not be loaded."}</p>
        {state.error?.correlationId && <code>{state.error.correlationId}</code>}
        <Button onPress={() => void refresh()} variant="secondary">
          Retry read
        </Button>
      </section>
    );
  }

  const supportedPendingVersion =
    visibleApproval.state !== "pending" || visibleApproval.version === 1;
  const blockedByError = state.error !== undefined;
  const resolutionAvailable =
    canResolve && supportedPendingVersion && !blockedByError && !state.loading;
  let disabledReason: string | undefined;
  if (state.loading) {
    disabledReason = "The authoritative approval state is being refreshed.";
  } else if (!supportedPendingVersion) {
    disabledReason = "This approval version requires a newer Workbench before it can be resolved.";
  } else if (blockedByError) {
    disabledReason = "Refresh the authoritative state before attempting another decision.";
  }

  return (
    <ApprovalRequest
      approval={visibleApproval as ApprovalResource}
      canResolve={resolutionAvailable}
      disabledReason={disabledReason}
      error={state.error}
      errorHeading={state.errorContext === "read" ? "State not refreshed." : undefined}
      isSubmitting={state.submitting !== undefined || state.loading}
      onDecision={(decision) => void submitDecision(decision)}
      onRefresh={() => void refresh()}
      recoveryDecision={state.recoveryAttempt?.decision}
    />
  );
}
