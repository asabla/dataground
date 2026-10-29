import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { DevelopmentWorkbench } from "./DevelopmentWorkbench";
import { routeHash } from "./navigation";

const scope = "iso_00000000000000000001";
const serviceId = "svc_00000000000000000001";
const service = {
  name: "Direct service",
  metadata: {
    id: serviceId,
    isolationDomainId: scope,
    version: 1,
    generation: 1,
    createdAt: "2026-10-29T08:00:00Z",
    updatedAt: "2026-10-29T08:00:00Z",
    createdBy: "operator",
  },
};
const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) });
const absent = (code = "RESOURCE_NOT_FOUND") => ({
  error: {
    error: {
      code,
      message: "Resource not found.",
      retryable: false,
      correlationId: "cor_navigation",
    },
  },
  response: new Response(null, { status: 404 }),
});
function fixture(exact: () => unknown = () => ok(service)) {
  const reads: string[] = [];
  return {
    reads,
    client: {
      GET: async (path: string) => {
        reads.push(path);
        if (path.endsWith("/{serviceId}")) return exact();
        if (path.endsWith("/{alias}")) return absent("SERVICE_ALIAS_NOT_FOUND");
        return ok({ items: [] });
      },
      POST: async () => {
        throw new Error("Lost response");
      },
    } as unknown as DataGroundClient,
  };
}
let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  window.history.replaceState(null, "", window.location.pathname);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  window.history.replaceState(null, "", window.location.pathname);
});
async function render(client: DataGroundClient) {
  await act(async () =>
    root.render(
      <DevelopmentWorkbench client={client} isolationDomainId={scope} onDisconnect={() => {}} />,
    ),
  );
}
async function click(text: string) {
  const button = [...host.querySelectorAll("button")].find(
    (element) => element.textContent === text && !element.closest("[hidden]"),
  );
  expect(button, text).toBeDefined();
  await act(async () => button?.click());
}
async function name(value: string) {
  const input = host.querySelector<HTMLInputElement>('input[name="service-name"]');
  expect(input).not.toBeNull();
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(input, value);
    input?.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function visit(hash: string) {
  await act(async () => {
    window.history.pushState(null, "", hash);
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
}

it("retains an exact uncertain create attempt while browsing away and back", async () => {
  const { client } = fixture();
  const attempts: unknown[] = [];
  client.POST = (async (_path: string, options: unknown) => {
    attempts.push(options);
    throw new Error("Lost response");
  }) as DataGroundClient["POST"];
  await render(client);
  await click("New service");
  await name("Retained draft");
  await click("Create service");
  expect(attempts).toHaveLength(1);
  await click("Agent services");
  const retry = [...host.querySelectorAll("button")].find(
    (button) => button.textContent === "Retry service creation",
  );
  expect(retry?.closest("[hidden]")).toBeInstanceOf(HTMLElement);
  await visit(routeHash({ isolationDomainId: scope, view: "workflow" }));
  await click("Retry service creation");
  expect(attempts).toHaveLength(2);
  expect(attempts[1]).toEqual(attempts[0]);
});

it("loads an exact service absent from the directory and keeps its scoped URL", async () => {
  window.history.replaceState(
    null,
    "",
    routeHash({ isolationDomainId: scope, serviceId, view: "workflow" }),
  );
  const { client, reads } = fixture();
  await render(client);
  expect(host.querySelector("#workflow-title")?.textContent).toBe("Direct service");
  expect(reads.filter((path) => path.endsWith("/{serviceId}"))).toHaveLength(1);
  expect(reads.filter((path) => path.endsWith("/agent-services"))).toHaveLength(1);
  await click("Agent services");
  await visit(routeHash({ isolationDomainId: scope, serviceId, view: "workflow" }));
  expect(host.querySelector("#workflow-title")?.textContent).toBe("Direct service");
  expect(reads.filter((path) => path.endsWith("/{serviceId}"))).toHaveLength(1);
});

it("keeps a denied direct service out of command forms", async () => {
  window.history.replaceState(
    null,
    "",
    routeHash({ isolationDomainId: scope, serviceId, view: "workflow" }),
  );
  await render(fixture(absent).client);
  expect(host.textContent).toContain("Service unavailable");
  expect(
    [...host.querySelectorAll("form")].filter((form) => !form.closest("[hidden]")),
  ).toHaveLength(0);
});

it("does not restore a late service read after navigation away", async () => {
  let finish: (value: unknown) => void = () => {};
  const pending = new Promise((resolve) => {
    finish = resolve;
  });
  window.history.replaceState(
    null,
    "",
    routeHash({ isolationDomainId: scope, serviceId, view: "workflow" }),
  );
  await render(fixture(() => pending).client);
  await click("Agent services");
  await act(async () => finish(ok(service)));
  expect(host.querySelector("#workflow-title")?.textContent ?? "").not.toBe("Direct service");
  expect(window.location.hash).toBe(routeHash({ isolationDomainId: scope, view: "services" }));
});

it("clears drafts when the connection identity changes", async () => {
  await render(fixture().client);
  await click("New service");
  await name("Sensitive draft");
  await render(fixture().client);
  expect(host.querySelector<HTMLInputElement>('input[name="service-name"]')?.value).toBe("");
});
