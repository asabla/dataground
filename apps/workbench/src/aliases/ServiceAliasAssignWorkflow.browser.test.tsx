import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { DataGroundClient } from "../contracts/client";
import type { PublishedServiceRevisionResource } from "../revisions/publicationClient";
import { ServiceAliasAssignWorkflow } from "./ServiceAliasAssignWorkflow";

const metadata = {
  id: "rev_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
  createdAt: "2026-10-29T10:00:00Z",
  updatedAt: "2026-10-29T10:01:00Z",
  createdBy: "review",
  version: 2,
  generation: 1,
};
const revision: PublishedServiceRevisionResource = {
  metadata,
  serviceId: "svc_00000000000000000001",
  revisionNumber: 1,
  state: "published",
  publishedAt: metadata.updatedAt,
  runtimeProfile: "reference/v1",
  requiredCapabilities: [],
};
const stable = {
  metadata: { ...metadata, id: "als_00000000000000000001", version: 1 },
  name: "stable",
  serviceId: revision.serviceId,
  revisionId: metadata.id,
};
type PutOptions = {
  body: { expectedVersion: number; revisionId: string };
  params: { path: { alias: string }; header: { "Idempotency-Key": string } };
};
const result = (name: string) => ({
  data: {
    ...stable,
    name,
    metadata: {
      ...stable.metadata,
      id: name === "canary" ? "als_00000000000000000002" : "als_00000000000000000003",
    },
  },
  response: new Response(null, { status: 200 }),
});
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
function button(label: string) {
  return [...host.querySelectorAll<HTMLButtonElement>("button")].find(
    (element) => element.textContent === label,
  );
}
async function click(label: string) {
  const control = button(label);
  expect(control, label).toBeDefined();
  await act(async () => userEvent.click(control as HTMLButtonElement));
}
async function render(client: DataGroundClient, onAliasAssigned = vi.fn()) {
  await act(async () =>
    root.render(
      <ServiceAliasAssignWorkflow
        compact
        canAssign
        client={client}
        revision={revision}
        currentAlias={stable}
        onAliasAssigned={onAliasAssigned}
      />,
    ),
  );
}
async function prepare(name: string) {
  await click("Create another route");
  const input = host.querySelector<HTMLInputElement>('input[name="service-alias"]');
  expect(input?.value).toBe("");
  await expect.poll(() => document.activeElement).toBe(input);
  await act(async () => userEvent.fill(input as HTMLInputElement, name));
  await click("Review routing");
}

it("creates independent named routes with a zero-version precondition after observed or completed routing", async () => {
  const send = vi.fn(async (_path: string, options: PutOptions) =>
    result(options.params.path.alias),
  );
  await render({ PUT: send } as unknown as DataGroundClient);
  await prepare("canary");
  expect(send).not.toHaveBeenCalled();
  await click("Confirm route change");
  await prepare("preview");
  await click("Confirm route change");
  expect(send).toHaveBeenCalledTimes(2);
  expect(send.mock.calls.map(([, options]) => options.params.path.alias)).toEqual([
    "canary",
    "preview",
  ]);
  expect(send.mock.calls.map(([, options]) => options.body)).toEqual([
    { expectedVersion: 0, revisionId: metadata.id },
    { expectedVersion: 0, revisionId: metadata.id },
  ]);
  expect(send.mock.calls[0]?.[1].params.header["Idempotency-Key"]).not.toBe(
    send.mock.calls[1]?.[1].params.header["Idempotency-Key"],
  );
});

it("retains an uncertain new route and never offers a fresh command until it resolves", async () => {
  const send = vi.fn(async (_path: string, options: PutOptions) => {
    if (send.mock.calls.length === 1) throw new Error("Lost acknowledgement");
    return result(options.params.path.alias);
  });
  await render({ PUT: send } as unknown as DataGroundClient);
  await prepare("canary");
  await click("Confirm route change");
  expect(button("Create another route")).toBeUndefined();
  await click("Retry route change");
  expect(send.mock.calls[1]).toEqual(send.mock.calls[0]);
  expect(button("Create another route")).toBeDefined();
});

it("clears a new-route session on connection replacement and ignores the previous completion", async () => {
  let finish!: (value: ReturnType<typeof result>) => void;
  const onAssigned = vi.fn();
  const send = vi.fn(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  await render({ PUT: send } as unknown as DataGroundClient, onAssigned);
  await prepare("canary");
  await click("Confirm route change");
  const replacement = vi.fn();
  await render({ PUT: replacement } as unknown as DataGroundClient, onAssigned);
  expect(host.textContent).toContain("Already routed");
  await act(async () => finish(result("canary")));
  expect(onAssigned).not.toHaveBeenCalled();
  expect(replacement).not.toHaveBeenCalled();
  expect(host.textContent).not.toContain("canary now routes");
});
