import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import type { ServiceAliasResource } from "./aliasClient";
import { ServiceRouteDiscovery } from "./ServiceRouteDiscovery";

const scope = {
  isolationDomainId: "iso_00000000000000000001",
  serviceId: "svc_00000000000000000001",
};
const alias: ServiceAliasResource = {
  name: "canary",
  serviceId: scope.serviceId,
  revisionId: "rev_00000000000000000001",
  metadata: {
    id: "als_00000000000000000001",
    isolationDomainId: scope.isolationDomainId,
    createdBy: "operator",
    createdAt: "2026-09-05T12:00:00Z",
    updatedAt: "2026-09-05T12:00:00Z",
    version: 1,
    generation: 1,
  },
};
const stable = {
  ...alias,
  name: "stable",
  metadata: { ...alias.metadata, id: "als_00000000000000000002" },
};
function page(items: ServiceAliasResource[], nextCursor?: string) {
  return { data: { items, nextCursor }, response: new Response(null, { status: 200 }) };
}
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
});
function button(text: string) {
  const element = [...host.querySelectorAll("button")].find((value) => value.textContent === text);
  expect(element).toBeDefined();
  return element as HTMLButtonElement;
}
async function click(text: string) {
  await act(async () => button(text).click());
}

it("pages in name order, locks duplicate reads and withdrawals, then refreshes current routes", async () => {
  const calls: unknown[] = [];
  let resolve: ((value: ReturnType<typeof page>) => void) | undefined;
  const client = {
    GET: async (_path: string, options: unknown) => {
      calls.push(options);
      if (calls.length === 1) return page([alias], "next");
      if (calls.length === 2)
        return new Promise((done) => {
          resolve = done;
        });
      return page([stable]);
    },
  } as unknown as DataGroundClient;
  const withdraw = vi.fn();
  await act(async () =>
    root.render(
      <ServiceRouteDiscovery client={client} scope={scope} canWithdraw onWithdraw={withdraw} />,
    ),
  );
  await act(async () => button("Load more routes").focus());
  await act(async () => {
    button("Load more routes").click();
    button("Load more routes").click();
  });
  expect(calls).toHaveLength(2);
  expect(button("Withdraw canary alias").disabled).toBe(true);
  await click("Withdraw canary alias");
  expect(withdraw).not.toHaveBeenCalled();
  await act(async () => resolve?.(page([stable])));
  expect(document.activeElement).toBe(host.querySelector("h2"));
  expect([...host.querySelectorAll("strong")].map((value) => value.textContent)).toEqual([
    "canary",
    "stable",
  ]);
  await click("Withdraw canary alias");
  expect(withdraw).toHaveBeenCalledWith(alias);
  expect(withdraw.mock.calls[0]?.[0]).not.toBe(alias);
  const refresh = button("Refresh routes");
  await act(async () => refresh.focus());
  await click("Refresh routes");
  expect(document.activeElement).toBe(refresh);
  expect(host.textContent).not.toContain("canary");
  expect(host.textContent).toContain("stable");
});

it("retains an exact retry boundary and clears routes when a continuation is denied", async () => {
  const cursors: unknown[] = [];
  const client = {
    GET: async (_path: string, options: { params: { query: { cursor?: string } } }) => {
      cursors.push(options.params.query.cursor);
      if (cursors.length === 1) return page([alias], "next");
      if (cursors.length === 2) throw new Error("offline");
      return {
        response: new Response(null, { status: 403 }),
        error: {
          error: {
            code: "ACCESS_DENIED",
            message: "Access denied.",
            retryable: false,
            correlationId: "cor_denied",
          },
        },
      };
    },
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <ServiceRouteDiscovery client={client} scope={scope} canWithdraw onWithdraw={() => {}} />,
    ),
  );
  await click("Load more routes");
  expect(button("Withdraw canary alias").disabled).toBe(true);
  await click("Retry route listing");
  expect(cursors).toEqual([undefined, "next", "next"]);
  expect(host.textContent).not.toContain("canary");
  expect(host.querySelector('[role="alert"]')?.textContent).toContain("Access denied.");
});

it("hides prior connection or scope rows and ignores delayed reads", async () => {
  let finishOld: ((value: ReturnType<typeof page>) => void) | undefined;
  let finishNew: ((value: ReturnType<typeof page>) => void) | undefined;
  let calls = 0;
  const oldClient = {
    GET: async () =>
      ++calls === 1
        ? page([alias])
        : new Promise((done) => {
            finishOld = done;
          }),
  } as unknown as DataGroundClient;
  const newClient = {
    GET: async () =>
      new Promise((done) => {
        finishNew = done;
      }),
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <ServiceRouteDiscovery client={oldClient} scope={scope} canWithdraw onWithdraw={() => {}} />,
    ),
  );
  await click("Refresh routes");
  const newScope = { ...scope, isolationDomainId: "iso_00000000000000000002" };
  await act(async () =>
    root.render(
      <ServiceRouteDiscovery
        client={newClient}
        scope={newScope}
        canWithdraw
        onWithdraw={() => {}}
      />,
    ),
  );
  await act(async () => finishOld?.(page([alias])));
  expect(host.textContent).not.toContain("canary");
  expect(host.textContent).toContain("Loading service routes");
  await act(async () => finishNew?.(page([])));
  expect(host.textContent).toContain("No active routes");
});

it("offers observation without withdrawal controls and stops stalled pagination", async () => {
  let calls = 0;
  const client = {
    GET: async () => (++calls === 1 ? page([alias], "first") : page([alias], "second")),
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <ServiceRouteDiscovery
        client={client}
        scope={scope}
        onWithdraw={() => {
          throw new Error("observer withdrawal");
        }}
      />,
    ),
  );
  expect(host.textContent).not.toContain("Withdraw");
  await click("Load more routes");
  expect(host.textContent).toContain("did not advance");
  expect(host.textContent).not.toContain("Load more routes");
});
