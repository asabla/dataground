import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { DataGroundClient } from "../contracts/client";
import { pendingQuestion } from "../questions/fixtures";
import { InvocationHistoryWorkflow } from "./InvocationHistoryWorkflow";

let host: HTMLDivElement;
let root: Root;
const first = "inv_00000000000000000001";
const second = "inv_00000000000000000002";
const target = {
  isolationDomainId: "iso_00000000000000000001",
  serviceId: "svc_00000000000000000001",
};
const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) });
const denied = () => ({
  error: { error: { code: "FORBIDDEN", message: "Access denied.", retryable: false } },
  response: new Response(null, { status: 403 }),
});
function fixture() {
  const question = pendingQuestion();
  let denyHistory = false;
  const get = vi.fn(
    async (path: string, options: { params: { path: { invocationId?: string } } }) => {
      const invocationId = options.params.path.invocationId;
      if (path.endsWith("/agent-services/{serviceId}/invocations"))
        return denyHistory
          ? denied()
          : ok({
              items: [first, second].map((id) => ({
                metadata: {
                  id,
                  isolationDomainId: target.isolationDomainId,
                  createdAt: question.createdAt,
                  updatedAt: question.updatedAt,
                  createdBy: "operator",
                  generation: 1,
                  version: 1,
                },
                serviceId: target.serviceId,
                revisionId: question.revisionId,
                operationId: "op_00000000000000000001",
                correlationId: "cor_review",
                alias: "stable",
                state: "waiting",
              })),
            });
      if (path.endsWith("/questions"))
        return ok({
          items: [
            {
              schemaVersion: "dataground.invocation-question-summary/v1",
              id: question.id,
              isolationDomainId: target.isolationDomainId,
              invocationId,
              state: question.state,
              version: question.version,
              createdAt: question.createdAt,
              updatedAt: question.updatedAt,
              expiresAt: question.expiresAt,
            },
          ],
        });
      if (path.endsWith("/questions/{questionId}")) return ok({ ...question, invocationId });
      if (path.endsWith("/approvals")) return ok({ items: [] });
      return denied();
    },
  );
  const post = vi.fn(async () => {
    throw new Error("Lost acknowledgement");
  });
  return {
    client: { GET: get, POST: post } as unknown as DataGroundClient,
    get,
    post,
    deny: () => {
      denyHistory = true;
    },
  };
}
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
async function render(client: DataGroundClient, scope = target) {
  await act(async () =>
    root.render(
      <InvocationHistoryWorkflow presentation="workbench" client={client} target={scope} />,
    ),
  );
}
function visible<T extends Element>(selector: string): T | undefined {
  return [...host.querySelectorAll<T>(selector)].find(
    (element) => !element.closest("[hidden], [inert]"),
  );
}
async function click(label: string) {
  const button = [...host.querySelectorAll<HTMLButtonElement>('button, [role="tab"]')].find(
    (element) =>
      !element.closest("[hidden], [inert]") &&
      (element.getAttribute("aria-label") ?? element.textContent) === label,
  );
  expect(button, label).toBeDefined();
  expect(button?.disabled, label).not.toBe(true);
  await act(async () => userEvent.click(button as HTMLButtonElement));
  return button;
}
async function edit(value: string) {
  const input = visible<HTMLTextAreaElement>("textarea");
  expect(input).toBeDefined();
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set?.call(
      input,
      value,
    );
    input?.dispatchEvent(new Event("input", { bubbles: true }));
  });
  return input;
}
async function openQuestion(id: string) {
  await click(`Open invocation ${id}`);
  await click("Requests");
  await click("Open question qst_00000000000000000001");
  await act(async () => visible<HTMLInputElement>('input[type="checkbox"]')?.click());
}

it("keeps independent answers and exact uncertain retries across history refresh and another run", async () => {
  const source = fixture();
  await render(source.client);
  await openQuestion(first);
  const firstAnswer = await edit("First run answer");
  await click("Submit answers");
  expect(source.post).toHaveBeenCalledTimes(1);
  await click("Back to invocation history");
  await expect
    .poll(() => document.activeElement?.getAttribute("aria-label"))
    .toBe(`Open invocation ${first}`);
  expect(firstAnswer?.closest("[hidden]")).not.toBeNull();
  await click("Refresh invocation history");
  await openQuestion(second);
  const secondAnswer = await edit("Second run draft");
  await click("Back to invocation history");
  await click(`Open invocation ${first}`);
  expect(visible("textarea")).toBe(firstAnswer);
  expect(firstAnswer?.value).toBe("First run answer");
  await click("Refresh question");
  await click("Retry same answers");
  expect(source.post).toHaveBeenCalledTimes(2);
  expect(source.post.mock.calls[1]).toEqual(source.post.mock.calls[0]);
  await click("Back to invocation history");
  await click(`Open invocation ${second}`);
  expect(visible("textarea")).toBe(secondAnswer);
  expect(secondAnswer?.value).toBe("Second run draft");
});

it.each(["identity", "scope", "denial"])(
  "discards hidden request contents on %s change",
  async (change) => {
    const source = fixture();
    await render(source.client);
    await openQuestion(first);
    await edit("Sensitive unfinished answer");
    await click("Back to invocation history");
    if (change === "identity") await render(fixture().client);
    if (change === "scope")
      await render(source.client, { ...target, isolationDomainId: "iso_00000000000000000002" });
    if (change === "denial") {
      source.deny();
      await click("Refresh invocation history");
    }
    expect(host.querySelector("textarea")).toBeNull();
    expect(host.textContent).not.toContain("Which environment should the report describe?");
    expect(source.post).not.toHaveBeenCalled();
  },
);
