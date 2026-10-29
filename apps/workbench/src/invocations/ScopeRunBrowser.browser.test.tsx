import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { DataGroundClient } from "../contracts/client";
import { ScopeRunBrowser } from "./ScopeRunBrowser";

const scope = "iso_00000000000000000001";
function run(
  id = "inv_00000000000000000001",
  state = "failed",
  serviceName = "Analysis assistant",
) {
  return {
    metadata: {
      id,
      isolationDomainId: scope,
      createdBy: "operator",
      createdAt: "2026-10-29T08:00:00Z",
      updatedAt: "2026-10-29T08:01:00Z",
      generation: 1,
      version: 1,
    },
    serviceName,
    serviceId: "svc_00000000000000000001",
    revisionId: "rev_00000000000000000001",
    alias: "stable",
    state,
    operationId: "op_00000000000000000001",
    correlationId: "cor_00000000000000000001",
  };
}
const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) });
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((complete) => {
    resolve = complete;
  });
  return { promise, resolve };
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
const button = (name: string) =>
  Array.from(host.querySelectorAll("button")).find(
    (item) => (item.getAttribute("aria-label") ?? item.textContent) === name,
  );
async function select(value: string) {
  await act(async () => {
    const field = host.querySelector("select") as HTMLSelectElement;
    field.value = value;
    field.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

it("pages summaries and opens the exact run without per-service reads", async () => {
  const reads = vi.fn(
    async (_path: string, options: { params: { query?: { cursor?: string } } }) =>
      options.params.query?.cursor
        ? ok({ items: [run("inv_00000000000000000002", "waiting", "Quality assistant")] })
        : ok({ items: [run()], nextCursor: "page-2" }),
  );
  const onOpen = vi.fn();
  await act(async () =>
    root.render(
      <ScopeRunBrowser
        client={{ GET: reads } as unknown as DataGroundClient}
        isolationDomainId={scope}
        onOpen={onOpen}
      />,
    ),
  );
  await act(async () => button("Load more runs")?.click());
  expect(reads).toHaveBeenCalledTimes(2);
  const inspect = button("Inspect Quality assistant, stable, waiting");
  await act(async () =>
    userEvent.click(
      inspect?.closest('[role="row"]')?.querySelector('[role="rowheader"]') as HTMLElement,
    ),
  );
  await act(async () => userEvent.keyboard("{End}{Enter}"));
  expect(onOpen).toHaveBeenCalledWith(
    expect.objectContaining({
      metadata: expect.objectContaining({ id: "inv_00000000000000000002" }),
    }),
  );
  expect(
    reads.mock.calls.every(
      ([path]) => path === "/v1/isolation-domains/{isolationDomainId}/invocations",
    ),
  ).toBe(true);
  await act(async () =>
    userEvent.fill(host.querySelector('input[type="search"]') as HTMLInputElement, "Quality"),
  );
  expect(host.textContent).toContain("1 of 2 loaded runs");
});
it("replaces an in-flight page when the server state filter changes", async () => {
  const old = deferred<ReturnType<typeof ok>>();
  const reads = vi.fn(async (_path: string, options: { params: { query: { state?: string } } }) =>
    options.params.query.state
      ? ok({ items: [run("inv_00000000000000000002", "waiting", "Pending assistant")] })
      : old.promise,
  );
  const client = { GET: reads } as unknown as DataGroundClient;
  await act(async () =>
    root.render(<ScopeRunBrowser client={client} isolationDomainId={scope} onOpen={() => {}} />),
  );
  await select("waiting");
  expect(host.textContent).toContain("Pending assistant");
  await act(async () => old.resolve(ok({ items: [run()] })));
  expect(host.textContent).not.toContain("Analysis assistant");
  expect(reads.mock.calls[1]?.[1].params.query.state).toBe("waiting");
});
it("clears denied rows and rejects detached actions and repeated pages", async () => {
  let deny = false;
  const onOpen = vi.fn();
  const client = {
    GET: async (_path: string, options: { params: { query: { cursor?: string } } }) =>
      deny
        ? {
            error: {
              error: {
                code: "FORBIDDEN",
                message: "Run access denied.",
                retryable: false,
                correlationId: "cor_00000000000000000001",
              },
            },
            response: new Response(null, { status: 403 }),
          }
        : ok({ items: [run()], nextCursor: options.params.query.cursor ? "page-3" : "page-2" }),
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(<ScopeRunBrowser client={client} isolationDomainId={scope} onOpen={onOpen} />),
  );
  const old = button("Inspect Analysis assistant, stable, failed");
  deny = true;
  await act(async () => button("Load more runs")?.click());
  expect(host.textContent).toContain("Run access denied.");
  expect(host.textContent).not.toContain("Analysis assistant");
  await act(async () => old?.click());
  expect(onOpen).not.toHaveBeenCalled();
  deny = false;
  await act(async () => button("Refresh runs")?.click());
  await act(async () => button("Load more runs")?.click());
  expect(host.textContent).toContain("Run discovery did not advance");
  expect(host.textContent).not.toContain("Analysis assistant");
});
it("discards an old identity response", async () => {
  const old = deferred<ReturnType<typeof ok>>();
  await act(async () =>
    root.render(
      <ScopeRunBrowser
        client={{ GET: () => old.promise } as unknown as DataGroundClient}
        isolationDomainId={scope}
        onOpen={() => {}}
      />,
    ),
  );
  await act(async () =>
    root.render(
      <ScopeRunBrowser
        client={{ GET: async () => ok({ items: [] }) } as unknown as DataGroundClient}
        isolationDomainId={scope}
        onOpen={() => {}}
      />,
    ),
  );
  await act(async () => old.resolve(ok({ items: [run()] })));
  expect(host.textContent).not.toContain("Analysis assistant");
  expect(host.textContent).toContain("No runs match");
});
