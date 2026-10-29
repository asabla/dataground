import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it } from "vitest";
import { userEvent } from "vitest/browser";
import type { DataGroundClient } from "../contracts/client";
import { DevelopmentWorkbench } from "../workbench/DevelopmentWorkbench";

const scope = "iso_00000000000000000001",
  workspaceId = "wsp_00000000000000000001";
const metadata = {
  id: "svc_00000000000000000001",
  isolationDomainId: scope,
  createdAt: "2026-10-29T08:00:00Z",
  updatedAt: "2026-10-29T08:00:00Z",
  createdBy: "fixture",
  generation: 1,
  version: 1,
};
const old = { metadata, name: "Previously loaded" };
const found = {
  metadata: { ...metadata, id: "svc_00000000000000000002" },
  name: "Beyond the first page",
};
const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) });
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
  window.history.replaceState(null, "", "#");
});
const visible = (el: Element) => !el.closest("[hidden],[inert]");
async function click(name: string) {
  const el = [...host.querySelectorAll("button")].find(
    (el) => visible(el) && (el.getAttribute("aria-label") ?? el.textContent?.trim()) === name,
  );
  expect(el, name).toBeDefined();
  await act(async () => userEvent.click(el as HTMLButtonElement));
}
async function fill(value: string) {
  const el = [...host.querySelectorAll<HTMLInputElement>('input[type="search"]')].find(visible);
  expect(el).toBeDefined();
  await act(async () => userEvent.fill(el as HTMLInputElement, value));
}

it.each([false, true])(
  "searches before paging and rejects an obsolete search response (workspace=%s)",
  async (workspace) => {
    window.history.replaceState(
      null,
      "",
      `#/scopes/${scope}/${workspace ? `workspaces/${workspaceId}` : "agents"}`,
    );
    const requests: Array<{ q?: string; cursor?: string; path: string }> = [];
    let finishOld: ((value: ReturnType<typeof ok>) => void) | undefined;
    const resource = (s: typeof old) => (workspace ? { ...s, workspaceId } : s);
    const client = {
      GET: async (
        path: string,
        options: { params: { query?: { q?: string; cursor?: string } } },
      ) => {
        if (path.endsWith("/{workspaceId}"))
          return ok({ metadata: { ...metadata, id: workspaceId }, name: "Research" });
        if (!path.endsWith("/agent-services")) return ok({ items: [] });
        if (workspace && !path.includes("/workspaces/")) return ok({ items: [] });
        const query = options.params.query ?? {};
        requests.push({ ...query, path });
        if (query.q === "slow")
          return new Promise<ReturnType<typeof ok>>((resolve) => {
            finishOld = resolve;
          });
        if (query.q === "beyond") return ok({ items: [resource(found)] });
        return ok({ items: [resource(old)], nextCursor: "initial_cursor" });
      },
    } as unknown as DataGroundClient;
    await act(async () =>
      root.render(
        <DevelopmentWorkbench client={client} isolationDomainId={scope} onDisconnect={() => {}} />,
      ),
    );
    expect(host.textContent).toContain("Previously loaded");
    const initialCount = requests.length;
    await fill("slow");
    expect(requests).toHaveLength(initialCount);
    await click("Search");
    expect(host.textContent).not.toContain("Previously loaded");
    await fill("beyond");
    await click("Search");
    expect(requests.at(-1)?.q).toBe("beyond");
    expect(requests.at(-1)?.cursor).toBeUndefined();
    expect(host.textContent).toContain("Beyond the first page");
    await act(async () => finishOld?.(ok({ items: [resource(old)] })));
    expect(host.textContent).not.toContain("Previously loaded");
    expect(host.textContent).toContain("Beyond the first page");
    expect(window.location.hash).not.toContain("beyond");
    await click("Home");
    await click(workspace ? "Workspace" : "Agent services");
    expect(host.textContent).toContain("Beyond the first page");
    expect(
      [...host.querySelectorAll<HTMLInputElement>('input[type="search"]')].find(visible)?.value,
    ).toBe("beyond");
  },
);
