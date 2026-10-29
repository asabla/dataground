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
  return Array.from(host.querySelectorAll("button")).find(
    (value) => (value.getAttribute("aria-label") ?? value.textContent) === text,
  );
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((complete) => {
    resolve = complete;
  });
  return { promise, resolve };
}

it.each([false, true])(
  "discovers paged approvals and reads the selected request without submitting a decision (compact=%s)",
  async (compact) => {
    const more = deferred<ReturnType<typeof response>>();
    const reads = vi.fn(
      async (
        path: string,
        options: { params: { query?: { cursor?: string }; path: { approvalId?: string } } },
      ) => {
        if (path.endsWith("/{approvalId}"))
          return response(approval(options.params.path.approvalId));
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
            compact={compact}
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
    if (compact) {
      await act(async () =>
        userEvent.click(
          open?.closest('[role="row"]')?.querySelector('[role="rowheader"]') as HTMLElement,
        ),
      );
      await act(async () => userEvent.keyboard("{End}"));
    } else {
      await act(async () => open?.focus());
    }
    expect(document.activeElement).toBe(open);
    await act(async () => userEvent.keyboard("{Enter}"));
    expect(reads).toHaveBeenCalledTimes(3);
    expect(host.textContent).toContain("Approval");
    expect(post).not.toHaveBeenCalled();
    expect(button("Approve request")).toBeUndefined();
    await act(async () => button("Refresh approvals")?.click());
    expect(button(`Open approval ${secondID}`)).toBeUndefined();
  },
);

it.each([false, true])(
  "removes prior requests on denial and blocks stale controls (compact=%s)",
  async (compact) => {
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
          compact={compact}
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
  },
);

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

it("keeps filters and rejects an earlier page after a completed request refresh", async () => {
  const earlier = deferred<ReturnType<typeof response>>();
  const request = approval();
  const get = vi.fn(async () => {
    if (get.mock.calls.length === 2) return earlier.promise;
    return response({
      items: [
        {
          ...request,
          state: get.mock.calls.length > 2 ? "resolved" : "pending",
          version: get.mock.calls.length > 2 ? 2 : 1,
          ...(get.mock.calls.length > 2
            ? { decision: "deny", resolvedBy: "reviewer", resolvedAt: request.updatedAt }
            : {}),
        },
      ],
    });
  });
  const client = { GET: get } as unknown as DataGroundClient;
  const show = async (refreshGeneration: number) =>
    act(async () =>
      root.render(
        <ApprovalDiscoveryWorkflow
          compact
          client={client}
          reference={reference}
          refreshGeneration={refreshGeneration}
          onInspectApproval={vi.fn()}
        />,
      ),
    );
  await show(0);
  await act(async () =>
    userEvent.selectOptions(host.querySelector("select") as HTMLSelectElement, "pending"),
  );
  await act(async () => userEvent.click(button("Refresh approvals") as HTMLButtonElement));
  await show(1);
  expect(get).toHaveBeenCalledTimes(3);
  expect(host.querySelector("select")?.value).toBe("pending");
  expect(host.textContent).toContain("No loaded requests match this state.");
  await act(async () => earlier.resolve(response({ items: [approval()] })));
  expect(host.textContent).toContain("No loaded requests match this state.");
});
