import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { DataGroundClient } from "../contracts/client";
import { DevelopmentWorkbench } from "../workbench/DevelopmentWorkbench";
import { ResourceSearch } from "./ResourceSearch";

const scope = "iso_00000000000000000001",
  workspaceId = "wsp_00000000000000000001",
  otherWorkspace = "wsp_00000000000000000002";
const metadata = { id: workspaceId, isolationDomainId: scope, createdAt: "2026-10-29T08:00:00Z" };
const found = { metadata, kind: "workspace", name: "Research workspace" };
const artifact = {
  metadata: { ...metadata, id: "art_00000000000000000001" },
  kind: "artifact",
  name: "Research.txt",
  serviceId: "svc_00000000000000000001",
  invocationId: "inv_00000000000000000001",
  state: "available",
};
const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) });
const denied = () => ({
  error: {
    error: {
      code: "ACCESS_DENIED",
      message: "Search access is denied.",
      correlationId: "cor-review",
      retryable: false,
    },
  },
  response: new Response(null, { status: 403 }),
});
let host: HTMLDivElement, root: Root;
beforeEach(() => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  window.history.replaceState(null, "", `#/scopes/${scope}/search`);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  window.history.replaceState(null, "", "#");
});
const visible = (el: Element) => !el.closest("[hidden],[inert]");
const button = (name: string) =>
  [...host.querySelectorAll("button")].find(
    (el) => visible(el) && (el.getAttribute("aria-label") ?? el.textContent?.trim()) === name,
  );
async function click(name: string) {
  const el = button(name);
  expect(el, name).toBeDefined();
  await act(async () => userEvent.click(el as HTMLButtonElement));
}
const input = () =>
  host.querySelector<HTMLInputElement>('input[name="resource-search"]') as HTMLInputElement;
async function search(value: string) {
  await act(async () => userEvent.fill(input(), value));
  await click("Search");
}
async function kind(value: string) {
  await act(async () => {
    const select = [...host.querySelectorAll("select")].find(
      (el) => visible(el) && [...el.options].some((o) => o.value === "artifact"),
    ) as HTMLSelectElement;
    select.value = value;
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

it("pages through one search endpoint and restores focus when the last continuation disappears", async () => {
  const read = vi.fn(
    async (_path: string, options: { params: { query: { q: string; cursor?: string } } }) =>
      options.params.query.cursor
        ? ok({ items: [artifact] })
        : ok({ items: [found], nextCursor: "next-page" }),
  );
  const onOpen = vi.fn();
  await act(async () =>
    root.render(
      <ResourceSearch
        client={{ GET: read } as unknown as DataGroundClient}
        isolationDomainId={scope}
        onOpen={onOpen}
      />,
    ),
  );
  expect(read).not.toHaveBeenCalled();
  await search("Research");
  expect(read).toHaveBeenCalledTimes(1);
  await click("Load more results");
  await expect.poll(() => document.activeElement?.textContent).toContain("2 results loaded");
  expect(button("Load more results")).toBeUndefined();
  await click("Open artifact Research.txt");
  expect(onOpen).toHaveBeenCalledWith(artifact);
  expect(
    read.mock.calls.every(([path]) => path === "/v1/isolation-domains/{isolationDomainId}/search"),
  ).toBe(true);
  expect(read.mock.calls[1]?.[1].params.query).toEqual({
    q: "Research",
    cursor: "next-page",
    limit: 50,
  });
});
it("rejects delayed query and kind responses, then removes stale actions after denial", async () => {
  let finishOld!: (value: ReturnType<typeof ok>) => void,
    finishKind!: (value: ReturnType<typeof ok>) => void;
  let forbidden = false;
  const read = vi.fn(
    async (_path: string, options: { params: { query: { q: string; kind?: string } } }) => {
      const q = options.params.query;
      if (q.q === "slow")
        return new Promise<ReturnType<typeof ok>>((resolve) => {
          finishOld = resolve;
        });
      if (q.kind === "workspace")
        return new Promise<ReturnType<typeof ok>>((resolve) => {
          finishKind = resolve;
        });
      if (forbidden) return denied();
      return ok({ items: q.kind === "artifact" ? [artifact] : [found] });
    },
  );
  const onOpen = vi.fn();
  await act(async () =>
    root.render(
      <ResourceSearch
        client={{ GET: read } as unknown as DataGroundClient}
        isolationDomainId={scope}
        onOpen={onOpen}
      />,
    ),
  );
  await search("slow");
  await search("Research");
  await act(async () => finishOld(ok({ items: [{ ...found, name: "slow old result" }] })));
  expect(host.textContent).not.toContain("slow old result");
  await kind("workspace");
  await kind("artifact");
  await act(async () => finishKind(ok({ items: [found] })));
  expect(button("Open workspace Research workspace")).toBeUndefined();
  const oldAction = button("Open artifact Research.txt");
  expect(oldAction).toBeDefined();
  forbidden = true;
  await click("Refresh search");
  expect(host.querySelector('[role="alert"]')?.textContent).toBe("Search access is denied.");
  expect(button("Open artifact Research.txt")).toBeUndefined();
  await act(async () => oldAction?.click());
  expect(onOpen).not.toHaveBeenCalled();
});
it("retains search through resource navigation and browser history, then clears it on identity replacement", async () => {
  const full = (id: string, name: string) => ({
    metadata: {
      ...metadata,
      id,
      updatedAt: metadata.createdAt,
      createdBy: "fixture",
      version: 1,
      generation: 1,
    },
    name,
  });
  const reads: string[] = [];
  const api = {
    GET: async (
      path: string,
      options: { params: { path: { workspaceId?: string }; query?: { kind?: string } } },
    ) => {
      reads.push(path);
      if (path.endsWith("/search"))
        return ok({ items: options.params.query?.kind === "artifact" ? [artifact] : [found] });
      if (path.endsWith("/{workspaceId}"))
        return ok(
          full(
            options.params.path.workspaceId ?? workspaceId,
            options.params.path.workspaceId === otherWorkspace
              ? "Second workspace"
              : "Research workspace",
          ),
        );
      if (path.endsWith("/workspaces"))
        return ok({
          items: [
            full(otherWorkspace, "Second workspace"),
            full(workspaceId, "Research workspace"),
          ],
        });
      if (path.endsWith("/{serviceId}")) return denied();
      return ok({ items: [] });
    },
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <DevelopmentWorkbench client={api} isolationDomainId={scope} onDisconnect={() => {}} />,
    ),
  );
  await expect.poll(() => document.activeElement).toBe(input());
  await search("Research");
  await click("Open workspace Research workspace");
  expect(window.location.hash).toBe(`#/scopes/${scope}/workspaces/${workspaceId}`);
  await click("All workspaces");
  await click("Open workspace Second workspace");
  await act(async () => window.history.back());
  await expect.poll(() => window.location.hash).toBe(`#/scopes/${scope}/workspaces`);
  await expect
    .poll(() => [...host.querySelectorAll("h1")].find(visible)?.textContent)
    .toBe("Workspace");
  await act(async () => window.history.back());
  await expect
    .poll(() => [...host.querySelectorAll("h1")].find(visible)?.textContent)
    .toBe("Research workspace");
  await click("Search resources");
  expect(input().value).toBe("Research");
  expect(window.location.hash).toBe(`#/scopes/${scope}/search`);
  await kind("artifact");
  await click("Open artifact Research.txt");
  expect(window.location.hash).toBe(
    `#/scopes/${scope}/agents/${artifact.serviceId}/runs/${artifact.invocationId}/files/artifacts/${artifact.metadata.id}`,
  );
  await expect
    .poll(() => [...host.querySelectorAll("h1")].find(visible)?.textContent)
    .toBe("Service unavailable");
  expect(reads.some((path) => path.endsWith("/content"))).toBe(false);
  await click("Search resources");
  expect(input().value).toBe("Research");
  expect(
    [...host.querySelectorAll("select")].find((el) => visible(el) && el.value === "artifact"),
  ).toBeDefined();
  await act(async () =>
    root.render(
      <DevelopmentWorkbench
        client={{ ...api }}
        isolationDomainId={scope}
        onDisconnect={() => {}}
      />,
    ),
  );
  expect(input().value).toBe("");
  expect(button("Open artifact Research.txt")).toBeUndefined();
  expect(window.location.hash).not.toContain("Research");
});
