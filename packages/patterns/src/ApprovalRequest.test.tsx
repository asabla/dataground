import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { ApprovalRequest, type ApprovalResource } from "./ApprovalRequest";

const pendingApproval: ApprovalResource = {
  id: "apr_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
  invocationId: "inv_00000000000000000001",
  requestedAction: "workspace.change",
  state: "pending",
  version: 1,
  createdAt: "2026-08-14T12:00:00Z",
  updatedAt: "2026-08-14T12:00:00Z",
};

describe("ApprovalRequest", () => {
  it("shows scope, authority limits, and both decisions to an authorized controller", () => {
    const markup = renderToStaticMarkup(
      <ApprovalRequest approval={pendingApproval} canResolve onDecision={() => undefined} />,
    );

    assert.match(markup, /Waiting for decision/u);
    assert.match(markup, /Change the workspace/u);
    assert.match(markup, /iso_00000000000000000001/u);
    assert.match(markup, /does not bypass policy/u);
    assert.match(markup, /Approve request/u);
    assert.match(markup, /Deny request/u);
  });

  it("keeps observers and unsupported versions from implying decision authority", () => {
    const markup = renderToStaticMarkup(
      <ApprovalRequest
        approval={{ ...pendingApproval, version: 2 }}
        canResolve={false}
        disabledReason="This approval version requires a newer Workbench."
      />,
    );

    assert.match(markup, /requires a newer Workbench/u);
    assert.doesNotMatch(markup, /Approve request/u);
    assert.doesNotMatch(markup, /Deny request/u);
  });

  it("renders resolved authorship without decision controls", () => {
    const markup = renderToStaticMarkup(
      <ApprovalRequest
        approval={{
          ...pendingApproval,
          decision: "approve",
          resolvedAt: "2026-08-14T12:01:00Z",
          resolvedBy: "usr_00000000000000000001",
          state: "resolved",
          version: 2,
        }}
        canResolve
        onDecision={() => undefined}
      />,
    );

    assert.match(markup, /Decision recorded/u);
    assert.match(markup, /Approved/u);
    assert.match(markup, /usr_00000000000000000001/u);
    assert.match(markup, /Decided at/u);
    assert.match(markup, /2026-08-14T12:01:00Z/u);
    assert.doesNotMatch(markup, /Approve request/u);
  });

  it("fails visibly for unknown states and actions", () => {
    const markup = renderToStaticMarkup(
      <ApprovalRequest
        approval={{ ...pendingApproval, requestedAction: "runtime.future", state: "future" }}
        canResolve
        onDecision={() => undefined}
      />,
    );

    assert.match(markup, /Unknown state: future/u);
    assert.match(markup, /runtime.future/u);
    assert.match(markup, /Update the Workbench before deciding/u);
    assert.doesNotMatch(markup, /Approve request/u);
    assert.doesNotMatch(markup, /Deny request/u);
  });

  it("offers only same-key recovery after a retryable ambiguous submission", () => {
    const markup = renderToStaticMarkup(
      <ApprovalRequest
        approval={pendingApproval}
        canResolve
        error={{ message: "The command outcome is not yet known.", retryable: true }}
        onDecision={() => undefined}
        recoveryDecision="approve"
      />,
    );

    assert.match(markup, /Retry approval/u);
    assert.match(markup, /reuse its original idempotency key/u);
    assert.doesNotMatch(markup, /Deny request/u);
  });

  it("lets the consumer distinguish a failed refresh from an unconfirmed decision", () => {
    const markup = renderToStaticMarkup(
      <ApprovalRequest
        approval={pendingApproval}
        canResolve={false}
        error={{ message: "The authoritative state is unavailable.", retryable: true }}
        errorHeading="State not refreshed."
      />,
    );

    assert.match(markup, /State not refreshed/u);
    assert.doesNotMatch(markup, /Decision not confirmed/u);
  });
});
