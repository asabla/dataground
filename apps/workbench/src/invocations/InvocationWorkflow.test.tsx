import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import {
  createCancellationIdempotencyKey,
  InvocationWorkflow,
  invocationReferenceKey,
  invocationWorkflowReducer,
} from "./InvocationWorkflow";
import type { InvocationOperationResource, InvocationStatusResource } from "./client";

const reference = {
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};
const referenceKey = invocationReferenceKey(reference);

const invocation: InvocationStatusResource = {
  alias: "stable",
  artifactIds: [],
  correlationId: "cor_00000000000000000001",
  metadata: {
    createdAt: "2026-08-14T12:00:00Z",
    createdBy: "reference-runtime",
    generation: 1,
    id: reference.invocationId,
    isolationDomainId: reference.isolationDomainId,
    updatedAt: "2026-08-14T12:00:01Z",
    version: 1,
  },
  operationId: "op_00000000000000000001",
  revisionId: "rev_00000000000000000001",
  serviceId: "svc_00000000000000000001",
  state: "running",
};

const operation: InvocationOperationResource = {
  attempt: 1,
  command: "invoke",
  correlationId: invocation.correlationId,
  desiredState: "succeeded",
  kind: "invocation-execution",
  metadata: { ...invocation.metadata, id: invocation.operationId },
  observedState: "running",
  stateMachineVersion: 2,
};

function state() {
  return {
    cancellationConfirmationVisible: false,
    cancelling: false,
    invocation,
    loading: false,
    operation,
    referenceKey,
  };
}

describe("InvocationWorkflow", () => {
  it("renders scoped loading without implying cancellation authority", () => {
    const markup = renderToStaticMarkup(
      <InvocationWorkflow canCancel client={{} as DataGroundClient} reference={reference} />,
    );

    assert.match(markup, /Loading invocation/u);
    assert.match(markup, new RegExp(reference.invocationId, "u"));
    assert.doesNotMatch(markup, /Request cancellation/u);
  });

  it("opens and dismisses confirmation only from confirmed cancellable state", () => {
    const requested = invocationWorkflowReducer(state(), {
      referenceKey,
      type: "cancellation-requested",
    });
    const dismissed = invocationWorkflowReducer(requested, {
      referenceKey,
      type: "cancellation-dismissed",
    });
    const missingOperation = invocationWorkflowReducer(
      { ...state(), operation: undefined },
      { referenceKey, type: "cancellation-requested" },
    );
    const unrelatedOperation = invocationWorkflowReducer(
      { ...state(), operation: { ...operation, command: "publish" } },
      { referenceKey, type: "cancellation-requested" },
    );

    assert.equal(requested.cancellationConfirmationVisible, true);
    assert.equal(dismissed.cancellationConfirmationVisible, false);
    assert.equal(missingOperation.cancellationConfirmationVisible, false);
    assert.equal(unrelatedOperation.cancellationConfirmationVisible, false);
  });

  it("retains the exact idempotency attempt after uncertain cancellation", () => {
    const attempt = { idempotencyKey: "cancel:stable0001" };
    const submitting = invocationWorkflowReducer(state(), {
      attempt,
      referenceKey,
      type: "cancellation-started",
    });
    const failed = invocationWorkflowReducer(submitting, {
      attempt,
      referenceKey,
      result: {
        error: {
          code: "WORKBENCH_CANCELLATION_UNCONFIRMED",
          message: "Cancellation could not be confirmed.",
          outcomeUnknown: true,
          retryable: true,
        },
        ok: false,
      },
      type: "cancellation-finished",
    });

    assert.equal(failed.cancelling, false);
    assert.equal(failed.errorContext, "cancellation");
    assert.equal(failed.recoveryAttempt, attempt);
  });

  it("replaces stale state with cancellation and operation observations", () => {
    const cancelledInvocation = { ...invocation, state: "cancelling" };
    const cancellingOperation = {
      ...operation,
      command: "cancel",
      desiredState: "cancelled",
      observedState: "cancelling",
    };
    const result = invocationWorkflowReducer(state(), {
      attempt: { idempotencyKey: "cancel:stable0001" },
      referenceKey,
      result: {
        invocation: cancelledInvocation,
        ok: true,
        operation: cancellingOperation,
      },
      type: "cancellation-finished",
    });

    assert.equal(result.invocation, cancelledInvocation);
    assert.equal(result.operation, cancellingOperation);
    assert.equal(result.recoveryAttempt, undefined);
    assert.equal(result.error, undefined);
  });

  it("retains confirmed invocation data when a refresh fails", () => {
    const loading = invocationWorkflowReducer(state(), {
      referenceKey,
      type: "load-started",
    });
    const failed = invocationWorkflowReducer(loading, {
      referenceKey,
      result: {
        error: {
          code: "WORKBENCH_NETWORK_UNAVAILABLE",
          message: "DataGround could not be reached.",
          retryable: true,
        },
        ok: false,
      },
      type: "load-finished",
    });

    assert.equal(failed.invocation, invocation);
    assert.equal(failed.operation, operation);
    assert.equal(failed.errorContext, "read");
    assert.equal(failed.loading, false);
  });

  it("clears prior scope immediately and ignores late completion", () => {
    const nextReferenceKey = `${reference.isolationDomainId}:inv_00000000000000000002`;
    const switched = invocationWorkflowReducer(state(), {
      referenceKey: nextReferenceKey,
      type: "load-started",
    });
    const late = invocationWorkflowReducer(switched, {
      referenceKey,
      result: { invocation, ok: true, operation },
      type: "load-finished",
    });

    assert.equal(switched.invocation, undefined);
    assert.equal(switched.operation, undefined);
    assert.equal(late, switched);
  });

  it("uses contract-safe deterministic idempotency key formatting", () => {
    const key = createCancellationIdempotencyKey(() => "11111111-2222-3333-4444-555555555555");

    assert.equal(key, "cancel:11111111222233334444555555555555");
  });
});
