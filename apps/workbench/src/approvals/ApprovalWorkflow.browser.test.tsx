import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { ApprovalWorkflow } from "./ApprovalWorkflow";
import type { InvocationApproval } from "./client";

const reference = {
  approvalId: "apr_00000000000000000001",
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};
let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});
function pending(): InvocationApproval {
  const now = Date.now();
  return {
    schemaVersion: "dataground.invocation-approval/v2",
    id: reference.approvalId,
    invocationId: reference.invocationId,
    isolationDomainId: reference.isolationDomainId,
    requestedAction: "process.execute",
    state: "pending",
    version: 1,
    createdAt: new Date(now).toISOString(),
    updatedAt: new Date(now).toISOString(),
    expiresAt: new Date(now + 60_000).toISOString(),
  };
}
function button(label: string) {
  return Array.from(host.querySelectorAll("button")).find((button) => button.textContent === label);
}
function source(value: InvocationApproval) {
  const post = vi.fn(async () => ({ data: value, response: new Response(null, { status: 200 }) }));
  return {
    post,
    client: {
      GET: async () => ({ data: value, response: new Response(null, { status: 200 }) }),
      POST: post,
    } as unknown as DataGroundClient,
  };
}
it("checks expiry at submission and latches it across a backwards clock and refresh", async () => {
  const value = pending(),
    transport = source(value);
  await act(async () =>
    root.render(<ApprovalWorkflow client={transport.client} reference={reference} canResolve />),
  );
  const approve = button("Approve request");
  expect(approve).toBeDefined();
  const clock = vi.spyOn(Date, "now").mockReturnValue(Date.parse(String(value.expiresAt)));
  await act(async () => approve?.click());
  expect(transport.post).not.toHaveBeenCalled();
  clock.mockReturnValue(Date.parse(value.createdAt));
  await act(async () => button("Refresh state")?.click());
  expect(button("Approve request")).toBeUndefined();
  expect(host.textContent).toContain("deadline has passed");
});
it("updates an idle inspection when its deadline passes", async () => {
  const value = pending(),
    transport = source(value);
  await act(async () =>
    root.render(<ApprovalWorkflow client={transport.client} reference={reference} canResolve />),
  );
  vi.spyOn(Date, "now").mockReturnValue(Date.parse(String(value.expiresAt)));
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 300));
  });
  expect(button("Approve request")).toBeUndefined();
  expect(transport.post).not.toHaveBeenCalled();
});
it("removes old decisions on authority change and presents uncertain delivery as terminal", async () => {
  const value = pending(),
    transport = source(value);
  await act(async () =>
    root.render(<ApprovalWorkflow client={transport.client} reference={reference} canResolve />),
  );
  const old = button("Approve request");
  await act(async () =>
    root.render(
      <ApprovalWorkflow client={transport.client} reference={reference} canResolve={false} />,
    ),
  );
  await act(async () => old?.click());
  expect(button("Approve request")).toBeUndefined();
  expect(transport.post).not.toHaveBeenCalled();
  const expiry = String(value.expiresAt);
  const terminal = {
    ...value,
    state: "delivery_unknown",
    version: 4,
    decision: "deny",
    resolvedBy: "controller",
    resolvedAt: new Date(Date.parse(value.createdAt) + 1_000).toISOString(),
    closedAt: expiry,
    closeReason: "expired",
    updatedAt: expiry,
  } as InvocationApproval;
  const next = source(terminal);
  await act(async () =>
    root.render(<ApprovalWorkflow client={next.client} reference={reference} canResolve />),
  );
  expect(host.textContent).toContain("Delivery unknown");
  expect(host.textContent).toContain("It cannot be sent again");
  expect(button("Approve request")).toBeUndefined();
  expect(button("Deny request")).toBeUndefined();
});
