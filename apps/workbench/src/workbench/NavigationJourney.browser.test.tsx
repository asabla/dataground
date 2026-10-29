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
  const button = [...host.querySelectorAll<HTMLElement>('button, [role="tab"]')].find(
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

it("changes theme without replacing an unfinished command form", async () => {
  const { client } = fixture();
  await render(client, "workbench");
  await click("New service");
  await name("Theme-safe draft");
  const input = host.querySelector('input[name="service-name"]');
  const label = [...host.querySelectorAll("label")].find(
    (element) => element.textContent === "Theme",
  );
  const select = document.getElementById(label?.htmlFor ?? "") as HTMLSelectElement;
  expect(select).not.toBeNull();
  for (const theme of ["light", "night", "system"]) {
    await act(async () => {
      select.value = theme;
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(host.querySelector("main")?.getAttribute("data-theme")).toBe(
      theme === "system" ? null : theme,
    );
    expect(host.querySelector('input[name="service-name"]')).toBe(input);
    expect((input as HTMLInputElement).value).toBe("Theme-safe draft");
  }
});

const runId = "inv_00000000000000000001";
const run = {
  metadata: { ...service.metadata, id: runId },
  serviceId,
  revisionId: "rev_00000000000000000001",
  operationId: "op_00000000000000000001",
  correlationId: "cor_navigation",
  alias: "stable",
  state: "succeeded",
  artifactIds: [],
  input: {},
};
function runFixture(exact: () => unknown = () => ok(run)) {
  const source = fixture();
  const baseGet = source.client.GET;
  source.client.GET = (async (path: string, options: never) => {
    if (path.endsWith("/invocations/{invocationId}")) {
      source.reads.push(path);
      return exact();
    }
    if (path.endsWith("/operations/{operationId}"))
      return ok({
        metadata: { ...service.metadata, id: run.operationId },
        kind: "invocation",
        command: "invoke",
        desiredState: "succeeded",
        observedState: "succeeded",
        stateMachineVersion: 1,
        attempt: 1,
        correlationId: run.correlationId,
      });
    return baseGet(path as never, options);
  }) as DataGroundClient["GET"];
  return source;
}
const runRoute = (runTab?: "output" | "files") =>
  routeHash({
    isolationDomainId: scope,
    serviceId,
    view: "interactions",
    invocationId: runId,
    ...(runTab ? { runTab } : {}),
  });
function selectedRunTab() {
  return [...host.querySelectorAll('[role="tab"][aria-selected="true"]')].find((element) =>
    element.closest('[aria-label="Run views"]'),
  )?.textContent;
}
it("opens an exact run outside the list and restores its view through browser navigation", async () => {
  window.history.replaceState(null, "", runRoute("output"));
  const source = runFixture();
  await render(source.client, "workbench");
  expect(selectedRunTab()).toBe("Output");
  expect(host.textContent).toContain("Show the completed result using your current access.");
  expect(source.reads.some((path) => path.endsWith("/result"))).toBe(false);
  await click("Files");
  expect(window.location.hash).toBe(runRoute("files"));
  await visit(runRoute("output"));
  expect(selectedRunTab()).toBe("Output");
  await click("Back to invocation history");
  expect(window.location.hash).toBe(
    routeHash({ isolationDomainId: scope, serviceId, view: "interactions" }),
  );
  const before = source.reads.filter((path) => path.endsWith("/invocations/{invocationId}")).length;
  await visit(runRoute("output"));
  expect(selectedRunTab()).toBe("Output");
  expect(source.reads.filter((path) => path.endsWith("/invocations/{invocationId}"))).toHaveLength(
    before,
  );
});
it("does not inspect a direct run belonging to another service", async () => {
  window.history.replaceState(null, "", runRoute("output"));
  await render(
    runFixture(() => ok({ ...run, serviceId: "svc_00000000000000000002" })).client,
    "workbench",
  );
  expect(host.textContent).toContain("This run does not belong to the selected service.");
  expect(selectedRunTab()).toBeUndefined();
  expect(host.querySelector('[aria-label="Run outcome"]')).toBeNull();
});
it("discards a late direct-run read after returning to history", async () => {
  let finish: (value: unknown) => void = () => {};
  const pending = new Promise((resolve) => {
    finish = resolve;
  });
  window.history.replaceState(null, "", runRoute("output"));
  await render(runFixture(() => pending).client, "workbench");
  await click("Back to invocation history");
  await act(async () => finish(ok(run)));
  expect(selectedRunTab()).toBeUndefined();
  expect(host.querySelector('[aria-label="Run outcome"]')).toBeNull();
});
it("keeps denied run locations out of inspection and permits a new explicit read", async () => {
  let allow = false;
  window.history.replaceState(null, "", runRoute("output"));
  await render(runFixture(() => (allow ? ok(run) : absent("FORBIDDEN"))).client, "workbench");
  expect(host.textContent).toContain("Run unavailable");
  expect(selectedRunTab()).toBeUndefined();
  allow = true;
  await click("Retry run read");
  expect(selectedRunTab()).toBe("Output");
});

it("keeps an uncertain draft when opening another version and starting revision work again", async () => {
  const revision = {
    metadata: { ...service.metadata, id: "rev_00000000000000000001", version: 2, generation: 2 },
    serviceId,
    revisionNumber: 1,
    state: "published",
    publishedAt: service.metadata.updatedAt,
    runtimeProfile: "reference/v1",
    requiredCapabilities: [],
  };
  const attempts: unknown[] = [];
  const client = {
    GET: async (path: string) => {
      if (path.endsWith("/{serviceId}")) return ok(service);
      if (path.endsWith("/{revisionId}")) return ok(revision);
      if (path.endsWith("/{alias}")) return absent("SERVICE_ALIAS_NOT_FOUND");
      if (path.endsWith("/revisions")) return ok({ items: [revision] });
      return ok({ items: [] });
    },
    POST: async (_path: string, options: unknown) => {
      attempts.push(options);
      throw new Error("Lost response");
    },
  } as unknown as DataGroundClient;
  window.history.replaceState(
    null,
    "",
    routeHash({ isolationDomainId: scope, serviceId, view: "workflow", serviceTab: "versions" }),
  );
  await render(client, "workbench");
  await click("New revision");
  await click("Create revision draft");
  expect(attempts).toHaveLength(1);
  await selectServiceTab("Versions");
  const open = host.querySelector<HTMLButtonElement>('button[aria-label="Open revision 1"]');
  expect(open).not.toBeNull();
  await act(async () => open?.click());
  expect(host.querySelector('[role="tab"][aria-selected="true"]')?.textContent).toBe("Deployments");
  await selectServiceTab("Versions");
  await click("New revision");
  await click("Retry revision creation");
  expect(attempts).toHaveLength(2);
  expect(attempts[1]).toEqual(attempts[0]);
});

it("starts a clean draft only after the previous draft was confirmed", async () => {
  const { client } = fixture();
  const created = {
    metadata: { ...service.metadata, id: "rev_00000000000000000002" },
    serviceId,
    revisionNumber: 1,
    state: "draft",
    runtimeProfile: "reference/v1",
    requiredCapabilities: [],
  };
  client.POST = (async () => ok(created)) as DataGroundClient["POST"];
  window.history.replaceState(
    null,
    "",
    routeHash({ isolationDomainId: scope, serviceId, view: "workflow" }),
  );
  await render(client, "workbench");
  await click("Create revision draft");
  await click("Open revision");
  await selectServiceTab("Versions");
  await click("New revision");
  expect(
    [...host.querySelectorAll("button")].some(
      (button) =>
        button.textContent === "Create revision draft" && !button.closest("[hidden], [inert]"),
    ),
  ).toBe(true);
  expect(
    [...host.querySelectorAll("button")].some(
      (button) => button.textContent === "Open revision" && !button.closest("[hidden], [inert]"),
    ),
  ).toBe(false);
});

const artifactId = "art_00000000000000000001";
const artifact = {
  metadata: { ...service.metadata, id: artifactId },
  invocationId: runId,
  digest: `sha256:${"a".repeat(64)}`,
  kind: "file",
  mediaType: "text/plain",
  name: "Reviewed output.txt",
  sensitive: true,
  sizeBytes: 20,
  state: "available",
};
const artifactRoute = () =>
  routeHash({
    isolationDomainId: scope,
    serviceId,
    invocationId: runId,
    view: "interactions",
    runTab: "files",
    artifactId,
  });
function artifactFixture(read: () => unknown = () => ok(artifact)) {
  const source = runFixture();
  const baseGet = source.client.GET;
  source.client.GET = (async (path: string, options: never) => {
    // Exact links remain usable when the artifact is not in the loaded list page.
    if (path.endsWith("/artifacts")) return ok({ items: [] });
    if (path.endsWith("/artifacts/{artifactId}")) {
      source.reads.push(path);
      return read();
    }
    return baseGet(path as never, options);
  }) as DataGroundClient["GET"];
  return source;
}
it("restores an artifact link in its originating run without reading protected content", async () => {
  const source = artifactFixture();
  window.history.replaceState(null, "", artifactRoute());
  await render(source.client, "workbench");
  expect(selectedRunTab()).toBe("Files");
  expect(host.textContent).toContain(artifact.name);
  expect(source.reads.some((path) => path.endsWith("/content"))).toBe(false);
  await click("Close artifact");
  expect(window.location.hash).toBe(runRoute("files"));
  expect(host.textContent).not.toContain(artifact.name);
  await visit(artifactRoute());
  expect(host.textContent).toContain(artifact.name);
  await click("Activity");
  expect(window.location.hash).toContain(`/activity/artifacts/${artifactId}`);
  expect(host.textContent).toContain(artifact.name);
  expect(source.reads.some((path) => path.endsWith("/content"))).toBe(false);
});
it("does not restore an artifact from a late read after its location was closed", async () => {
  let finish!: (value: unknown) => void;
  const source = artifactFixture(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  window.history.replaceState(null, "", artifactRoute());
  await render(source.client, "workbench");
  await click("Close artifact");
  await act(async () => finish(ok(artifact)));
  expect(host.textContent).not.toContain(artifact.name);
  expect(window.location.hash).toBe(runRoute("files"));
});
it("keeps artifact access failures visible without requesting file content", async () => {
  const source = artifactFixture(() => ({
    error: {
      error: {
        code: "FORBIDDEN",
        message: "Access denied.",
        correlationId: "cor_artifact",
        retryable: false,
      },
    },
    response: new Response(null, { status: 403 }),
  }));
  window.history.replaceState(null, "", artifactRoute());
  await render(source.client, "workbench");
  expect(host.querySelector('[role="alert"]')).not.toBeNull();
  expect(host.textContent).not.toContain(artifact.name);
  expect(source.reads.some((path) => path.endsWith("/content"))).toBe(false);
});

const linkedRevisionId = "rev_00000000000000000003";
const linkedRevision = {
  metadata: { ...service.metadata, id: linkedRevisionId },
  serviceId,
  revisionNumber: 3,
  state: "draft",
  runtimeProfile: "reference/v1",
  requiredCapabilities: [],
};
const revisionRoute = (serviceTab?: "overview" | "versions" | "deployments") =>
  routeHash({
    isolationDomainId: scope,
    serviceId,
    view: "workflow",
    revisionId: linkedRevisionId,
    ...(serviceTab ? { serviceTab } : {}),
  });
function revisionFixture(
  exact: () => unknown = () => ok(linkedRevision),
  list: () => unknown = () => ok({ items: [] }),
) {
  const source = fixture();
  const baseGet = source.client.GET;
  source.client.GET = (async (path: string, options: never) => {
    if (path.endsWith("/{revisionId}")) {
      source.reads.push(path);
      return exact();
    }
    if (path.endsWith("/revisions")) return list();
    return baseGet(path as never, options);
  }) as DataGroundClient["GET"];
  return source;
}
it("opens an exact revision outside the list and retains it across service views and refresh", async () => {
  const source = revisionFixture();
  window.history.replaceState(null, "", revisionRoute());
  await render(source.client, "workbench");
  await selectServiceTab("Overview");
  expect(window.location.hash).toBe(revisionRoute("overview"));
  expect(host.querySelector('[aria-label="Service overview"]')?.textContent).toContain(
    "Revision 3",
  );
  await selectServiceTab("Versions");
  expect(window.location.hash).toBe(revisionRoute("versions"));
  await visit(revisionRoute());
  expect(source.reads.filter((path) => path.endsWith("/{revisionId}"))).toHaveLength(1);
  await act(async () => root.unmount());
  root = createRoot(host);
  await render(source.client, "workbench");
  expect(source.reads.filter((path) => path.endsWith("/{revisionId}"))).toHaveLength(2);
  await selectServiceTab("Overview");
  expect(host.querySelector('[aria-label="Service overview"]')?.textContent).toContain(
    "Revision 3",
  );
});
it("does not replace a linked revision with a late newest-revision list", async () => {
  let finish!: (value: unknown) => void;
  const source = revisionFixture(
    undefined,
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  window.history.replaceState(null, "", revisionRoute("overview"));
  await render(source.client, "workbench");
  await act(async () =>
    finish(
      ok({
        items: [
          {
            ...linkedRevision,
            metadata: { ...linkedRevision.metadata, id: "rev_00000000000000000009" },
            revisionNumber: 9,
          },
        ],
      }),
    ),
  );
  expect(host.querySelector('[aria-label="Service overview"]')?.textContent).toContain(
    "Revision 3",
  );
  expect(window.location.hash).toBe(revisionRoute("overview"));
});
it("keeps a denied or mismatched direct revision out of commands", async () => {
  for (const read of [
    () => absent(),
    () => ok({ ...linkedRevision, serviceId: "svc_00000000000000000002" }),
  ]) {
    const source = revisionFixture(read);
    window.history.replaceState(null, "", revisionRoute());
    await render(source.client, "workbench");
    expect(host.querySelector('[role="alert"]')).not.toBeNull();
    expect(
      [...host.querySelectorAll("form")].filter((form) => !form.closest("[hidden], [inert]")),
    ).toHaveLength(0);
  }
});
it("does not restore a late revision after starting a new draft", async () => {
  let finish!: (value: unknown) => void;
  const source = revisionFixture(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  window.history.replaceState(null, "", revisionRoute("versions"));
  await render(source.client, "workbench");
  await click("New revision");
  await act(async () => finish(ok(linkedRevision)));
  expect(window.location.hash).not.toContain("/revisions/");
  expect(
    [...host.querySelectorAll("button")].some(
      (button) =>
        button.textContent === "Create revision draft" && !button.closest("[hidden], [inert]"),
    ),
  ).toBe(true);
});

it("retains the scope run filter after an independently denied service read", async () => {
  const reads: string[] = [];
  const client = {
    GET: async (path: string) => {
      reads.push(path);
      if (path.endsWith("/{serviceId}")) return absent("FORBIDDEN");
      if (path === "/v1/isolation-domains/{isolationDomainId}/invocations")
        return ok({
          items: [
            {
              metadata: { ...service.metadata, id: "inv_00000000000000000001" },
              serviceId,
              serviceName: service.name,
              revisionId: "rev_00000000000000000001",
              alias: "stable",
              state: "failed",
              operationId: "op_00000000000000000001",
              correlationId: "cor_navigation",
            },
          ],
        });
      return ok({ items: [] });
    },
  } as unknown as DataGroundClient;
  await visit(routeHash({ isolationDomainId: scope, view: "interactions" }));
  await render(client, "workbench");
  const field = Array.from(host.querySelectorAll("select")).find((element) =>
    element.closest(".dg-select-field")?.textContent?.includes("Run state"),
  );
  expect(field).toBeDefined();
  await act(async () => {
    if (field) {
      field.value = "failed";
      field.dispatchEvent(new Event("change", { bubbles: true }));
    }
  });
  const inspect = host.querySelector<HTMLButtonElement>(
    '[aria-label="Inspect Direct service, stable, failed"]',
  );
  await act(async () => inspect?.click());
  expect(window.location.hash).toContain("inv_00000000000000000001");
  expect(host.textContent).toContain("Service unavailable");
  expect(reads.some((path) => path.endsWith("/invocations/{invocationId}"))).toBe(false);
  await click("Runs");
  expect(field?.value).toBe("failed");
  expect(host.querySelector("#scope-runs-title")?.closest("[hidden]")).toBeNull();
  expect(
    reads.filter((path) => path === "/v1/isolation-domains/{isolationDomainId}/invocations"),
  ).toHaveLength(2);
});

it("resumes recent work from Home without losing drafts and clears recents on connection change", async () => {
  const { client } = fixture();
  await render(client, "workbench");
  await click("New service");
  await name("Home-retained draft");
  const input = host.querySelector('input[name="service-name"]');
  await click("Home");
  expect(window.location.hash).toBe(routeHash({ isolationDomainId: scope, view: "home" }));
  expect(document.activeElement).toBe(host.querySelector("#workbench-home-title"));
  expect(
    host.querySelector('[aria-label="Recent work in this connection"]')?.textContent,
  ).toContain("Unsaved service draft");
  await click("Resume");
  expect(host.querySelector('input[name="service-name"]')).toBe(input);
  expect((input as HTMLInputElement).value).toBe("Home-retained draft");
  await visit(routeHash({ isolationDomainId: scope, view: "home" }));
  await render(fixture().client, "workbench");
  expect(
    host.querySelector('[aria-label="Recent work in this connection"]')?.textContent,
  ).toContain("No work opened in this connection yet");
  expect(host.querySelector('input[name="service-name"]')).toBeNull();
});

it("resumes the last service view and exact run location from open work and Home", async () => {
  const { client } = fixture();
  await render(client, "workbench");
  const versions = routeHash({
    isolationDomainId: scope,
    serviceId,
    view: "workflow",
    serviceTab: "versions",
  });
  await visit(versions);
  await click("Home");
  await click("Resume");
  expect(window.location.hash).toBe(versions);
  const requests = routeHash({
    isolationDomainId: scope,
    serviceId,
    view: "interactions",
    invocationId: "inv_00000000000000000001",
    runTab: "requests",
  });
  await visit(requests);
  await click("Home");
  await click("Direct service");
  expect(window.location.hash).toBe(requests);
  await click("Home");
  await render(fixture().client, "workbench");
  expect(host.querySelector('[aria-label="Open service work"]')).toBeNull();
});
