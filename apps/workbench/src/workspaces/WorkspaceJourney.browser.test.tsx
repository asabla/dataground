import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it } from "vitest";
import { userEvent } from "vitest/browser";
import type { DataGroundClient } from "../contracts/client";
import { DevelopmentWorkbench } from "../workbench/DevelopmentWorkbench";

const scope = "iso_00000000000000000001",
  workspaceId = "wsp_00000000000000000001",
  serviceId = "svc_00000000000000000001";
const metadata = {
  id: workspaceId,
  isolationDomainId: scope,
  createdBy: "fixture",
  createdAt: "2026-10-29T08:00:00Z",
  updatedAt: "2026-10-29T08:00:00Z",
  version: 1,
  generation: 1,
};
const workspace = { metadata, name: "Research" };
const service = { metadata: { ...metadata, id: serviceId }, name: "Retained analyst", workspaceId };
const ok = (data: unknown, status = 200) => ({ data, response: new Response(null, { status }) });
const denied = () => ({
  error: {
    error: {
      code: "ACCESS_DENIED",
      message: "Workspace access is denied.",
      retryable: false,
      correlationId: "cor-fixture",
    },
  },
  response: new Response(null, { status: 403 }),
});
let host: HTMLDivElement, root: Root;
beforeEach(() => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  window.history.replaceState(null, "", `#/scopes/${scope}/workspaces`);
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
const button = (name: string) =>
  [...host.querySelectorAll("button")].find(
    (el) => visible(el) && (el.getAttribute("aria-label") ?? el.textContent?.trim()) === name,
  );
async function click(name: string) {
  const el = button(name);
  expect(el).toBeDefined();
  await act(async () => userEvent.click(el as HTMLButtonElement));
}
async function field(label: string, value: string) {
  const element = [...host.querySelectorAll("label")].find(
    (el) => visible(el) && el.textContent === label,
  );
  expect(element).toBeDefined();
  const input = document.getElementById(element?.htmlFor ?? "");
  expect(input).not.toBeNull();
  await act(async () => userEvent.fill(input as HTMLInputElement, value));
}

it("retains a workspace service draft and exact retry across navigation and opens the exact service", async () => {
  let created = false;
  const attempts: unknown[] = [];
  const reads: string[] = [];
  const client = {
    GET: async (path: string) => {
      reads.push(path);
      if (path.endsWith("/workspaces")) return ok({ items: [workspace] });
      if (path.endsWith("/{workspaceId}")) return ok(workspace);
      if (path.endsWith("/{serviceId}")) return ok(service);
      if (path.endsWith("/agent-services")) return ok({ items: created ? [service] : [] });
      return ok({ items: [] });
    },
    POST: async (path: string, options: unknown) => {
      attempts.push({ path, options });
      if (attempts.length === 1) throw new Error("lost acknowledgement");
      created = true;
      return ok(service, 201);
    },
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <DevelopmentWorkbench client={client} isolationDomainId={scope} onDisconnect={() => {}} />,
    ),
  );
  await click("Open workspace Research");
  await click("New service in workspace");
  await field("Service name", "Retained analyst");
  await click("Home");
  await click("Workspace");
  expect((host.querySelector('input[name="service-name"]') as HTMLInputElement).value).toBe(
    "Retained analyst",
  );
  await click("Create service");
  await click("Home");
  await click("Workspace");
  await click("Retry service creation");
  expect(attempts).toHaveLength(2);
  expect(attempts[0]).toEqual(attempts[1]);
  expect((attempts[0] as { path: string }).path).toContain(
    "/workspaces/{workspaceId}/agent-services",
  );
  expect(document.activeElement?.textContent).toBe("Agent services");
  await click("Open service Retained analyst");
  expect(window.location.hash).toContain(`/agents/${serviceId}/overview`);
  expect(reads).toContain("/v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}");
  await click("Back to workspace");
  expect(window.location.hash).toBe(`#/scopes/${scope}/workspaces/${workspaceId}`);
  await click("New service in workspace");
  expect((host.querySelector('input[name="service-name"]') as HTMLInputElement).value).toBe("");
});
it("keeps a pending workspace request without navigating away from the user's new location", async () => {
  let complete!: (value: unknown) => void;
  const pending = new Promise((resolve) => {
    complete = resolve;
  });
  let calls = 0;
  const client = {
    GET: async () => ok({ items: [] }),
    POST: async () => {
      calls++;
      return pending;
    },
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <DevelopmentWorkbench client={client} isolationDomainId={scope} onDisconnect={() => {}} />,
    ),
  );
  await click("New workspace");
  await field("Workspace name", "Research");
  await click("Create workspace");
  await click("Home");
  await act(async () => complete(ok(workspace, 201)));
  expect(calls).toBe(1);
  expect(window.location.hash).toBe(`#/scopes/${scope}/home`);
  await click("Workspace");
  await click("New workspace");
  expect(
    (
      document.getElementById(
        [...host.querySelectorAll("label")].find((el) => el.textContent === "Workspace name")
          ?.htmlFor ?? "",
      ) as HTMLInputElement
    ).value,
  ).toBe("");
});
it("does not treat denied access as an empty workspace or fetch service contents", async () => {
  const paths: string[] = [];
  window.history.replaceState(null, "", `#/scopes/${scope}/workspaces/${workspaceId}`);
  const client = {
    GET: async (path: string) => {
      paths.push(path);
      return denied();
    },
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <DevelopmentWorkbench client={client} isolationDomainId={scope} onDisconnect={() => {}} />,
    ),
  );
  expect(
    [...host.querySelectorAll('[role="alert"]')]
      .filter(visible)
      .map((el) => el.textContent)
      .join(" "),
  ).toContain("Workspace access is denied.");
  expect(paths.some((path) => path.endsWith("/workspaces/{workspaceId}/agent-services"))).toBe(
    false,
  );
  expect(button("New service in workspace")).toBeUndefined();
});

it("does not redirect a pending workspace creation over another workspace detail", async () => {
  let complete!: (value: unknown) => void;
  const pending = new Promise((resolve) => {
    complete = resolve;
  });
  const client = {
    GET: async (path: string) =>
      path.endsWith("/workspaces")
        ? ok({ items: [workspace] })
        : path.endsWith("/{workspaceId}")
          ? ok(workspace)
          : ok({ items: [] }),
    POST: async () => pending,
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <DevelopmentWorkbench client={client} isolationDomainId={scope} onDisconnect={() => {}} />,
    ),
  );
  await click("New workspace");
  await field("Workspace name", "Other research");
  await click("Create workspace");
  await click("Open workspace Research");
  await act(async () =>
    complete(
      ok(
        {
          ...workspace,
          name: "Other research",
          metadata: { ...metadata, id: "wsp_00000000000000000002" },
        },
        201,
      ),
    ),
  );
  expect(window.location.hash).toBe(`#/scopes/${scope}/workspaces/${workspaceId}`);
});

it("opens creation from the shell and retains exact workspace recovery across resource kinds", async () => {
  const attempts: unknown[] = [];
  const client = {
    GET: async () => ok({ items: [] }),
    POST: async (_path: string, options: unknown) => {
      attempts.push(options);
      throw new Error("lost acknowledgement");
    },
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <DevelopmentWorkbench client={client} isolationDomainId={scope} onDisconnect={() => {}} />,
    ),
  );
  await click("Home");
  const create = async (kind: "Workspace" | "Ungrouped agent service") => {
    await click("Create resource");
    const items = [...host.querySelectorAll<HTMLElement>('[role="menuitem"]')];
    expect(items.map((item) => item.textContent)).toEqual(["Ungrouped agent service", "Workspace"]);
    const item = items.find((item) => item.textContent === kind);
    await act(async () => userEvent.click(item as HTMLElement));
  };
  await create("Workspace");
  await expect.poll(() => document.activeElement?.textContent).toBe("New workspace");
  await field("Workspace name", "Retained shell draft");
  await click("Create workspace");
  expect(attempts).toHaveLength(1);
  await create("Ungrouped agent service");
  expect(window.location.hash).toContain("/agents/new");
  await field("Service name", "Independent service draft");
  await create("Workspace");
  await expect.poll(() => document.activeElement?.textContent).toBe("New workspace");
  await click("Retry workspace creation");
  expect(attempts).toHaveLength(2);
  expect(attempts[1]).toEqual(attempts[0]);
  await create("Ungrouped agent service");
  expect((host.querySelector('input[name="service-name"]') as HTMLInputElement).value).toBe(
    "Independent service draft",
  );
  await click("Create resource");
  await act(async () => userEvent.keyboard("{Escape}"));
  await expect.poll(() => document.activeElement).toBe(button("Create resource"));
  expect(attempts).toHaveLength(2);
  await act(async () =>
    root.render(
      <DevelopmentWorkbench
        client={{ ...client }}
        isolationDomainId={scope}
        onDisconnect={() => {}}
      />,
    ),
  );
  await create("Workspace");
  const label = [...host.querySelectorAll("label")].find(
    (label) => visible(label) && label.textContent === "Workspace name",
  );
  expect((document.getElementById(label?.htmlFor ?? "") as HTMLInputElement).value).toBe("");
  expect(button("Retry workspace creation")).toBeUndefined();
});
