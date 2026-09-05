import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import type { ServiceRevisionResource } from "./client";
import { ServiceRevisionPublishWorkflow } from "./ServiceRevisionPublishWorkflow";

const draft: ServiceRevisionResource = {
  metadata: {
    id: "rev_00000000000000000001",
    isolationDomainId: "iso_00000000000000000001",
    createdAt: "2026-09-05T12:00:00Z",
    updatedAt: "2026-09-05T12:00:00Z",
    createdBy: "operator",
    version: 1,
    generation: 1,
  },
  serviceId: "svc_00000000000000000001",
  revisionNumber: 1,
  runtimeProfile: "reference/v1",
  requiredCapabilities: [],
  state: "draft",
};
const operation = {
  metadata: { ...draft.metadata, id: "op_00000000000000000001" },
  resourceId: draft.metadata.id,
  kind: "service-publication",
  command: "publish",
  desiredState: "published",
  observedState: "queued",
  stateMachineVersion: 1,
  attempt: 0,
  correlationId: "cor_publication",
};
const published = {
  ...draft,
  state: "published",
  publishedAt: "2026-09-05T12:01:00Z",
  metadata: { ...draft.metadata, version: 2, generation: 2, updatedAt: "2026-09-05T12:01:00Z" },
};
const reply = (data: unknown, status = 200) => ({ data, response: new Response(null, { status }) });
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
  const found = Array.from(host.querySelectorAll("button")).find(
    (element) => element.textContent === label,
  );
  expect(found).toBeDefined();
  return found as HTMLButtonElement;
}
async function click(label: string) {
  await act(async () => button(label).click());
}
async function confirm() {
  await click("Review publication");
  await click("Confirm publication");
}

it("observes asynchronous completion before offering explicit alias assignment and locks duplicate reads", async () => {
  let resolve: ((value: unknown) => void) | undefined;
  let complete = false;
  const post = vi.fn(async () => reply(operation, 202));
  const get = vi.fn(async (path: string) => {
    if (!complete)
      return new Promise((done) => {
        resolve = done;
      });
    return reply(
      path.includes("operations") ? { ...operation, observedState: "published" } : published,
    );
  });
  const onPublished = vi.fn();
  const onAssignAlias = vi.fn();
  await act(async () =>
    root.render(
      <ServiceRevisionPublishWorkflow
        canPublish
        client={{ POST: post, GET: get } as unknown as DataGroundClient}
        revision={draft}
        onPublished={onPublished}
        onAssignAlias={onAssignAlias}
      />,
    ),
  );
  expect(post).not.toHaveBeenCalled();
  await confirm();
  expect(host.textContent).toContain("Publication accepted");
  expect(host.textContent).not.toContain("Assign alias");
  expect(onPublished).not.toHaveBeenCalled();
  expect(document.activeElement).toBe(host.querySelector('[aria-label="Revision publication"]'));
  await act(async () => button("Check publication").focus());
  await act(async () => {
    button("Check publication").click();
    button("Check publication").click();
  });
  expect(get).toHaveBeenCalledOnce();
  expect(button("Check publication").getAttribute("aria-disabled")).toBe("true");
  await act(async () => resolve?.(reply(operation)));
  expect(document.activeElement).toBe(button("Check publication"));
  expect(onPublished).not.toHaveBeenCalled();
  complete = true;
  await click("Check publication");
  expect(post).toHaveBeenCalledOnce();
  expect(get).toHaveBeenCalledTimes(3);
  expect(onPublished).toHaveBeenCalledExactlyOnceWith(published);
  expect(onAssignAlias).not.toHaveBeenCalled();
  expect(document.activeElement).toBe(host.querySelector('[aria-label="Revision publication"]'));
  await click("Assign alias");
  expect(onAssignAlias).toHaveBeenCalledExactlyOnceWith(published);
});

it("recovers a lost acceptance with the same key then retries only reads", async () => {
  const post = vi.fn(async () => {
    if (post.mock.calls.length === 1) throw new Error("lost acknowledgement");
    return reply({ ...operation, resourceId: undefined }, 202);
  });
  const get = vi.fn(async () => {
    if (get.mock.calls.length === 1) throw new Error("connection lost");
    return reply({ ...operation, observedState: "failed" });
  });
  const key = vi.fn(() => "publication:recovery0001");
  await act(async () =>
    root.render(
      <ServiceRevisionPublishWorkflow
        canPublish
        client={{ POST: post, GET: get } as unknown as DataGroundClient}
        revision={draft}
        createIdempotencyKey={key}
      />,
    ),
  );
  await confirm();
  await click("Retry publication");
  expect(post).toHaveBeenCalledTimes(2);
  expect(post.mock.calls[0]).toEqual(post.mock.calls[1]);
  expect(key).toHaveBeenCalledOnce();
  await click("Check publication");
  expect(host.querySelector('[role="alert"]')).not.toBeNull();
  await click("Check publication");
  expect(host.textContent).toContain("Publication failed.");
  expect(host.textContent).not.toContain("Assign alias");
  expect(post).toHaveBeenCalledTimes(2);
});

it.each(["client", "scope", "unmount"])(
  "fences an accepted publication read after %s changes",
  async (change) => {
    let resolve: ((value: unknown) => void) | undefined;
    const client = {
      POST: async () => reply(operation, 202),
      GET: () =>
        new Promise((done) => {
          resolve = done;
        }),
    } as unknown as DataGroundClient;
    const onPublished = vi.fn();
    await act(async () =>
      root.render(
        <ServiceRevisionPublishWorkflow
          canPublish
          client={client}
          revision={draft}
          onPublished={onPublished}
        />,
      ),
    );
    await confirm();
    await click("Check publication");
    await act(async () =>
      root.render(
        change === "unmount" ? null : (
          <ServiceRevisionPublishWorkflow
            canPublish
            client={change === "client" ? ({} as DataGroundClient) : client}
            revision={
              change === "scope"
                ? { ...draft, metadata: { ...draft.metadata, id: "rev_00000000000000000002" } }
                : draft
            }
            onPublished={onPublished}
          />
        ),
      ),
    );
    await act(async () => resolve?.(reply({ ...operation, observedState: "failed" })));
    expect(onPublished).not.toHaveBeenCalled();
    expect(host.textContent).not.toContain("Publication accepted");
    expect(host.textContent).not.toContain("Publication failed.");
  },
);

it("withholds publication controls from observers and routing after denied observation", async () => {
  const post = vi.fn(async () => reply(operation, 202));
  const client = {
    POST: post,
    GET: async () => ({
      response: new Response(null, { status: 403 }),
      error: {
        error: {
          code: "REQUEST_DENIED",
          message: "Access denied.",
          correlationId: "cor_denied",
          retryable: false,
        },
      },
    }),
  } as unknown as DataGroundClient;
  const onPublished = vi.fn();
  const render = (canPublish: boolean) => (
    <ServiceRevisionPublishWorkflow
      canPublish={canPublish}
      client={client}
      revision={draft}
      onPublished={onPublished}
    />
  );
  await act(async () => root.render(render(false)));
  expect(button("Review publication").disabled).toBe(true);
  expect(post).not.toHaveBeenCalled();
  await act(async () => root.render(render(true)));
  await confirm();
  await act(async () => root.render(render(false)));
  await click("Check publication");
  expect(host.textContent).toContain("Access denied.");
  expect(host.textContent).not.toContain("Assign alias");
  expect(onPublished).not.toHaveBeenCalled();
  expect(post).toHaveBeenCalledOnce();
});
