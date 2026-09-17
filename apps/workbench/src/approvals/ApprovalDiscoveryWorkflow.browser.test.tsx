import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { DataGroundClient } from "../contracts/client";
import { ApprovalDiscoveryWorkflow } from "./ApprovalDiscoveryWorkflow";
import { ApprovalWorkflow } from "./ApprovalWorkflow";
import type { InvocationApprovalReference } from "./client";

const reference = {
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};
const firstID = "apr_00000000000000000001",
  secondID = "apr_00000000000000000002";
function approval(id = firstID) {
  const now = new Date().toISOString();
  return {
    schemaVersion: "dataground.invocation-approval/v2",
    id,
    ...reference,
    requestedAction: "process.execute",
    state: "pending",
    version: 1,
    createdAt: now,
    updatedAt: now,
    expiresAt: new Date(Date.now() + 60_000).toISOString(),
  };
}
function response(data: unknown) {
  return { data, response: new Response(null, { status: 200 }) };
}
let host: HTMLDivElement, root: Root;
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
function button(text: string) {
  return Array.from(host.querySelectorAll("button")).find((value) => value.textContent === text);
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((complete) => {
    resolve = complete;
  });
  return { promise, resolve };
}

it("discovers paged approvals and reads the selected request without submitting a decision", async () => {
  const more = deferred<ReturnType<typeof response>>();
  const reads = vi.fn(
    async (
      path: string,
      options: { params: { query?: { cursor?: string }; path: { approvalId?: string } } },
    ) => {
      if (path.endsWith("/{approvalId}")) return response(approval(options.params.path.approvalId));
      if (options.params.query?.cursor) return more.promise;
      return response({ items: [approval()], nextCursor: "cursor-1" });
    },
  );
  const post = vi.fn();
  const client = { GET: reads, POST: post } as unknown as DataGroundClient;
  function Journey() {
    const [selected, setSelected] = useState<InvocationApprovalReference>();
    return (
      <>
        <ApprovalDiscoveryWorkflow
          client={client}
          reference={reference}
          onInspectApproval={setSelected}
        />
        {selected && <ApprovalWorkflow client={client} reference={selected} canResolve={false} />}
      </>
    );
  }
  await act(async () => root.render(<Journey />));
  const loadMore = button("Load more approvals");
  await act(async () => {
    loadMore?.click();
    loadMore?.click();
  });
  expect(reads).toHaveBeenCalledTimes(2);
  expect(button(`Open approval ${firstID}`)?.disabled).toBe(true);
  await act(async () => more.resolve(response({ items: [approval(secondID)] })));
  const open = button(`Open approval ${secondID}`);
  await act(async () => open?.focus());
  expect(document.activeElement).toBe(open);
  await act(async () => userEvent.keyboard("{Enter}"));
  expect(reads).toHaveBeenCalledTimes(3);
  expect(host.textContent).toContain("Approval");
  expect(post).not.toHaveBeenCalled();
  expect(button("Approve request")).toBeUndefined();
  await act(async () => button("Refresh approvals")?.click());
  expect(button(`Open approval ${secondID}`)).toBeUndefined();
});

it("removes prior requests on denial and blocks stale controls", async () => {
  let denied = false;
  const inspected = vi.fn();
  const client = {
    GET: async () =>
      denied
        ? {
            error: {
              error: {
                code: "FORBIDDEN",
                message: "Access denied.",
                retryable: false,
                correlationId: "cor_00000000000000000001",
              },
            },
            response: new Response(null, { status: 403 }),
          }
        : response({ items: [approval()], nextCursor: "cursor-1" }),
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <ApprovalDiscoveryWorkflow
        client={client}
        reference={reference}
        onInspectApproval={inspected}
      />,
    ),
  );
  const old = button(`Open approval ${firstID}`);
  denied = true;
  await act(async () => button("Load more approvals")?.click());
  expect(host.textContent).toContain("Access denied.");
  expect(button(`Open approval ${firstID}`)).toBeUndefined();
  await act(async () => old?.click());
  expect(inspected).not.toHaveBeenCalled();
  denied = false;
  await act(async () => button("Refresh approvals")?.click());
  await act(async () => button("Load more approvals")?.click());
  expect(host.textContent).toContain("could not interpret");
  expect(button(`Open approval ${firstID}`)).toBeUndefined();
});

it("ignores delayed responses after connection or invocation changes", async () => {
  const pending = deferred<ReturnType<typeof response>>();
  const oldClient = { GET: () => pending.promise } as unknown as DataGroundClient;
  const inspected = vi.fn();
  await act(async () =>
    root.render(
      <ApprovalDiscoveryWorkflow
        client={oldClient}
        reference={reference}
        onInspectApproval={inspected}
      />,
    ),
  );
  const client = { GET: async () => response({ items: [] }) } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <ApprovalDiscoveryWorkflow
        client={client}
        reference={{ ...reference, invocationId: "inv_00000000000000000002" }}
        onInspectApproval={inspected}
      />,
    ),
  );
  await act(async () => pending.resolve(response({ items: [approval()] })));
  expect(host.textContent).toContain("No retained approval requests");
  expect(button(`Open approval ${firstID}`)).toBeUndefined();
  expect(inspected).not.toHaveBeenCalled();
});
