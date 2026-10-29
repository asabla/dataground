import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { DataGroundClient } from "../contracts/client";
import type { ServiceRevisionResource } from "../revisions";
import { RetainedServiceCommands } from "./RetainedServiceCommands";

const scope = "iso_00000000000000000001";
const serviceId = "svc_00000000000000000001";
const metadata = {
  id: serviceId,
  isolationDomainId: scope,
  version: 1,
  generation: 1,
  createdAt: "2026-10-29T08:00:00Z",
  updatedAt: "2026-10-29T08:00:00Z",
  createdBy: "operator",
};
const service = { metadata, name: "Retained command review" };
function revision(number: number): ServiceRevisionResource {
  return {
    metadata: { ...metadata, id: `rev_${String(number).padStart(20, "0")}` },
    serviceId,
    revisionNumber: number,
    state: "draft",
    runtimeProfile: "reference/v1",
    requiredCapabilities: [],
    inputSchema: {
      type: "object",
      properties: { prompt: { type: "string", minLength: 1, maxLength: 100 } },
      required: ["prompt"],
      additionalProperties: false,
    },
  };
}
const first = revision(1);
const second = revision(2);
const noop = () => {};
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
const visible = (element: Element) => !element.closest("[hidden], [inert]");
async function click(label: string) {
  const button = [...host.querySelectorAll("button")].find(
    (element) => visible(element) && element.textContent === label,
  );
  expect(button, label).toBeDefined();
  await act(async () => userEvent.click(button as HTMLButtonElement));
}
async function render(
  kind: "publication" | "route" | "invocation",
  client: DataGroundClient,
  draft = first,
  enabled = true,
  active = true,
) {
  const publication = {
    ...draft,
    state: "published" as const,
    publishedAt: metadata.updatedAt,
    metadata: { ...draft.metadata, version: 2 },
  };
  await act(async () =>
    root.render(
      <RetainedServiceCommands
        kind={kind}
        client={client}
        service={service}
        isolationDomainId={scope}
        enabled={enabled}
        active={active}
        revision={draft}
        publication={publication}
        alias={{
          metadata: { ...metadata, id: `als_${String(draft.revisionNumber).padStart(20, "0")}` },
          name: draft.revisionNumber === 1 ? "stable" : "candidate",
          serviceId,
          revisionId: draft.metadata.id,
        }}
        onAssignAlias={noop}
        onPublished={noop}
        onComposeInvocation={noop}
        onOpenInvocation={noop}
      />,
    ),
  );
}

it.each(["publication", "route", "invocation"] as const)(
  "retains the exact uncertain %s request across another selection and a discovery gap",
  async (kind) => {
    const send = vi.fn(async () => {
      throw new Error("Lost acknowledgement");
    });
    const client = { POST: send, PUT: send } as unknown as DataGroundClient;
    await render(kind, client);
    if (kind === "publication") {
      await click("Review publication");
      await click("Confirm publication");
    } else if (kind === "route") {
      await click("Review routing");
      await click("Confirm route change");
    } else {
      const input = [...host.querySelectorAll("textarea")].find(visible);
      expect(input).toBeDefined();
      await act(async () => userEvent.fill(input as HTMLTextAreaElement, "Retained prompt"));
      await click("Start invocation");
    }
    expect(send).toHaveBeenCalledOnce();
    await render(kind, client, first, false);
    expect([...host.querySelectorAll("button")].filter(visible)).toHaveLength(0);
    await render(kind, client, second);
    expect([...host.querySelectorAll('[role="alert"]')].filter(visible)).toHaveLength(0);
    await render(kind, client);
    await click(
      kind === "publication"
        ? "Retry publication"
        : kind === "route"
          ? "Retry route change"
          : "Retry invocation",
    );
    expect(send).toHaveBeenCalledTimes(2);
    expect(send.mock.calls[1]).toEqual(send.mock.calls[0]);
    const ids = [...host.querySelectorAll("[id]")].map((element) => element.id);
    expect(new Set(ids).size).toBe(ids.length);
  },
);

it("clears retained commands for a replacement client and rejects mismatched scope", async () => {
  const send = vi.fn(async () => {
    throw new Error("Lost acknowledgement");
  });
  await render("publication", { POST: send } as unknown as DataGroundClient);
  await click("Review publication");
  await click("Confirm publication");
  await render("publication", { POST: send } as unknown as DataGroundClient);
  expect(host.textContent).not.toContain("Retry publication");
  await render("publication", { POST: send } as unknown as DataGroundClient, {
    ...first,
    metadata: { ...first.metadata, isolationDomainId: "iso_00000000000000000002" },
  });
  expect(host.querySelector("button")).toBeNull();
  expect(send).toHaveBeenCalledOnce();
});

it("hides a retained publication dialog on navigation and restores the same review", async () => {
  const post = vi.fn();
  const client = { POST: post } as unknown as DataGroundClient;
  await render("publication", client);
  await click("Review publication");
  expect(host.querySelector('[role="dialog"]')?.textContent).toContain("Publish revision 1");
  await render("publication", client, first, true, false);
  expect(host.querySelector('[role="dialog"]')).toBeNull();
  await render("publication", client, first, true, true);
  expect(host.querySelector('[role="dialog"]')?.textContent).toContain("Publish revision 1");
  await click("Keep as draft");
  expect(host.querySelector('[role="dialog"]')).toBeNull();
  expect(post).not.toHaveBeenCalled();
});

it("hides retained route review on navigation and keeps pending routing in its dialog", async () => {
  let finish!: () => void;
  const send = vi.fn(
    () =>
      new Promise((resolve) => {
        finish = () =>
          resolve({
            response: new Response(null, { status: 503 }),
          });
      }),
  );
  const client = { PUT: send } as unknown as DataGroundClient;
  await render("route", client);
  await click("Review routing");
  expect(host.querySelector('[role="dialog"]')?.textContent).toContain("revision 1");
  await render("route", client, first, true, false);
  expect(host.querySelector('[role="dialog"]')).toBeNull();
  await render("route", client, first, true, true);
  expect(host.querySelector('[role="dialog"]')?.textContent).toContain("revision 1");
  await click("Confirm route change");
  expect(send).toHaveBeenCalledOnce();
  expect(host.querySelector('[role="dialog"]')?.textContent).toContain("Changing route");
  await act(async () => userEvent.keyboard("{Escape}"));
  expect(host.querySelector('[role="dialog"]')).not.toBeNull();
  await act(async () => finish());
  expect(host.querySelector('[role="dialog"]')).toBeNull();
  expect(host.textContent).toContain("Retry route change");
});
