import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { DataGroundClient } from "../contracts/client";
import { InvocationHistoryWorkflow } from "../invocations/InvocationHistoryWorkflow";
import type { InvocationQuestionReference } from "./client";
import { pendingQuestion } from "./fixtures";
import { QuestionDiscoveryWorkflow } from "./QuestionDiscoveryWorkflow";
import { QuestionWorkflow } from "./QuestionWorkflow";

const reference = {
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};
const firstID = "qst_00000000000000000001",
  secondID = "qst_00000000000000000002";
function question(id = firstID) {
  const now = new Date().toISOString();
  return {
    schemaVersion: "dataground.invocation-question-summary/v1",
    id,
    ...reference,
    state: "pending",
    version: 1,
    createdAt: now,
    updatedAt: now,
    expiresAt: new Date(Date.now() + 60_000).toISOString(),
  };
}
function response(data: unknown) {
  return { data, response: new Response(null, { status: 200 }) };
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
  vi.restoreAllMocks();
});
function button(text: string) {
  return Array.from(host.querySelectorAll("button")).find((value) => value.textContent === text);
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((complete) => {
    resolve = complete;
  });
  return { promise, resolve };
}

it("discovers paged questions and reads the selected request without submitting an answer", async () => {
  const more = deferred<ReturnType<typeof response>>();
  const reads = vi.fn(
    async (
      path: string,
      options: { params: { query?: { cursor?: string }; path: { questionId?: string } } },
    ) => {
      if (path.endsWith("/{questionId}"))
        return response({ ...pendingQuestion(), id: options.params.path.questionId });
      if (options.params.query?.cursor) return more.promise;
      return response({ items: [question()], nextCursor: "cursor-1" });
    },
  );
  const post = vi.fn();
  const client = { GET: reads, POST: post } as unknown as DataGroundClient;
  function Journey() {
    const [selected, setSelected] = useState<InvocationQuestionReference>();
    return (
      <>
        <QuestionDiscoveryWorkflow
          client={client}
          reference={reference}
          onInspectQuestion={setSelected}
        />
        {selected && <QuestionWorkflow client={client} reference={selected} canAnswer={false} />}
      </>
    );
  }
  await act(async () => root.render(<Journey />));
  const loadMore = button("Load more questions");
  await act(async () => {
    loadMore?.click();
    loadMore?.click();
  });
  expect(reads).toHaveBeenCalledTimes(2);
  expect(button(`Open question ${firstID}`)?.disabled).toBe(true);
  await act(async () => more.resolve(response({ items: [question(secondID)] })));
  const open = button(`Open question ${secondID}`);
  await act(async () => open?.focus());
  expect(document.activeElement).toBe(open);
  await act(async () => userEvent.keyboard("{Enter}"));
  expect(reads).toHaveBeenCalledTimes(3);
  expect(host.textContent).toContain("Which environment should the report describe?");
  expect(post).not.toHaveBeenCalled();
  expect(button("Submit answers")).toBeUndefined();
  await act(async () => button("Refresh questions")?.click());
  expect(button(`Open question ${secondID}`)).toBeUndefined();
});

it("removes prior requests on denial and blocks stale controls", async () => {
  let denied = false;
  const inspected = vi.fn();
  const client = {
    GET: async () =>
      denied
        ? {
            error: {
              error: {
                code: "FORBIDDEN",
                message: "Access denied.",
                retryable: false,
                correlationId: "cor_00000000000000000001",
              },
            },
            response: new Response(null, { status: 403 }),
          }
        : response({ items: [question()], nextCursor: "cursor-1" }),
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <QuestionDiscoveryWorkflow
        client={client}
        reference={reference}
        onInspectQuestion={inspected}
      />,
    ),
  );
  const old = button(`Open question ${firstID}`);
  denied = true;
  await act(async () => button("Load more questions")?.click());
  expect(host.textContent).toContain("not authorized to discover questions");
  expect(button(`Open question ${firstID}`)).toBeUndefined();
  await act(async () => old?.click());
  expect(inspected).not.toHaveBeenCalled();
  denied = false;
  await act(async () => button("Refresh questions")?.click());
  await act(async () => button("Load more questions")?.click());
  expect(host.textContent).toContain("could not interpret");
  expect(button(`Open question ${firstID}`)).toBeUndefined();
});

it("ignores delayed responses after connection or invocation changes", async () => {
  const pending = deferred<ReturnType<typeof response>>();
  const oldClient = { GET: () => pending.promise } as unknown as DataGroundClient;
  const inspected = vi.fn();
  await act(async () =>
    root.render(
      <QuestionDiscoveryWorkflow
        client={oldClient}
        reference={reference}
        onInspectQuestion={inspected}
      />,
    ),
  );
  const client = { GET: async () => response({ items: [] }) } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <QuestionDiscoveryWorkflow
        client={client}
        reference={{ ...reference, invocationId: "inv_00000000000000000002" }}
        onInspectQuestion={inspected}
      />,
    ),
  );
  await act(async () => pending.resolve(response({ items: [question()] })));
  expect(host.textContent).toContain("No retained question requests");
  expect(button(`Open question ${firstID}`)).toBeUndefined();
  expect(inspected).not.toHaveBeenCalled();
});

it("finds retained questions from service invocation history without event replay", async () => {
  const full = pendingQuestion();
  const summary = {
    metadata: {
      id: reference.invocationId,
      isolationDomainId: reference.isolationDomainId,
      createdAt: full.createdAt,
      updatedAt: full.updatedAt,
      createdBy: "operator",
      generation: 1,
      version: 2,
    },
    serviceId: full.serviceId,
    revisionId: full.revisionId,
    operationId: "op_00000000000000000001",
    correlationId: "cor_00000000000000000001",
    alias: "old-route",
    state: "waiting",
  };
  const reads = vi.fn(async (path: string) => {
    if (path.endsWith("/agent-services/{serviceId}/invocations"))
      return response({ items: [summary] });
    if (path.endsWith("/questions")) return response({ items: [question()] });
    if (path.endsWith("/questions/{questionId}")) return response(full);
    if (path.endsWith("/approvals")) return response({ items: [] });
    return {
      error: { error: { code: "FORBIDDEN", message: "Access denied.", retryable: false } },
      response: new Response(null, { status: 403 }),
    };
  });
  const post = vi.fn();
  const client = { GET: reads, POST: post } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <InvocationHistoryWorkflow
        client={client}
        target={{ isolationDomainId: reference.isolationDomainId, serviceId: full.serviceId }}
      />,
    ),
  );
  const inspect = Array.from(host.querySelectorAll("button")).find(
    (value) => value.textContent === `Open invocation ${reference.invocationId}`,
  );
  expect(inspect).toBeDefined();
  await act(async () => inspect?.click());
  expect(host.textContent).not.toContain(full.questions[0]?.prompt);
  await act(async () => button(`Open question ${firstID}`)?.click());
  expect(host.textContent).toContain(full.questions[0]?.prompt);
  expect(post).not.toHaveBeenCalled();
  await act(async () => button("Back to invocation history")?.click());
  expect(host.textContent).not.toContain(full.questions[0]?.prompt);
  await act(async () =>
    root.render(
      <InvocationHistoryWorkflow
        client={client}
        target={{ isolationDomainId: "iso_00000000000000000002", serviceId: full.serviceId }}
      />,
    ),
  );
  expect(button(`Open question ${firstID}`)).toBeUndefined();
});
