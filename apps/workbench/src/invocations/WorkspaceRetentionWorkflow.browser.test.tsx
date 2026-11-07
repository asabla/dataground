import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { WorkspaceRetentionWorkflow } from "./WorkspaceRetentionWorkflow";

const reference = {
  isolationDomainId: "iso_00000000000000000001",
  invocationId: "inv_00000000000000000001",
};
const artifactId = "art_00000000000000000001";
function response(data: unknown) {
  return { data, response: new Response(null, { status: 200 }) };
}
function recorded() {
  return response({
    schemaVersion: "dataground.invocation-workspace-retention/v1",
    ...reference,
    state: "recorded",
    recovery: "none",
    artifactId,
    recordedAt: "2026-11-07T23:00:00Z",
  });
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((complete) => {
    resolve = complete;
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
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});
function button(label: string) {
  return Array.from(host.querySelectorAll("button")).find((item) => item.textContent === label);
}

it("removes old artifact actions during refresh and after denial", async () => {
  const pending = deferred<ReturnType<typeof recorded>>();
  const read = vi.fn().mockResolvedValueOnce(recorded()).mockReturnValueOnce(pending.promise);
  const client = { GET: read } as unknown as DataGroundClient;
  const inspect = vi.fn();
  await act(async () =>
    root.render(
      <WorkspaceRetentionWorkflow
        active
        client={client}
        reference={reference}
        revision={1}
        onInspectArtifact={inspect}
      />,
    ),
  );
  await act(async () => button("Inspect workspace changes")?.click());
  expect(inspect).toHaveBeenCalledWith(artifactId);
  await act(async () => button("Refresh retention status")?.click());
  expect(button("Inspect workspace changes")).toBeUndefined();
  expect(button("Refresh retention status")?.disabled).toBe(true);
  await act(async () =>
    pending.resolve({ data: undefined, response: new Response(null, { status: 403 }) }),
  );
  expect(host.textContent).toContain("You do not have access");
  expect(button("Inspect workspace changes")).toBeUndefined();
  expect(read).toHaveBeenCalledTimes(2);
});

it("ignores a late response after identity replacement", async () => {
  const pending = deferred<ReturnType<typeof recorded>>();
  const oldClient = { GET: () => pending.promise } as unknown as DataGroundClient;
  const newClient = {
    GET: async () => ({ response: new Response(null, { status: 403 }) }),
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <WorkspaceRetentionWorkflow active client={oldClient} reference={reference} revision={1} />,
    ),
  );
  await act(async () =>
    root.render(
      <WorkspaceRetentionWorkflow active client={newClient} reference={reference} revision={1} />,
    ),
  );
  await act(async () => pending.resolve(recorded()));
  expect(host.textContent).toContain("You do not have access");
  expect(host.textContent).not.toContain("Changes recorded");
});

it("discards inactive state and checks access again when the run returns", async () => {
  const pending = deferred<ReturnType<typeof recorded>>();
  const read = vi.fn().mockResolvedValueOnce(recorded()).mockReturnValueOnce(pending.promise);
  const client = { GET: read } as unknown as DataGroundClient;
  const render = (active: boolean) =>
    root.render(
      <WorkspaceRetentionWorkflow
        active={active}
        client={client}
        reference={reference}
        revision={1}
      />,
    );
  await act(async () => render(true));
  expect(host.textContent).toContain("Changes recorded");
  await act(async () => render(false));
  expect(host.textContent).toBe("");
  await act(async () => render(true));
  expect(host.textContent).toContain("Checking workspace retention");
  expect(host.textContent).not.toContain("Changes recorded");
  await act(async () => pending.resolve(recorded()));
  expect(read).toHaveBeenCalledTimes(2);
});
