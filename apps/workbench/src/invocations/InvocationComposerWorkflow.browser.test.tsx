import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { DataGroundClient } from "../contracts/client";
import { InvocationComposerWorkflow } from "./InvocationComposerWorkflow";

const target = {
  isolationDomainId: "iso_00000000000000000001",
  serviceId: "svc_00000000000000000001",
};
const inputSchema = {
  type: "object",
  additionalProperties: false,
  required: ["prompt"],
  properties: { prompt: { type: "string", minLength: 1, maxLength: 1024 } },
};
const metadata = {
  id: "inv_00000000000000000001",
  isolationDomainId: target.isolationDomainId,
  createdAt: "2026-10-29T10:00:00Z",
  updatedAt: "2026-10-29T10:00:00Z",
  createdBy: "review",
  generation: 1,
  version: 1,
};
const invocation = {
  metadata,
  input: { prompt: "Fictional receipt input" },
  alias: "stable",
  serviceId: target.serviceId,
  revisionId: "rev_00000000000000000001",
  operationId: "op_00000000000000000001",
  correlationId: "cor_00000000000000000001",
  state: "accepted",
  artifactIds: [],
};
const operation = {
  metadata: { ...metadata, id: invocation.operationId },
  correlationId: invocation.correlationId,
  kind: "invocation-execution",
  command: "invoke",
  desiredState: "succeeded",
  observedState: "accepted",
  stateMachineVersion: 2,
  attempt: 0,
};
const accepted = () => ({ data: invocation, response: new Response(null, { status: 202 }) });
const readOperation = async () => ({
  data: operation,
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
const button = (label: string) =>
  [...host.querySelectorAll<HTMLButtonElement>("button")].find(
    (element) => element.textContent === label,
  );
async function click(label: string) {
  await expect.poll(() => button(label)).toBeDefined();
  await act(async () => userEvent.click(button(label) as HTMLButtonElement));
}
async function render(client: DataGroundClient, onInvocationCreated = vi.fn()) {
  await act(async () =>
    root.render(
      <InvocationComposerWorkflow
        compact
        canInvoke
        client={client}
        target={target}
        inputSchema={inputSchema}
        onInvocationCreated={onInvocationCreated}
      />,
    ),
  );
}
async function fillPrompt(value: string) {
  const input = host.querySelector<HTMLTextAreaElement>('textarea[name="prompt"]');
  expect(input).not.toBeNull();
  await act(async () => userEvent.fill(input as HTMLTextAreaElement, value));
}

it("starts a fresh run only after acceptance, clears the previous input, and creates a new request key", async () => {
  const send = vi.fn(async (_path: string, _options: unknown) => accepted());
  const created = vi.fn();
  await render({ POST: send, GET: readOperation } as unknown as DataGroundClient, created);
  await fillPrompt("first private prompt");
  await click("Start invocation");
  await click("Start another run");
  expect(host.querySelector<HTMLTextAreaElement>('textarea[name="prompt"]')?.value).toBe("");
  expect(host.textContent).not.toContain("first private prompt");
  await expect.poll(() => document.activeElement).toBe(host.querySelector('input[name="alias"]'));
  await fillPrompt("second prompt");
  await click("Start invocation");
  await expect.poll(() => created.mock.calls.length).toBe(2);
  expect(send.mock.calls[0]?.[1]).not.toEqual(send.mock.calls[1]?.[1]);
  const calls = send.mock.calls.map(
    ([, options]) =>
      options as {
        body: { input: { prompt: string } };
        params: { header: { "Idempotency-Key": string } };
      },
  );
  expect(calls.map((call) => call.body.input.prompt)).toEqual([
    "first private prompt",
    "second prompt",
  ]);
  expect(calls[0]?.params.header["Idempotency-Key"]).not.toBe(
    calls[1]?.params.header["Idempotency-Key"],
  );
});

it.each(["lost acknowledgement", "malformed server response"])(
  "retains the exact request after %s until its acceptance is confirmed",
  async (failure) => {
    const send = vi.fn(async (_path: string, _options: unknown) => {
      if (send.mock.calls.length === 1) {
        if (failure === "lost acknowledgement") throw new Error("Lost acknowledgement");
        return { response: new Response(null, { status: 503 }) };
      }
      return accepted();
    });
    await render({ POST: send, GET: readOperation } as unknown as DataGroundClient);
    await fillPrompt("retained prompt");
    await click("Start invocation");
    expect(button("Start another run")).toBeUndefined();
    await click("Retry invocation");
    await expect.poll(() => button("Start another run")).toBeDefined();
    expect(send.mock.calls[1]).toEqual(send.mock.calls[0]);
  },
);

it("clears input on connection replacement and ignores the old accepted response", async () => {
  let finish!: (value: ReturnType<typeof accepted>) => void;
  const send = vi.fn(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  const created = vi.fn();
  await render({ POST: send, GET: readOperation } as unknown as DataGroundClient, created);
  await fillPrompt("old connection prompt");
  await click("Start invocation");
  expect(button("Start another run")).toBeUndefined();
  await render({ POST: vi.fn(), GET: readOperation } as unknown as DataGroundClient, created);
  expect(host.querySelector<HTMLTextAreaElement>('textarea[name="prompt"]')?.value).toBe("");
  await act(async () => finish(accepted()));
  expect(created).not.toHaveBeenCalled();
  expect(button("Start another run")).toBeUndefined();
});
