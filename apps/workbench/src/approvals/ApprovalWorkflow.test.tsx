import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import {
  ApprovalWorkflow,
  approvalWorkflowReducer,
  createApprovalIdempotencyKey,
} from "./ApprovalWorkflow";
import type { InvocationApproval } from "./client";

const approval: InvocationApproval = {
  schemaVersion: "dataground.invocation-approval/v1",
  id: "apr_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
  invocationId: "inv_00000000000000000001",
  requestedAction: "workspace.change",
  state: "pending",
  version: 1,
  createdAt: "2026-08-14T12:00:00Z",
  updatedAt: "2026-08-14T12:00:00Z",
};
const referenceKey = `${approval.isolationDomainId}:${approval.invocationId}:${approval.id}`;

describe("ApprovalWorkflow", () => {
  it("renders a contract-matched initial approval without implying unavailable authority", () => {
    const markup = renderToStaticMarkup(
      <ApprovalWorkflow
        canResolve={false}
        client={{} as DataGroundClient}
        initialApproval={approval}
        reference={{
          approvalId: approval.id,
          invocationId: approval.invocationId,
          isolationDomainId: approval.isolationDomainId,
        }}
      />,
    );

    assert.match(markup, /Waiting for decision/u);
    assert.match(markup, /observe the request but cannot submit/u);
    assert.doesNotMatch(markup, /Approve request/u);
  });

  it("retains the exact attempt only for retryable ambiguous failures", () => {
    const attempt = { decision: "approve" as const, idempotencyKey: "approval:stable" };
    const submitting = approvalWorkflowReducer(
      { approval, loading: false, referenceKey },
      { attempt, referenceKey, type: "submission-started" },
    );
    const failed = approvalWorkflowReducer(submitting, {
      attempt,
      referenceKey,
      result: {
        error: {
          code: "COMMAND_IN_PROGRESS",
          message: "The command is still being reconciled.",
          retryable: true,
        },
        ok: false,
      },
      type: "submission-finished",
    });

    assert.deepEqual(failed.recoveryAttempt, attempt);
    assert.equal(failed.submitting, undefined);
    const refreshed = approvalWorkflowReducer(failed, {
      referenceKey,
      result: { approval, ok: true },
      type: "load-finished",
    });
    assert.deepEqual(refreshed.recoveryAttempt, attempt);
  });

  it("clears ambiguous recovery after authoritative completion", () => {
    const state = approvalWorkflowReducer(
      {
        approval,
        loading: false,
        referenceKey,
        recoveryAttempt: { decision: "deny", idempotencyKey: "approval:stable" },
      },
      {
        referenceKey,
        result: {
          approval: { ...approval, decision: "deny", state: "resolved", version: 2 },
          ok: true,
        },
        type: "load-finished",
      },
    );

    assert.equal(state.recoveryAttempt, undefined);
    assert.equal(state.approval?.state, "resolved");
  });

  it("clears approval data as soon as a different reference starts loading", () => {
    const nextReferenceKey = `${approval.isolationDomainId}:inv_00000000000000000002:apr_00000000000000000002`;
    const state = approvalWorkflowReducer(
      { approval, loading: false, referenceKey },
      { referenceKey: nextReferenceKey, type: "load-started" },
    );

    assert.equal(state.referenceKey, nextReferenceKey);
    assert.equal(state.approval, undefined);
    assert.equal(state.loading, true);
  });

  it("marks an existing approval busy during refresh and classifies read failures", () => {
    const refreshing = approvalWorkflowReducer(
      { approval, loading: false, referenceKey },
      { referenceKey, type: "load-started" },
    );
    const failed = approvalWorkflowReducer(refreshing, {
      referenceKey,
      result: {
        error: {
          code: "WORKBENCH_NETWORK_UNAVAILABLE",
          message: "The Workbench could not reach DataGround.",
          retryable: true,
        },
        ok: false,
      },
      type: "load-finished",
    });

    assert.equal(refreshing.loading, true);
    assert.equal(failed.errorContext, "read");
    assert.equal(failed.approval, approval);
  });

  it("generates API-compatible idempotency keys", () => {
    const key = createApprovalIdempotencyKey(() => "11111111-2222-3333-4444-555555555555");

    assert.equal(key, "approval:11111111222233334444555555555555");
    assert.match(key, /^[A-Za-z0-9._:]{8,128}$/u);
  });
});

it("rejects a refreshed approval that extends expiry or regresses terminal state", () => {
  const expiring = {
    ...approval,
    schemaVersion: "dataground.invocation-approval/v2",
    expiresAt: "2026-08-14T12:10:00Z",
  } as InvocationApproval;
  const expired = {
    ...expiring,
    state: "expired",
    version: 2,
    closedAt: "2026-08-14T12:10:00Z",
    closeReason: "expired",
    updatedAt: "2026-08-14T12:10:00Z",
  } as InvocationApproval;
  for (const next of [
    expiring,
    { ...expired, expiresAt: "2026-08-14T12:12:00Z" } as InvocationApproval,
  ]) {
    const state = approvalWorkflowReducer(
      { approval: expired, loading: true, referenceKey },
      { type: "load-finished", referenceKey, result: { ok: true, approval: next } },
    );
    assert.equal(state.approval, expired);
    assert.equal(state.error?.code, "WORKBENCH_INVALID_RESPONSE");
  }
});
