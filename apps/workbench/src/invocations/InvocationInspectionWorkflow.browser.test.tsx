import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { DataGroundClient } from "../contracts/client";
import type { InvocationQuestionReference } from "../questions";
import { answeredQuestion, pendingQuestion, questionReference } from "../questions/fixtures";
import { InvocationInspectionWorkflow } from "./InvocationInspectionWorkflow";

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  vi.spyOn(console, "error");
});
afterEach(async () => {
  try {
    await act(async () => root.unmount());
    expect(console.error).not.toHaveBeenCalled();
  } finally {
    host.remove();
    vi.restoreAllMocks();
  }
});

function fixture(withActivity = false) {
  const event = {
    invocationId: questionReference.invocationId,
    isolationDomainId: questionReference.isolationDomainId,
    actorId: "runtime",
    correlationId: "cor_review",
    id: "evt_00000000000000000001",
    occurredAt: "2026-10-29T00:00:00Z",
    recordedAt: "2026-10-29T00:00:00Z",
    payload: { name: "review.lookup" },
    revisionId: "rev_00000000000000000001",
    schemaVersion: "dataground.event/v1",
    sequence: 1,
    serviceId: "svc_00000000000000000001",
    type: "activity.tool.started",
    source: "runtime",
  };
  let listed = true;
  let question = pendingQuestion();
  const get = vi.fn(async (path: string) => {
    if (path.endsWith("/events"))
      return {
        data: withActivity
          ? `id: 1\nevent: activity.tool.started\ndata: ${JSON.stringify(event)}\n\n`
          : "",
        response: new Response(null, {
          status: 200,
          headers: { "X-DataGround-Has-More": "false" },
        }),
      };
    if (path.endsWith("/questions/{questionId}"))
      return { data: question, response: new Response(null, { status: 200 }) };
    if (path.endsWith("/questions"))
      return {
        data: {
          items: listed
            ? [
                {
                  schemaVersion: "dataground.invocation-question-summary/v1",
                  isolationDomainId: questionReference.isolationDomainId,
                  invocationId: questionReference.invocationId,
                  id: question.id,
                  state: question.state,
                  version: question.version,
                  createdAt: question.createdAt,
                  updatedAt: question.updatedAt,
                  expiresAt: question.expiresAt,
                },
              ]
            : [],
        },
        response: new Response(null, { status: 200 }),
      };
    if (path.endsWith("/approvals"))
      return { data: { items: [] }, response: new Response(null, { status: 200 }) };
    return { response: new Response(null, { status: 403 }) };
  });
  const post = vi.fn(async () => {
    question = answeredQuestion(question);
    return { data: question, response: new Response(null, { status: 200 }) };
  });
  return {
    client: { GET: get, POST: post } as unknown as DataGroundClient,
    get,
    post,
    removeQuestion: () => {
      listed = false;
    },
  };
}

function Journey({
  client,
  presentation = "classic",
}: {
  client: DataGroundClient;
  presentation?: "classic" | "workbench";
}) {
  const [question, setQuestion] = useState<InvocationQuestionReference>();
  return (
    <InvocationInspectionWorkflow
      presentation={presentation}
      client={client}
      reference={questionReference}
      canCancelInvocation={false}
      canResolveApproval={false}
      canAnswerQuestion
      selectedQuestion={question}
      onInspectQuestion={setQuestion}
      onCloseQuestion={() => setQuestion(undefined)}
      onInspectApproval={() => undefined}
      onCloseApproval={() => undefined}
      onInspectArtifact={() => undefined}
      onCloseArtifact={() => undefined}
    />
  );
}
async function render(client: DataGroundClient) {
  await act(async () => root.render(<Journey client={client} />));
}
function button(label: string) {
  const result = [...host.querySelectorAll("button")].find(
    (value) => (value.getAttribute("aria-label") ?? value.textContent) === label,
  );
  expect(result, label).toBeDefined();
  return result as HTMLButtonElement;
}
async function click(label: string) {
  const target = button(label);
  await act(async () => userEvent.click(target));
  return target;
}

it("refreshes the question list after acceptance and keeps the selected receipt in context", async () => {
  const source = fixture();
  await act(async () => root.render(<Journey client={source.client} presentation="workbench" />));
  const requests = [...host.querySelectorAll<HTMLElement>('[role="tab"]')].find(
    (tab) => tab.textContent === "Requests",
  );
  await act(async () => userEvent.click(requests as HTMLElement));
  await click(`Open question ${questionReference.questionId}`);
  await act(async () =>
    userEvent.click(host.querySelector('input[type="radio"]') as HTMLInputElement),
  );
  await click("Submit answers");
  await expect
    .poll(
      () => host.querySelector('[role="grid"][aria-label="Loaded question requests"]')?.textContent,
    )
    .toContain("answered");
  await expect
    .poll(() => document.activeElement)
    .toBe(host.querySelector(".dg-question-request__heading"));
  expect(host.querySelector('[aria-label="Selected interaction item"]')?.textContent).toContain(
    "Answers recorded",
  );
  expect(source.post).toHaveBeenCalledOnce();
});

it("moves focus between sections without remounting workflows or sending commands", async () => {
  const source = fixture();
  await render(source.client);
  const initialReads = source.get.mock.calls.length;
  for (const [label, target] of [
    ["Activity", "Interaction activity"],
    ["Questions and approvals", "Interaction questions and approvals"],
    ["Audit", "Interaction audit"],
    ["Overview and result", "Interaction overview and result"],
  ]) {
    await click(label as string);
    expect(document.activeElement).toBe(host.querySelector(`[aria-label="${target}"]`));
  }
  expect(source.get.mock.calls.length).toBe(initialReads);
  expect(source.post).not.toHaveBeenCalled();
});

it("opens the selected request before the overview and retains edits while navigating", async () => {
  const source = fixture();
  await render(source.client);
  const opener = await click(`Open question ${questionReference.questionId}`);
  const selection = host.querySelector('[aria-label="Selected interaction item"]');
  const overview = host.querySelector('[aria-label="Interaction overview and result"]');
  expect(document.activeElement).toBe(selection);
  expect(
    (selection?.compareDocumentPosition(overview as Node) ?? 0) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  const freeText = host.querySelector('input[type="checkbox"]') as HTMLInputElement;
  expect(freeText).not.toBeNull();
  await act(async () => freeText.click());
  const answer = host.querySelector("textarea") as HTMLTextAreaElement;
  expect(answer).not.toBeNull();
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set?.call(
      answer,
      "Keep this unfinished answer",
    );
    answer.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const reads = source.get.mock.calls.length;
  await click("Activity");
  await click("Selected item");
  expect(host.querySelector("textarea")).toBe(answer);
  expect(answer.value).toBe("Keep this unfinished answer");
  expect(source.get.mock.calls.length).toBe(reads);
  await click("Questions and approvals");
  await click(`Open question ${questionReference.questionId}`);
  expect(document.activeElement).toBe(selection);
  expect(host.querySelector("textarea")).toBe(answer);
  expect(answer.value).toBe("Keep this unfinished answer");
  expect(source.get.mock.calls.length).toBe(reads);
  await click("Close questions");
  expect(document.activeElement).toBe(opener);
  expect(host.querySelector('[aria-label="Selected interaction item"]')).toBeNull();
  expect(source.post).not.toHaveBeenCalled();
});

it("returns focus to section navigation if the request opener disappears", async () => {
  const source = fixture();
  await render(source.client);
  await click(`Open question ${questionReference.questionId}`);
  source.removeQuestion();
  await click("Refresh questions");
  await click("Close questions");
  expect(document.activeElement).toBe(host.querySelector('[aria-label="Interaction sections"]'));
});

it("does not restore a focus target retained by another connection", async () => {
  const source = fixture();
  await render(source.client);
  await click(`Open question ${questionReference.questionId}`);
  const replacement = fixture();
  await render(replacement.client);
  await click("Close questions");
  expect(document.activeElement).toBe(host.querySelector('[aria-label="Interaction sections"]'));
});

it("returns focus to navigation when the retained opener cannot receive focus", async () => {
  const source = fixture();
  await render(source.client);
  const opener = await click(`Open question ${questionReference.questionId}`);
  opener.disabled = true;
  await click("Close questions");
  expect(document.activeElement).toBe(host.querySelector('[aria-label="Interaction sections"]'));
});

async function selectTab(name: string) {
  const tab = [...host.querySelectorAll<HTMLElement>('[role="tab"]')].find(
    (element) => element.textContent === name,
  );
  expect(tab).toBeDefined();
  await act(async () => tab?.click());
}
it("keeps selected request edits beside run tabs and falls back from an inert opener", async () => {
  const source = fixture(true);
  await act(async () => root.render(<Journey client={source.client} presentation="workbench" />));
  expect(source.get.mock.calls.some(([path]) => path.endsWith("/questions"))).toBe(false);
  await selectTab("Requests");
  await click(`Open question ${questionReference.questionId}`);
  const selection = host.querySelector('[aria-label="Selected interaction item"]');
  expect(document.activeElement).toBe(selection);
  const freeText = host.querySelector('input[type="checkbox"]') as HTMLInputElement;
  await act(async () => freeText.click());
  const answer = host.querySelector("textarea") as HTMLTextAreaElement;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set?.call(
      answer,
      "Retained side panel answer",
    );
    answer.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await selectTab("Activity");
  expect(host.querySelector("textarea")).toBe(answer);
  expect(answer.value).toBe("Retained side panel answer");
  await click("Inspect event 1");
  expect(host.querySelector('[aria-label="Activity inspection"]')?.textContent).toContain(
    "review.lookup",
  );
  expect(answer.closest("[hidden]")).not.toBeNull();
  await click("Close activity");
  expect(host.querySelector("textarea")).toBe(answer);
  expect(answer.value).toBe("Retained side panel answer");
  expect(selection?.closest("[inert]")).toBeNull();
  expect(host.querySelector(".dg-run-status")?.closest("[inert]")).toBeNull();
  await click("Close questions");
  expect(document.activeElement).toBe(host.querySelector('[aria-label="Run views"]'));
  expect(source.post).not.toHaveBeenCalled();
});

it("clears selected activity when the connection changes", async () => {
  const source = fixture(true);
  await act(async () => root.render(<Journey client={source.client} presentation="workbench" />));
  await click("Inspect event 1");
  expect(host.querySelector('[aria-label="Activity inspection"]')).not.toBeNull();
  const replacement = fixture();
  await act(async () =>
    root.render(<Journey client={replacement.client} presentation="workbench" />),
  );
  expect(host.querySelector('[aria-label="Activity inspection"]')).toBeNull();
});
