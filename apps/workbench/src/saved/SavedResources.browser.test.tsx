import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { DataGroundClient } from "../contracts/client";
import type { SearchResult } from "../search/client";
import { DevelopmentWorkbench } from "../workbench/DevelopmentWorkbench";
import { SavedResourceNotice, SavedResources, useSavedResources } from "./SavedResources";

const scope = "iso_00000000000000000001",
  workspaceId = "wsp_00000000000000000001";
const resource: SearchResult = {
  kind: "workspace",
  name: "Research workspace",
  metadata: { id: workspaceId, isolationDomainId: scope, createdAt: "2026-10-29T08:00:00Z" },
};
const saved = {
  kind: "workspace",
  resourceId: workspaceId,
  isolationDomainId: scope,
  savedAt: "2026-10-29T10:00:00Z",
  resource,
};
const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) });
const receipt = (saved = true) =>
  ok({
    kind: "workspace",
    resourceId: workspaceId,
    isolationDomainId: scope,
    saved,
    updatedAt: "2026-10-29T10:00:00Z",
  });
const denied = () => ({
  error: {
    error: {
      code: "ACCESS_DENIED",
      message: "Saved resource access denied.",
      correlationId: "cor_test",
      retryable: false,
    },
  },
  response: new Response(null, { status: 403 }),
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
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
  history.replaceState(null, "", `#/scopes/${scope}/home`);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  history.replaceState(null, "", location.pathname);
});
const button = (label: string) =>
  Array.from(host.querySelectorAll("button")).find(
    (b) => (b.getAttribute("aria-label") ?? b.textContent) === label,
  );
async function click(label: string) {
  const target = button(label);
  expect(target).toBeDefined();
  await act(async () => userEvent.click(target as HTMLButtonElement));
}
function Harness({
  client,
  onOpen = () => undefined,
}: {
  client: DataGroundClient;
  onOpen?: (item: SearchResult) => void;
}) {
  const controller = useSavedResources(client, scope, true);
  const [visible, setVisible] = useState(true);
  return (
    <>
      <button type="button" onClick={() => setVisible((v) => !v)}>
        Switch work
      </button>
      <button
        type="button"
        disabled={controller.blocked}
        onClick={() => controller.change("workspace", workspaceId, "Research workspace", true)}
      >
        Save research
      </button>
      <SavedResourceNotice controller={controller} />
      <div hidden={!visible} inert={!visible}>
        <SavedResources controller={controller} onOpen={onOpen} onSearch={() => undefined} />
      </div>
      {!visible && <h1>Other work</h1>}
    </>
  );
}

it("keeps an uncertain request across navigation and denial, then retries the exact command", async () => {
  let attempt = 0,
    stored = false;
  const writes = vi.fn(async (_path: unknown, options: { body: { saved: boolean } }) => {
    attempt++;
    if (attempt === 1) throw new Error("lost response");
    if (attempt === 2) return denied();
    stored = options.body.saved;
    return receipt(stored);
  });
  const client = {
    GET: async () => ok({ items: stored ? [saved] : [] }),
    POST: writes,
  } as unknown as DataGroundClient;
  await act(async () => root.render(<Harness client={client} />));
  await click("Save research");
  expect(host.textContent).toContain("outcome is unknown");
  expect(button("Save research")?.disabled).toBe(true);
  await click("Switch work");
  expect(
    host.querySelector('[aria-label="Saved resource request"]')?.closest("[hidden]"),
  ).toBeNull();
  await click("Retry saved-resource request");
  expect(host.textContent).toContain("outcome remains unconfirmed");
  expect(button("Save research")?.disabled).toBe(true);
  await click("Retry saved-resource request");
  expect(host.textContent).toContain("Save request confirmed for workspace Research workspace.");
  expect(button("Save research")?.disabled).toBe(false);
  expect(writes).toHaveBeenCalledTimes(3);
  expect(writes.mock.calls[0]).toEqual(writes.mock.calls[1]);
  expect(writes.mock.calls[1]).toEqual(writes.mock.calls[2]);
  expect(document.activeElement).toBe(host.querySelector('[aria-label="Saved resource request"]'));
  await click("Switch work");
  expect(host.querySelector('table[aria-label="Saved resources"]')?.textContent).toContain(
    "Research workspace",
  );
});

it("invalidates an older list response when a save commits", async () => {
  const old = deferred<ReturnType<typeof ok>>();
  let reads = 0;
  const client = {
    GET: async () => (++reads === 1 ? old.promise : ok({ items: [saved] })),
    POST: async () => receipt(),
  } as unknown as DataGroundClient;
  await act(async () => root.render(<Harness client={client} />));
  await click("Save research");
  expect(host.textContent).toContain("1 saved resource loaded.");
  await act(async () => old.resolve(ok({ items: [] })));
  expect(host.textContent).toContain("1 saved resource loaded.");
  expect(button("Open saved workspace Research workspace")).toBeDefined();
});

it("opens exact metadata, removes unavailable references, and clears actions on denied refresh", async () => {
  const unavailable = {
    ...saved,
    resource: undefined,
    resourceId: "wsp_00000000000000000002",
    savedAt: "2026-10-29T09:00:00Z",
  };
  let mode = "first";
  const onOpen = vi.fn();
  const client = {
    GET: async (_path: unknown, options: { params: { query: { cursor?: string } } }) =>
      mode === "denied"
        ? denied()
        : ok(
            options.params.query.cursor
              ? { items: [unavailable] }
              : { items: [saved], nextCursor: "next" },
          ),
    POST: vi.fn(
      async (
        _path: unknown,
        options: { body: { kind: string; resourceId: string; saved: boolean } },
      ) => {
        mode = "denied";
        return ok({ ...options.body, isolationDomainId: scope, updatedAt: "2026-10-29T11:00:00Z" });
      },
    ),
  } as unknown as DataGroundClient;
  await act(async () => root.render(<Harness client={client} onOpen={onOpen} />));
  await click("Open saved workspace Research workspace");
  expect(onOpen).toHaveBeenCalledWith(resource);
  await click("Load more saved resources");
  expect(button("Load more saved resources")).toBeUndefined();
  expect(document.activeElement).toBe(host.querySelector("#home-saved-title"));
  expect(button("Open saved workspace unavailable resource")?.disabled).toBe(true);
  await click("Remove saved workspace wsp_00000000000000000002");
  expect(client.POST).toHaveBeenCalledWith(
    expect.any(String),
    expect.objectContaining({
      body: { kind: "workspace", resourceId: "wsp_00000000000000000002", saved: false },
    }),
  );
  expect(host.textContent).toContain("Saved resource access denied.");
  expect(button("Open saved workspace Research workspace")).toBeUndefined();
});

it("connects search, save, Home and workspace navigation without browser persistence", async () => {
  history.replaceState(null, "", `#/scopes/${scope}/search`);
  let stored = false;
  const reads = vi.fn(async (path: string) => {
    if (path.endsWith("/search")) return ok({ items: [resource] });
    if (path.endsWith("/saved-resources")) return ok({ items: stored ? [saved] : [] });
    if (path.endsWith("/workspaces/{workspaceId}"))
      return ok({
        ...resource,
        kind: undefined,
        metadata: {
          ...resource.metadata,
          updatedAt: resource.metadata.createdAt,
          createdBy: "fixture",
          generation: 1,
          version: 1,
        },
      });
    return ok({ items: [] });
  });
  const client = {
    GET: reads,
    POST: async () => {
      stored = true;
      return receipt();
    },
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <DevelopmentWorkbench
        client={client}
        isolationDomainId={scope}
        onDisconnect={() => undefined}
      />,
    ),
  );
  await act(async () =>
    userEvent.fill(host.querySelector('input[type="search"]') as HTMLInputElement, "Research"),
  );
  await click("Search");
  await click("Save workspace Research workspace");
  expect(location.hash).not.toContain("Research");
  await click("Home");
  await click("Open saved workspace Research workspace");
  expect(location.hash).toBe(`#/scopes/${scope}/workspaces/${workspaceId}`);
  expect(
    Array.from(host.querySelectorAll("h1")).find((heading) => !heading.closest("[hidden],[inert]"))
      ?.textContent,
  ).toBe("Research workspace");
  await click("Home");
  // Reconnect using a new client identity. Only the API can restore saved work.
  await act(async () =>
    root.render(
      <DevelopmentWorkbench
        client={{ ...client }}
        isolationDomainId={scope}
        onDisconnect={() => undefined}
      />,
    ),
  );
  expect(button("Open saved workspace Research workspace")).toBeDefined();
  expect(Object.values(localStorage).join(" ")).not.toContain("Research workspace");
  expect(Object.values(sessionStorage).join(" ")).not.toContain("Research workspace");
});

it("clears an old identity's pending command and ignores its late receipt", async () => {
  history.replaceState(null, "", `#/scopes/${scope}/search`);
  const delayed = deferred<ReturnType<typeof receipt>>();
  const old = {
    GET: async (path: string) => ok({ items: path.endsWith("/search") ? [resource] : [] }),
    POST: async () => delayed.promise,
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <DevelopmentWorkbench
        client={old}
        isolationDomainId={scope}
        onDisconnect={() => undefined}
      />,
    ),
  );
  await act(async () =>
    userEvent.fill(host.querySelector('input[type="search"]') as HTMLInputElement, "Research"),
  );
  await click("Search");
  await click("Save workspace Research workspace");
  expect(host.textContent).toContain("Saving Research workspace");
  const next = { GET: async () => ok({ items: [] }) } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <DevelopmentWorkbench
        client={next}
        isolationDomainId={scope}
        onDisconnect={() => undefined}
      />,
    ),
  );
  await act(async () => delayed.resolve(receipt()));
  expect(host.textContent).not.toContain("Research workspace");
  expect(host.querySelector('[aria-label="Saved resource request"]')).toBeNull();
  expect((host.querySelector('input[type="search"]') as HTMLInputElement).value).toBe("");
});
