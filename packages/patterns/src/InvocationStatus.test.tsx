import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import {
  InvocationStatus,
  type InvocationStatusResource,
  isInvocationCancellable,
} from "./InvocationStatus";

const invocation: InvocationStatusResource = {
  alias: "stable",
  artifactIds: ["art_00000000000000000001"],
  correlationId: "cor_00000000000000000001",
  metadata: {
    createdAt: "2026-08-14T12:00:00Z",
    createdBy: "reference-runtime",
    generation: 2,
    id: "inv_00000000000000000001",
    isolationDomainId: "iso_00000000000000000001",
    updatedAt: "2026-08-14T12:00:01Z",
    version: 2,
  },
  operationId: "op_00000000000000000001",
  revisionId: "rev_00000000000000000001",
  serviceId: "svc_00000000000000000001",
  state: "running",
  usage: { inputTokens: 12, outputTokens: 8, totalTokens: 20 },
};

const operation = {
  attempt: 1,
  command: "invoke",
  correlationId: invocation.correlationId,
  desiredState: "succeeded",
  kind: "invocation-execution",
  metadata: { ...invocation.metadata, id: invocation.operationId },
  observedState: "running",
  stateMachineVersion: 2,
};

const reference = {
  invocationId: invocation.metadata.id,
  isolationDomainId: invocation.metadata.isolationDomainId,
};

describe("InvocationStatus", () => {
  it("keeps scope, desired state, observed state, and bounded usage explicit", () => {
    const markup = renderToStaticMarkup(
      <InvocationStatus
        canCancel={false}
        invocation={invocation}
        operation={operation}
        reference={reference}
      />,
    );

    assert.match(markup, /Invocation running/u);
    assert.match(markup, /Desired state/u);
    assert.match(markup, /succeeded/u);
    assert.match(markup, /Observed state/u);
    assert.match(markup, /running/u);
    assert.match(markup, /Total tokens/u);
    assert.match(markup, />20</u);
    assert.match(markup, /has not been granted cancellation authority/u);
  });

  it("requires explicit confirmation before presenting the consequential command", () => {
    const initial = renderToStaticMarkup(
      <InvocationStatus
        canCancel
        invocation={invocation}
        onConfirmCancellation={() => undefined}
        onDismissCancellation={() => undefined}
        onRequestCancellation={() => undefined}
        reference={reference}
      />,
    );
    const confirming = renderToStaticMarkup(
      <InvocationStatus
        canCancel
        cancellationConfirmationVisible
        invocation={invocation}
        onConfirmCancellation={() => undefined}
        onDismissCancellation={() => undefined}
        onRequestCancellation={() => undefined}
        reference={reference}
      />,
    );

    assert.match(initial, /Request cancellation/u);
    assert.doesNotMatch(initial, /Confirm cancellation/u);
    assert.match(confirming, /Confirm cancellation/u);
    assert.match(confirming, /cannot be withdrawn/u);
    assert.match(confirming, /Keep running/u);
  });

  it("offers an idempotent recovery action when command outcome is uncertain", () => {
    const markup = renderToStaticMarkup(
      <InvocationStatus
        canCancel
        cancellationRecovery
        error={{ message: "DataGround could not confirm the cancellation.", retryable: true }}
        invocation={invocation}
        onConfirmCancellation={() => undefined}
        onDismissCancellation={() => undefined}
        onRequestCancellation={() => undefined}
        reference={reference}
      />,
    );

    assert.match(markup, /outcome is uncertain/u);
    assert.match(markup, /same request identifier/u);
    assert.match(markup, /Retry cancellation/u);
  });

  it("does not expose cancellation for terminal or unknown states", () => {
    for (const state of ["succeeded", "failed", "cancelled", "quarantined"]) {
      const markup = renderToStaticMarkup(
        <InvocationStatus
          canCancel
          invocation={{ ...invocation, state }}
          onConfirmCancellation={() => undefined}
          onDismissCancellation={() => undefined}
          onRequestCancellation={() => undefined}
          reference={reference}
        />,
      );

      assert.doesNotMatch(markup, /Request cancellation/u);
      assert.doesNotMatch(markup, /Confirm cancellation/u);
    }
  });

  it("never presents aligned unknown operation state as success", () => {
    const markup = renderToStaticMarkup(
      <InvocationStatus
        canCancel={false}
        invocation={{ ...invocation, state: "unknown" }}
        operation={{ ...operation, desiredState: "unknown", observedState: "unknown" }}
        reference={reference}
      />,
    );

    assert.match(markup, /Operation state unknown/u);
    assert.doesNotMatch(markup, /Desired state observed/u);
  });

  it("blocks lifecycle actions while state is degraded unless recovering one exact command", () => {
    const degraded = renderToStaticMarkup(
      <InvocationStatus
        canCancel
        error={{ message: "State could not be refreshed.", retryable: true }}
        invocation={invocation}
        onConfirmCancellation={() => undefined}
        onDismissCancellation={() => undefined}
        onRequestCancellation={() => undefined}
        reference={reference}
      />,
    );
    const recovery = renderToStaticMarkup(
      <InvocationStatus
        canCancel
        cancellationRecovery
        error={{ message: "Cancellation could not be confirmed.", retryable: true }}
        invocation={invocation}
        onConfirmCancellation={() => undefined}
        onDismissCancellation={() => undefined}
        onRequestCancellation={() => undefined}
        reference={reference}
      />,
    );

    assert.doesNotMatch(degraded, /Request cancellation/u);
    assert.doesNotMatch(degraded, /Retry cancellation/u);
    assert.match(recovery, /Retry cancellation/u);
  });

  it("renders partial and unavailable states without inventing operation state", () => {
    const partial = renderToStaticMarkup(
      <InvocationStatus canCancel={false} invocation={invocation} reference={reference} />,
    );
    const unavailable = renderToStaticMarkup(
      <InvocationStatus
        canCancel={false}
        error={{ message: "Invocation state could not be read.", retryable: true }}
        reference={reference}
      />,
    );

    assert.match(partial, /durable operation has not been confirmed/u);
    assert.match(unavailable, /Invocation unavailable/u);
    assert.doesNotMatch(unavailable, /Observed invocation state/u);
  });

  it("shows bounded invocation and operation failure evidence without hiding retryability", () => {
    const failure = {
      code: "RUNTIME_FAILED",
      correlationId: "cor_00000000000000000002",
      message: "The runtime reported a safe terminal failure.",
      retryable: false,
    };
    const markup = renderToStaticMarkup(
      <InvocationStatus
        canCancel={false}
        invocation={{ ...invocation, error: failure, state: "failed" }}
        operation={{
          ...operation,
          error: failure,
          errorClassification: "terminal",
          observedState: "failed",
        }}
        reference={reference}
      />,
    );

    assert.match(markup, /Invocation failure: RUNTIME_FAILED/u);
    assert.match(markup, /Operation failure: RUNTIME_FAILED/u);
    assert.match(markup, /Error classification/u);
    assert.match(markup, /not retryable/u);
    assert.match(markup, new RegExp(failure.correlationId, "u"));
  });

  it("sanitizes bounded server labels before presentation", () => {
    const markup = renderToStaticMarkup(
      <InvocationStatus
        canCancel={false}
        invocation={{ ...invocation, alias: `safe\u001b[31m-${"a".repeat(100)}` }}
        reference={{ ...reference, invocationId: `inv_${"a".repeat(200)}` }}
      />,
    );

    assert.match(markup, /safe�\[31m-/u);
    assert.match(markup, /…/u);
  });

  it("classifies only the known nonterminal states as cancellable", () => {
    assert.equal(isInvocationCancellable("accepted"), true);
    assert.equal(isInvocationCancellable("running"), true);
    assert.equal(isInvocationCancellable("waiting"), true);
    assert.equal(isInvocationCancellable("cancelling"), false);
    assert.equal(isInvocationCancellable("unknown"), false);
    assert.equal(isInvocationCancellable("future-state"), false);
  });
});
