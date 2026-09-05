import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { InvocationResultWorkflow } from "./InvocationResultWorkflow";

const reference = {
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
  serviceId: "svc_00000000000000000001",
  revisionId: "rev_00000000000000000001",
};
const invocation = {
  input: { prompt: "private-input" },
  metadata: {
    id: reference.invocationId,
    isolationDomainId: reference.isolationDomainId,
    createdBy: "operator",
    createdAt: "2026-09-05T12:00:00Z",
    updatedAt: "2026-09-05T12:00:00Z",
    version: 1,
    generation: 1,
  },
  serviceId: reference.serviceId,
  revisionId: reference.revisionId,
  alias: "stable",
  state: "succeeded",
  artifactIds: [],
  operationId: "op_00000000000000000001",
  correlationId: "cor_00000000000000000001",
  result: { output: "private-result" },
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
async function click(label: string) {
  const button = Array.from(host.querySelectorAll("button")).find(
    (entry) => entry.textContent === label,
  );
  expect(button).toBeDefined();
  await act(async () => button?.click());
}
function pendingClient() {
  let resolve: ((value: unknown) => void) | undefined;
  let reads = 0;
  const client = {
    GET: () => {
      reads++;
      return new Promise((done) => {
        resolve = done;
      });
    },
  } as unknown as DataGroundClient;
  return {
    client,
    reads: () => reads,
    finish: async () => {
      await act(async () =>
        resolve?.({ data: invocation, response: new Response(null, { status: 200 }) }),
      );
    },
  };
}

it("fetches only on request and discards late results after hiding", async () => {
  const source = pendingClient();
  await act(async () =>
    root.render(<InvocationResultWorkflow client={source.client} reference={reference} />),
  );
  expect(source.reads()).toBe(0);
  await click("Show result");
  expect(source.reads()).toBe(1);
  await click("Hide result");
  await source.finish();
  expect(host.textContent).not.toContain("private-result");
  expect(host.querySelector("pre")).toBeNull();
  await click("Show result");
  await source.finish();
  expect(host.querySelector("pre")?.textContent).toContain("private-result");
  await click("Hide result");
  expect(host.querySelector("pre")).toBeNull();
});

it("clears visible content on identity changes and ignores old in-flight scope reads", async () => {
  const source = pendingClient();
  await act(async () =>
    root.render(<InvocationResultWorkflow client={source.client} reference={reference} />),
  );
  await click("Show result");
  await source.finish();
  expect(host.textContent).toContain("private-result");
  const next = pendingClient();
  await act(async () =>
    root.render(<InvocationResultWorkflow client={next.client} reference={reference} />),
  );
  expect(host.textContent).not.toContain("private-result");
  expect(next.reads()).toBe(0);
  await click("Show result");
  await act(async () =>
    root.render(
      <InvocationResultWorkflow
        client={next.client}
        reference={{ ...reference, invocationId: "inv_00000000000000000002" }}
      />,
    ),
  );
  await next.finish();
  expect(host.textContent).not.toContain("private-result");
  expect(host.querySelector("pre")).toBeNull();
});

it("does not restore content after unmounting during a read", async () => {
  const source = pendingClient();
  await act(async () =>
    root.render(<InvocationResultWorkflow client={source.client} reference={reference} />),
  );
  await click("Show result");
  await act(async () => root.render(null));
  await source.finish();
  expect(host.textContent).toBe("");
});
