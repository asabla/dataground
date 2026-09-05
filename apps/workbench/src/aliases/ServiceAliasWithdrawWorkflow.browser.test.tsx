import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import type { ServiceAliasResource } from "./aliasClient";
import {
  prepareAliasWithdrawal,
  ServiceAliasWithdrawWorkflow,
} from "./ServiceAliasWithdrawWorkflow";

const alias: ServiceAliasResource = {
  name: "stable",
  serviceId: "svc_00000000000000000001",
  revisionId: "rev_00000000000000000001",
  metadata: {
    id: "als_00000000000000000001",
    isolationDomainId: "iso_00000000000000000001",
    createdBy: "operator",
    createdAt: "2026-09-05T12:00:00Z",
    updatedAt: "2026-09-05T12:00:00Z",
    version: 1,
    generation: 1,
  },
};
const withdrawnAt = "2026-09-05T12:01:00Z";
const receipt = {
  ...alias,
  withdrawnAt,
  metadata: { ...alias.metadata, version: 2, generation: 2, updatedAt: withdrawnAt },
};
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

it("requires confirmation and retains the original request across uncertain acknowledgement", async () => {
  const calls: unknown[] = [];
  let reject: ((reason: unknown) => void) | undefined;
  const client = {
    POST: async (_path: string, options: unknown) => {
      calls.push(options);
      if (calls.length === 1)
        return new Promise((_resolve, fail) => {
          reject = fail;
        });
      return { data: receipt, response: new Response(null, { status: 200 }) };
    },
  } as unknown as DataGroundClient;
  const onWithdrawn = vi.fn();
  const onClose = vi.fn();
  await act(async () =>
    root.render(
      <ServiceAliasWithdrawWorkflow
        alias={alias}
        canWithdraw
        client={client}
        onClose={onClose}
        onWithdrawn={onWithdrawn}
      />,
    ),
  );
  expect(calls).toHaveLength(0);
  expect(document.activeElement).toBe(host.querySelector("h2"));
  await act(async () => {
    button("Confirm alias withdrawal").click();
    button("Confirm alias withdrawal").click();
  });
  expect(calls).toHaveLength(1);
  expect(button("Close withdrawal").disabled).toBe(true);
  await act(async () => reject?.(new Error("lost acknowledgement")));
  expect(onWithdrawn).not.toHaveBeenCalled();
  expect(document.activeElement).toBe(host.querySelector('[role="alert"]'));
  await click("Recover withdrawal request");
  expect(calls).toHaveLength(2);
  expect(calls[1]).toEqual(calls[0]);
  expect(onWithdrawn).toHaveBeenCalledOnce();
  expect(document.activeElement).toBe(host.querySelector('[role="status"]'));
  await click("Back to service");
  expect(onClose).toHaveBeenCalledOnce();
});

it("does not submit with observer permissions or claim success after an authoritative denial", async () => {
  const post = vi.fn(async () => ({
    error: {
      error: {
        code: "REQUEST_DENIED",
        message: "Access denied.",
        correlationId: "cor_request",
        retryable: false,
      },
    },
    response: new Response(null, { status: 403 }),
  }));
  const client = { POST: post } as unknown as DataGroundClient;
  const onWithdrawn = vi.fn();
  await act(async () =>
    root.render(
      <ServiceAliasWithdrawWorkflow
        alias={alias}
        canWithdraw={false}
        client={client}
        onClose={() => {}}
        onWithdrawn={onWithdrawn}
      />,
    ),
  );
  expect(host.textContent).toContain("current permissions");
  expect(host.textContent).not.toContain("Confirm alias withdrawal");
  expect(post).not.toHaveBeenCalled();
  await act(async () =>
    root.render(
      <ServiceAliasWithdrawWorkflow
        alias={alias}
        canWithdraw
        client={client}
        onClose={() => {}}
        onWithdrawn={onWithdrawn}
      />,
    ),
  );
  await click("Confirm alias withdrawal");
  expect(onWithdrawn).not.toHaveBeenCalled();
  expect(host.textContent).toContain("Access denied.");
  expect(host.textContent).not.toContain("Recover withdrawal request");
});

it("fences delayed acknowledgements after identity and scope changes", async () => {
  let resolve: ((value: unknown) => void) | undefined;
  const client = {
    POST: () =>
      new Promise((done) => {
        resolve = done;
      }),
  } as unknown as DataGroundClient;
  const onWithdrawn = vi.fn();
  await act(async () =>
    root.render(
      <ServiceAliasWithdrawWorkflow
        alias={alias}
        canWithdraw
        client={client}
        onClose={() => {}}
        onWithdrawn={onWithdrawn}
      />,
    ),
  );
  await click("Confirm alias withdrawal");
  await act(async () =>
    root.render(
      <ServiceAliasWithdrawWorkflow
        alias={{ ...alias, name: "next" }}
        canWithdraw
        client={{} as DataGroundClient}
        onClose={() => {}}
        onWithdrawn={onWithdrawn}
      />,
    ),
  );
  await act(async () =>
    resolve?.({ data: receipt, response: new Response(null, { status: 200 }) }),
  );
  expect(onWithdrawn).not.toHaveBeenCalled();
  expect(host.textContent).not.toContain("Alias withdrawn");
  expect(host.querySelector("h2")?.textContent).toContain("next");
});

it("freezes the confirmed alias version for recovery", () => {
  const source = { ...alias, metadata: { ...alias.metadata } };
  const attempt = prepareAliasWithdrawal(source, () => "00000000-0000-4000-8000-000000000001");
  source.metadata.version = 99;
  source.name = "other";
  expect(attempt.alias.metadata.version).toBe(1);
  expect(attempt.alias.name).toBe("stable");
  expect(attempt.idempotencyKey).toBe("alias-withdrawal:00000000000040008000000000000001");
});
