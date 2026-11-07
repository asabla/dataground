import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { WorkspaceRetentionWorkflow } from "./WorkspaceRetentionWorkflow";

const reference = {
  isolationDomainId: "iso_00000000000000000001",
  invocationId: "inv_00000000000000000001",
};
const metadata = {
  schemaVersion: "dataground.invocation-workspace-retention/v1",
  ...reference,
  state: "export_pending",
  recovery: "review_required",
  recoveryAccess: "allowed",
};
const receipt = {
  schemaVersion: "dataground.workspace-retention-recovery-receipt/v1",
  ...reference,
  state: "requested",
  invocationVersion: 5,
  deadlineAt: "2026-11-08T01:00:00Z",
  correlationId: "cor_00000000000000000001",
};
function response(data: unknown, status = 200) {
  return { data, response: new Response(null, { status }) };
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

it("requires a current permission and a separate confirmation", async () => {
  const post = vi.fn(async () => response(receipt, 202));
  let access = "denied";
  const client = {
    GET: async () => response({ ...metadata, recoveryAccess: access }),
    POST: post,
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <WorkspaceRetentionWorkflow active client={client} reference={reference} revision={4} />,
    ),
  );
  expect(button("Request retention recovery")).toBeUndefined();
  access = "allowed";
  await act(async () => button("Refresh retention status")?.click());
  await act(async () => {
    button("Request retention recovery")?.focus();
    button("Request retention recovery")?.click();
  });
  expect(host.querySelector('[role="dialog"]')).not.toBeNull();
  expect(post).not.toHaveBeenCalled();
  await act(async () => button("Keep unchanged")?.click());
  expect(post).not.toHaveBeenCalled();
  await expect.poll(() => document.activeElement).toBe(button("Request retention recovery"));
  await act(async () => button("Request retention recovery")?.click());
  await act(async () => button("Request recovery")?.click());
  expect(post).toHaveBeenCalledTimes(1);
  expect(host.textContent).toContain("Recovery request accepted");
  expect(host.querySelector('[role="dialog"]')).toBeNull();
});

it("retains an uncertain exact request across navigation and a changed observed version", async () => {
  const pending = deferred<ReturnType<typeof response>>();
  const accepted = vi.fn();
  let recovery = "review_required";
  const post = vi
    .fn()
    .mockReturnValueOnce(pending.promise)
    .mockResolvedValueOnce(response(undefined, 403))
    .mockResolvedValueOnce(response(receipt, 202));
  const client = {
    GET: async () => response({ ...metadata, recovery }),
    POST: post,
  } as unknown as DataGroundClient;
  const render = (active: boolean, revision: number, confirmed = true) =>
    root.render(
      <WorkspaceRetentionWorkflow
        active={active}
        confirmed={confirmed}
        client={client}
        reference={reference}
        revision={revision}
        onRecoveryAccepted={accepted}
      />,
    );
  await act(async () => render(true, 4));
  await act(async () => button("Request retention recovery")?.click());
  await act(async () => {
    button("Request recovery")?.click();
    button("Request recovery")?.click();
  });
  expect(post).toHaveBeenCalledTimes(1);
  await act(async () => render(false, 4));
  await act(async () => pending.resolve(response(undefined, 503)));
  recovery = "requested";
  await act(async () => render(true, 5));
  expect(host.textContent).toContain("outcome is unknown");
  await act(async () => render(true, 5, false));
  expect(host.textContent).toContain("outcome is unknown");
  expect(button("Retry exact recovery request")?.disabled).toBe(true);
  await act(async () => render(true, 5, true));

  expect(button("Request retention recovery")).toBeUndefined();
  await act(async () => button("Retry exact recovery request")?.click());
  expect(post).toHaveBeenCalledTimes(2);
  expect(post.mock.calls[0]).toEqual(post.mock.calls[1]);
  expect(post.mock.calls[1]?.[1].body.expectedVersion).toBe(4);
  expect(accepted).not.toHaveBeenCalled();
  expect(host.textContent).toContain("earlier recovery request outcome is still unknown");
  expect(button("Request retention recovery")).toBeUndefined();
  await act(async () => button("Retry exact recovery request")?.click());
  expect(post).toHaveBeenCalledTimes(3);
  expect(post.mock.calls[0]).toEqual(post.mock.calls[2]);
  expect(accepted).toHaveBeenCalledTimes(1);
  expect(host.textContent).toContain("Recovery request accepted");
});

it("discards a pending command after identity replacement", async () => {
  const pending = deferred<ReturnType<typeof response>>();
  const accepted = vi.fn();
  const oldClient = {
    GET: async () => response(metadata),
    POST: () => pending.promise,
  } as unknown as DataGroundClient;
  const newClient = {
    GET: async () => response({ ...metadata, recoveryAccess: "denied" }),
  } as unknown as DataGroundClient;
  const render = (client: DataGroundClient) =>
    root.render(
      <WorkspaceRetentionWorkflow
        active
        client={client}
        reference={reference}
        revision={4}
        onRecoveryAccepted={accepted}
      />,
    );
  await act(async () => render(oldClient));
  await act(async () => button("Request retention recovery")?.click());
  await act(async () => button("Request recovery")?.click());
  await act(async () => render(newClient));
  await act(async () => pending.resolve(response(receipt, 202)));
  expect(accepted).not.toHaveBeenCalled();
  expect(host.textContent).not.toContain("Recovery request accepted");
  expect(button("Retry exact recovery request")).toBeUndefined();
});
