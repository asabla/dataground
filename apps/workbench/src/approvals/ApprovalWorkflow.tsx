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

function sameApprovalRequest(left: InvocationApproval, right: InvocationApproval): boolean {
  return (
    left.schemaVersion === right.schemaVersion &&
    left.id === right.id &&
    left.isolationDomainId === right.isolationDomainId &&
    left.invocationId === right.invocationId &&
    left.requestedAction === right.requestedAction &&
    left.createdAt === right.createdAt &&
    left.expiresAt === right.expiresAt
  );
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
      if (
        state.approval &&
        (!sameApprovalRequest(state.approval, action.result.approval) ||
          action.result.approval.version < state.approval.version ||
          (action.result.approval.version === state.approval.version &&
            action.result.approval.state !== state.approval.state) ||
          (state.approval.decision !== undefined &&
            (state.approval.decision !== action.result.approval.decision ||
              state.approval.resolvedBy !== action.result.approval.resolvedBy ||
              state.approval.resolvedAt !== action.result.approval.resolvedAt)))
      ) {
        return {
          ...state,
          loading: false,
          errorContext: "read",
          error: {
            code: "WORKBENCH_INVALID_RESPONSE",
            message:
              "The approval changed unexpectedly. Close this inspection and reopen it from the timeline.",
            retryable: false,
          },
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
    (left.schemaVersion === "dataground.invocation-approval/v1" ||
      left.schemaVersion === "dataground.invocation-approval/v2") &&
    left.id === right.approvalId &&
    left.invocationId === right.invocationId &&
    left.isolationDomainId === right.isolationDomainId
  );
}

export function approvalReferenceKey(reference: InvocationApprovalReference): string {
  return `${reference.isolationDomainId}:${reference.invocationId}:${reference.approvalId}`;
}

export function ApprovalWorkflow(props: ApprovalWorkflowProps) {
  const key = `${approvalReferenceKey(props.reference)}:${props.canResolve}`;
  const [identity, setIdentity] = useState({ client: props.client, key, generation: 0 });
  if (identity.client !== props.client || identity.key !== key) {
    setIdentity({ client: props.client, key, generation: identity.generation + 1 });
    return null;
  }
  return (
    <ApprovalSession
      key={identity.generation}
      {...props}
      initialApproval={identity.generation === 0 ? props.initialApproval : undefined}
    />
  );
}

function approvalExpired(approval: InvocationApproval | undefined): boolean {
  return (
    approval?.schemaVersion === "dataground.invocation-approval/v2" &&
    Date.now() >= Date.parse(approval.expiresAt)
  );
}

function ApprovalSession({
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
  const [expired, setExpired] = useState(false);
  const expiryObserved = useRef(false);
  useEffect(() => {
    const check = () => {
      if (approvalExpired(state.approval)) {
        expiryObserved.current = true;
        setExpired(true);
      }
    };
    check();
    const timer = setInterval(check, 250);
    return () => clearInterval(timer);
  }, [state.approval]);
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
      if (approvalExpired(current)) {
        expiryObserved.current = true;
        setExpired(true);
        return;
      }
      if (
        !canResolve ||
        state.loading ||
        state.error !== undefined ||
        expiryObserved.current ||
        approvalExpired(current) ||
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
      canResolve,
      state.loading,
      state.error,
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
    canResolve &&
    !expired &&
    !approvalExpired(visibleApproval) &&
    supportedPendingVersion &&
    !blockedByError &&
    !state.loading;
  let disabledReason: string | undefined;
  if (expired || approvalExpired(visibleApproval)) {
    disabledReason = "The approval deadline has passed. Refresh to observe its final state.";
  } else if (state.loading) {
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
