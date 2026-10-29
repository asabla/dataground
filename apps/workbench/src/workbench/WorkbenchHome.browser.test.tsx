import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { DataGroundClient } from "../contracts/client";
import { WorkbenchHome } from "./WorkbenchHome";

const scope = "iso_00000000000000000001";
function run(
  id = "inv_00000000000000000001",
  state = "waiting",
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

const baseProps = {
  savedResources: null,
  isolationDomainId: scope,
  recentWork: [{ key: "draft", name: "New service draft", kind: "Unsaved service draft" as const }],
  onOpenWork: vi.fn(),
  onCreate: vi.fn(),
  onBrowseRuns: vi.fn(),
  onOpenRun: vi.fn(),
};

it("shows a bounded attention preview using one scoped query and opens the exact run", async () => {
  const reads = vi.fn(async () =>
    ok({
      items: Array.from({ length: 10 }, (_, index) =>
        run(`inv_${String(index + 1).padStart(20, "0")}`, "waiting", `Analysis ${index + 1}`),
      ),
      nextCursor: "next-page",
    }),
  );
  await act(async () =>
    root.render(
      <WorkbenchHome {...baseProps} client={{ GET: reads } as unknown as DataGroundClient} />,
    ),
  );
  expect(reads).toHaveBeenCalledExactlyOnceWith(
    "/v1/isolation-domains/{isolationDomainId}/invocations",
    { params: { path: { isolationDomainId: scope }, query: { limit: 50, state: "waiting" } } },
  );
  expect(host.textContent).toContain("Showing the first 8 loaded runs");
  expect(host.textContent).not.toContain("Analysis 9");
  await act(async () =>
    userEvent.click(button("Inspect Analysis 2, stable, waiting") as HTMLButtonElement),
  );
  expect(baseProps.onOpenRun).toHaveBeenCalledWith(
    expect.objectContaining({
      metadata: expect.objectContaining({ id: "inv_00000000000000000002" }),
    }),
  );
  await act(async () => button("Resume New service draft")?.click());
  expect(baseProps.onOpenWork).toHaveBeenCalledWith("draft");
  await act(async () => button("View all runs")?.click());
  expect(baseProps.onBrowseRuns).toHaveBeenCalledOnce();
});

it("rejects old waiting results after changing the server attention filter", async () => {
  const pending = deferred<ReturnType<typeof ok>>();
  const reads = vi.fn(async (_path: string, options: { params: { query: { state: string } } }) =>
    options.params.query.state === "waiting"
      ? pending.promise
      : ok({ items: [run("inv_00000000000000000002", "failed", "Failed analysis")] }),
  );
  await act(async () =>
    root.render(
      <WorkbenchHome {...baseProps} client={{ GET: reads } as unknown as DataGroundClient} />,
    ),
  );
  await select("failed");
  expect(host.textContent).toContain("Failed analysis");
  await act(async () => pending.resolve(ok({ items: [run()] })));
  expect(host.textContent).not.toContain("Analysis assistant");
  expect(host.textContent).toContain("Failed analysis");
});

it("clears attention rows after a denied refresh and keeps recent drafts separate", async () => {
  let denied = false;
  const reads = vi.fn(async () =>
    denied
      ? {
          error: {
            error: { code: "FORBIDDEN", message: "Attention access denied.", retryable: false },
          },
          response: new Response(null, { status: 403 }),
        }
      : ok({ items: [run()] }),
  );
  await act(async () =>
    root.render(
      <WorkbenchHome {...baseProps} client={{ GET: reads } as unknown as DataGroundClient} />,
    ),
  );
  const old = button("Inspect Analysis assistant, stable, waiting");
  denied = true;
  await act(async () => button("Refresh attention")?.click());
  expect(host.querySelector('[role="alert"]')).not.toBeNull();
  expect(button("Inspect Analysis assistant, stable, waiting")).toBeUndefined();
  baseProps.onOpenRun.mockClear();
  await act(async () => old?.click());
  expect(baseProps.onOpenRun).not.toHaveBeenCalled();
  expect(button("Resume New service draft")).toBeDefined();
});
