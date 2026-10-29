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
async function render(client: DataGroundClient, presentation: "classic" | "workbench" = "classic") {
  await act(async () =>
    root.render(
      <DevelopmentWorkbench
        presentation={presentation}
        client={client}
        isolationDomainId={scope}
        onDisconnect={() => {}}
      />,
    ),
  );
}
async function click(text: string) {
  const button = [...host.querySelectorAll("button")].find(
    (element) => element.textContent === text && !element.closest("[hidden], [inert]"),
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
  expect(host.querySelector('[data-workbench-heading="workflow"]')?.textContent).toBe(
    "Direct service",
  );
  expect(reads.filter((path) => path.endsWith("/{serviceId}"))).toHaveLength(1);
  expect(reads.filter((path) => path.endsWith("/agent-services"))).toHaveLength(1);
  await click("Agent services");
  await visit(routeHash({ isolationDomainId: scope, serviceId, view: "workflow" }));
  expect(host.querySelector('[data-workbench-heading="workflow"]')?.textContent).toBe(
    "Direct service",
  );
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
  expect(host.querySelector('[data-workbench-heading="workflow"]')?.textContent ?? "").not.toBe(
    "Direct service",
  );
  expect(window.location.hash).toBe(routeHash({ isolationDomainId: scope, view: "services" }));
});

it("clears drafts when the connection identity changes", async () => {
  await render(fixture().client);
  await click("New service");
  await name("Sensitive draft");
  await render(fixture().client);
  expect(host.querySelector<HTMLInputElement>('input[name="service-name"]')?.value).toBe("");
});

it("retains independent revision drafts and exact retries for two services", async () => {
  const secondId = "svc_00000000000000000002";
  const attempts: unknown[] = [];
  const client = {
    GET: async (path: string, options: { params: { path: { serviceId?: string } } }) => {
      if (path.endsWith("/{serviceId}")) {
        const id = options.params.path.serviceId;
        return ok({
          ...service,
          name: id === serviceId ? "First service" : "Second service",
          metadata: { ...service.metadata, id },
        });
      }
      if (path.endsWith("/{alias}")) return absent("SERVICE_ALIAS_NOT_FOUND");
      return ok({ items: [] });
    },
    POST: async (_path: string, options: unknown) => {
      attempts.push(options);
      throw new Error("Lost response");
    },
  } as unknown as DataGroundClient;
  await render(client);
  const firstRoute = routeHash({ isolationDomainId: scope, serviceId, view: "workflow" });
  const secondRoute = routeHash({
    isolationDomainId: scope,
    serviceId: secondId,
    view: "workflow",
  });
  const capabilities = () =>
    [
      ...host.querySelectorAll<HTMLTextAreaElement>(
        'textarea[name="revision-required-capabilities"]',
      ),
    ].find((input) => !input.closest("[hidden]"));
  const edit = async (value: string) => {
    const input = capabilities();
    expect(input).toBeDefined();
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set?.call(
        input,
        value,
      );
      input?.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };
  await visit(firstRoute);
  await edit("tool");
  await visit(secondRoute);
  await edit("file");
  await visit(firstRoute);
  expect(capabilities()?.value).toBe("tool");
  await click("Create revision draft");
  await visit(secondRoute);
  expect(capabilities()?.value).toBe("file");
  await visit(firstRoute);
  await click("Retry revision creation");
  expect(attempts).toHaveLength(2);
  expect(attempts[1]).toEqual(attempts[0]);
  const ids = [...host.querySelectorAll("[id]")].map((element) => element.id);
  expect(new Set(ids).size).toBe(ids.length);
});

async function selectServiceTab(label: string) {
  const tab = [...host.querySelectorAll<HTMLElement>('[role="tab"]')].find(
    (element) => element.textContent === label,
  );
  expect(tab).toBeDefined();
  await act(async () => tab?.click());
}
it("retains an uncertain revision attempt across the five service views", async () => {
  window.history.replaceState(
    null,
    "",
    routeHash({ isolationDomainId: scope, serviceId, view: "workflow" }),
  );
  const { client } = fixture();
  const attempts: unknown[] = [];
  client.POST = (async (_path: string, options: unknown) => {
    attempts.push(options);
    throw new Error("Lost response");
  }) as DataGroundClient["POST"];
  await render(client, "workbench");
  await click("Create revision draft");
  expect(attempts).toHaveLength(1);
  await selectServiceTab("Overview");
  expect(window.location.hash).toContain("/overview");
  const retry = [...host.querySelectorAll("button")].find(
    (button) => button.textContent === "Retry revision creation",
  );
  expect(retry?.closest("[inert]")).toBeInstanceOf(HTMLElement);
  await selectServiceTab("Versions");
  await selectServiceTab("Deployments");
  await selectServiceTab("Runs");
  expect(window.location.hash).toContain("/runs");
  await selectServiceTab("Build");
  await click("Retry revision creation");
  expect(attempts).toHaveLength(2);
  expect(attempts[1]).toEqual(attempts[0]);
});
it("restores a direct versions link without changing it to build", async () => {
  const hash = routeHash({
    isolationDomainId: scope,
    serviceId,
    view: "workflow",
    serviceTab: "versions",
  });
  window.history.replaceState(null, "", hash);
  const { client } = fixture();
  await render(client, "workbench");
  expect(window.location.hash).toBe(hash);
  expect(host.querySelector('[role="tab"][aria-selected="true"]')?.textContent).toBe("Versions");
  await selectServiceTab("Overview");
  await visit(hash);
  expect(host.querySelector('[role="tab"][aria-selected="true"]')?.textContent).toBe("Versions");
});
