import { EventTimeline, type TimelineEvent } from "@dataground/patterns";
import { act, StrictMode, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import type { InvocationQuestion, InvocationQuestionReference } from "./client";
import { answeredQuestion, pendingQuestion, questionReference as reference } from "./fixtures";
import { QuestionWorkflow } from "./QuestionWorkflow";

function source() {
  const reads: { resolve: (value: unknown) => void; options: unknown }[] = [];
  const posts: {
    resolve: (value: unknown) => void;
    options: {
      body: unknown;
      params: { path: InvocationQuestionReference; header: Record<string, string> };
    };
  }[] = [];
  const client = {
    GET: (_path: string, options: unknown) =>
      new Promise((resolve) => reads.push({ resolve, options })),
    POST: (_path: string, options: (typeof posts)[number]["options"]) =>
      new Promise((resolve) => posts.push({ resolve, options })),
  } as unknown as DataGroundClient;
  return {
    client,
    reads,
    posts,
    read: async (index: number, question: InvocationQuestion, status = 200) =>
      act(async () =>
        reads[index]?.resolve({
          data: status === 200 ? question : undefined,
          response: new Response(null, { status }),
        }),
      ),
    answer: async (index: number, question: InvocationQuestion, status = 200) =>
      act(async () =>
        posts[index]?.resolve({
          data: status === 200 ? question : undefined,
          response: new Response(null, { status }),
        }),
      ),
  };
}
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
  vi.restoreAllMocks();
});
function button(label: string) {
  const value = Array.from(host.querySelectorAll("button")).find(
    (button) => button.textContent === label,
  );
  expect(value).toBeDefined();
  return value as HTMLButtonElement;
}
async function click(label: string, twice = false) {
  await act(async () => {
    button(label).click();
    if (twice) button(label).click();
  });
}
async function choose(index = 0) {
  await act(async () =>
    host.querySelectorAll<HTMLInputElement>('input[type="radio"]')[index]?.click(),
  );
}

it("requires explicit answers and retries an uncertain submission with the exact immutable payload and key", async () => {
  const transport = source();
  const question = pendingQuestion();
  let keys = 0;
  await act(async () =>
    root.render(
      <QuestionWorkflow
        client={transport.client}
        reference={reference}
        canAnswer
        createIdempotencyKey={() => `question:fixed${++keys}`}
      />,
    ),
  );
  await transport.read(0, question);
  expect(button("Submit answers").disabled).toBe(true);
  await choose(1);
  await click("Submit answers", true);
  expect(transport.posts.length).toBe(1);
  await transport.answer(0, question, 503);
  expect(button("Retry same answers").disabled).toBe(true);
  expect(
    Array.from(host.querySelectorAll<HTMLInputElement>("input")).every((input) => input.disabled),
  ).toBe(true);
  await click("Refresh question", true);
  expect(transport.reads.length).toBe(2);
  await transport.read(1, question);
  await click("Retry same answers");
  expect(keys).toBe(1);
  expect(transport.posts[1]?.options).toEqual(transport.posts[0]?.options);
  await transport.answer(1, answeredQuestion(question));
  expect(host.textContent).toContain("Answers recorded");
  expect(host.textContent).toContain("Delivery to the runtime has not been confirmed");
  expect(host.querySelectorAll("input, textarea").length).toBe(0);
});
it("clears drafts and discards late reads and receipts across identity, scope and authority changes", async () => {
  const first = source();
  const second = source();
  const question = pendingQuestion();
  await act(async () =>
    root.render(<QuestionWorkflow client={first.client} reference={reference} canAnswer />),
  );
  await first.read(0, question);
  await choose();
  await click("Submit answers");
  await act(async () =>
    root.render(<QuestionWorkflow client={second.client} reference={reference} canAnswer />),
  );
  expect(host.textContent).not.toContain(question.questions[0]?.prompt);
  await first.answer(0, answeredQuestion(question));
  expect(host.textContent).not.toContain("Answers recorded");
  await second.read(0, question);
  expect(button("Submit answers").disabled).toBe(true);
  await choose();
  await act(async () =>
    root.render(
      <QuestionWorkflow client={second.client} reference={reference} canAnswer={false} />,
    ),
  );
  expect(host.querySelectorAll("input").length).toBe(0);
  await second.read(1, question);
  expect(host.textContent).toContain("cannot submit answers");
  const next = { ...reference, invocationId: "inv_00000000000000000002" };
  await click("Refresh question");
  await act(async () =>
    root.render(<QuestionWorkflow client={second.client} reference={next} canAnswer />),
  );
  await second.read(2, question);
  expect(host.textContent).not.toContain(question.questions[0]?.prompt);
  await second.read(3, { ...question, invocationId: next.invocationId });
  expect(button("Submit answers").disabled).toBe(true);
});
it("latches expiry, rejects a last-moment click and never re-enables after the local clock moves back", async () => {
  const transport = source();
  const now = Date.now();
  const question = pendingQuestion(now);
  const clock = vi.spyOn(Date, "now").mockReturnValue(now);
  await act(async () =>
    root.render(<QuestionWorkflow client={transport.client} reference={reference} canAnswer />),
  );
  await transport.read(0, question);
  await choose();
  clock.mockReturnValue(Date.parse(question.expiresAt));
  await click("Submit answers");
  expect(transport.posts.length).toBe(0);
  await act(async () => new Promise((resolve) => setTimeout(resolve, 300)));
  expect(host.textContent).toContain("Question expired");
  clock.mockReturnValue(now);
  await click("Refresh question");
  await transport.read(1, question);
  expect(host.textContent).toContain("Question expired");
  expect(host.querySelectorAll("input").length).toBe(0);
});
it("recovers a committed answer by read, and fails closed for a substituted request", async () => {
  const transport = source();
  const question = pendingQuestion();
  await act(async () =>
    root.render(<QuestionWorkflow client={transport.client} reference={reference} canAnswer />),
  );
  await transport.read(0, question);
  await choose();
  await click("Submit answers");
  await transport.answer(0, question, 409);
  await click("Refresh question");
  await transport.read(1, answeredQuestion(question));
  expect(host.textContent).toContain("Answers recorded");
  expect(host.textContent).not.toContain("Retry same answers");
  await click("Refresh question");
  await transport.read(2, {
    ...answeredQuestion(question),
    questions: question.questions.map((prompt) => ({ ...prompt, prompt: "substituted content" })),
  });
  expect(host.textContent).not.toContain("substituted content");
  expect(host.textContent).toContain("changed unexpectedly");
});
it("does not repeat submission when secure key creation fails and safely handles StrictMode and unmount", async () => {
  const transport = source();
  const question = pendingQuestion();
  await act(async () =>
    root.render(
      <StrictMode>
        <QuestionWorkflow
          client={transport.client}
          reference={reference}
          canAnswer
          createIdempotencyKey={() => {
            throw new Error("no entropy");
          }}
        />
      </StrictMode>,
    ),
  );
  expect(transport.reads.length).toBe(2);
  await transport.read(0, question);
  expect(host.textContent).toContain("Loading question");
  await transport.read(1, question);
  await choose();
  await click("Submit answers");
  expect(transport.posts.length).toBe(0);
  expect(host.textContent).toContain("secure request identifier");
  await click("Refresh question");
  await act(async () => root.render(null));
  await transport.read(2, question);
  expect(host.textContent).toBe("");
});

it("answers a complete mixed bundle opened from its scoped timeline reference", async () => {
  const transport = source();
  const question = pendingQuestion();
  question.questions.push(
    {
      ...question.questions[0],
      id: "item_2",
      title: "Sections",
      prompt: "Which sections?",
      multiple: true,
      allowFreeText: false,
    },
    {
      id: "item_3",
      title: "Summary",
      prompt: "Describe the report.",
      multiple: false,
      allowFreeText: true,
    },
  );
  const event: TimelineEvent = {
    ...reference,
    actorId: "runtime",
    correlationId: "cor_00000000000000000001",
    id: "evt_00000000000000000001",
    occurredAt: question.createdAt,
    recordedAt: question.createdAt,
    payload: { questionId: question.id, questionVersion: 1, expiresAt: question.expiresAt },
    revisionId: question.revisionId,
    serviceId: question.serviceId,
    schemaVersion: "dataground.event/v1",
    sequence: 1,
    source: "runtime",
    type: "interaction.question.requested",
  };
  function Inspection() {
    const [selected, setSelected] = useState<InvocationQuestionReference>();
    return (
      <>
        <EventTimeline
          connectionState="current"
          events={[event]}
          reference={reference}
          onInspectQuestion={setSelected}
        />
        {selected && <QuestionWorkflow client={transport.client} reference={selected} canAnswer />}
      </>
    );
  }
  await act(async () => root.render(<Inspection />));
  await click("View questions");
  await transport.read(0, question);
  await choose();
  expect(button("Submit answers").disabled).toBe(true);
  await act(async () => {
    for (const input of host.querySelectorAll<HTMLInputElement>(
      'fieldset:nth-of-type(2) input[type="checkbox"]',
    ))
      input.click();
  });
  const textarea = host.querySelector("textarea");
  expect(textarea).not.toBeNull();
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set;
  await act(async () => {
    setter?.call(textarea, "😀".repeat(1025));
    textarea?.dispatchEvent(new Event("input", { bubbles: true }));
  });
  expect(button("Submit answers").disabled).toBe(true);
  await act(async () => {
    setter?.call(textarea, "Local performance report");
    textarea?.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click("Submit answers");
  expect(transport.posts[0]?.options.params.path).toEqual(reference);
  expect(transport.posts[0]?.options.body).toEqual({
    expectedVersion: 1,
    answers: [
      { questionId: "item_1", optionIds: ["option_1"] },
      { questionId: "item_2", optionIds: ["option_1", "option_2"] },
      { questionId: "item_3", text: "Local performance report" },
    ],
  });
  await transport.answer(0, answeredQuestion(question));
  expect(host.textContent).toContain("Answers recorded");
  expect(host.textContent).not.toContain("Local performance report");
});

it("clears private input and question content when authoritative read access is withdrawn", async () => {
  const transport = source();
  const question = pendingQuestion();
  await act(async () =>
    root.render(<QuestionWorkflow client={transport.client} reference={reference} canAnswer />),
  );
  await transport.read(0, question);
  await choose();
  await click("Submit answers");
  await transport.answer(0, question, 503);
  await click("Refresh question");
  await transport.read(1, question, 403);
  expect(host.textContent).toContain("Question unavailable");
  expect(host.textContent).not.toContain(question.questions[0]?.prompt);
  expect(host.querySelectorAll("input, textarea").length).toBe(0);
  await click("Retry question read");
  await transport.read(2, question);
  expect(button("Submit answers").disabled).toBe(true);
  expect(host.textContent).not.toContain("Retry same answers");
});
