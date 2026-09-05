import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { EventTimelineWorkflow } from "./EventTimelineWorkflow";

const reference = {
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};
function page(first: number, count: number, marker = "journal", scope = reference) {
  return Array.from({ length: count }, (_, index) => {
    const sequence = first + index;
    const event = {
      ...scope,
      actorId: "reference-runtime",
      correlationId: "correlation-fixture",
      id: `evt_${sequence.toString(36).padStart(20, "0")}`,
      occurredAt: "2026-09-05T12:00:00Z",
      recordedAt: "2026-09-05T12:00:00Z",
      payload: { text: `${marker}-${sequence}` },
      revisionId: "rev_00000000000000000001",
      schemaVersion: "dataground.event/v1",
      sequence,
      serviceId: "svc_00000000000000000001",
      type: "output.text.delta",
      source: "runtime",
    };
    return `id: ${sequence}\nevent: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`;
  }).join("");
}
function source() {
  const reads: {
    cursor: number;
    scope: typeof reference;
    limit: number;
    resolve: (value: unknown) => void;
  }[] = [];
  const client = {
    GET: (
      _path: string,
      options: {
        params: {
          path: typeof reference;
          header?: Record<string, string>;
          query: { limit: number };
        };
      },
    ) =>
      new Promise((resolve) => {
        reads.push({
          cursor: Number(options.params.header?.["Last-Event-ID"] ?? 0),
          scope: options.params.path,
          limit: options.params.query.limit,
          resolve,
        });
      }),
  } as unknown as DataGroundClient;
  return {
    client,
    reads,
    finish: async (index: number, body: string, hasMore: boolean) => {
      await act(async () =>
        reads[index]?.resolve({
          data: body,
          response: new Response(null, {
            status: 200,
            headers: { "X-DataGround-Has-More": String(hasMore) },
          }),
        }),
      );
    },
    fail: async (index: number) => {
      await act(async () =>
        reads[index]?.resolve({
          error: {
            error: {
              code: "TEMPORARILY_UNAVAILABLE",
              message: "Replay temporarily unavailable.",
              retryable: true,
            },
          },
          response: new Response(null, { status: 503 }),
        }),
      );
    },
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
});
async function click(label: string, twice = false) {
  const button = Array.from(host.querySelectorAll("button")).find(
    (entry) => entry.textContent === label,
  );
  expect(button).toBeDefined();
  await act(async () => {
    button?.click();
    if (twice) button?.click();
  });
}

it("replays more than 500 records in bounded pages, retries the confirmed cursor, and prevents duplicate reads", async () => {
  const transport = source();
  await act(async () =>
    root.render(<EventTimelineWorkflow client={transport.client} reference={reference} />),
  );
  await transport.finish(0, page(1, 200), true);
  expect(host.textContent).toContain("More events available");
  expect(host.textContent).not.toContain("Replay current");
  expect(host.querySelectorAll("ol > li").length).toBe(200);
  const replayButton = host.querySelector<HTMLButtonElement>("button");
  await act(async () => replayButton?.focus());
  await click("Replay more events", true);
  expect(document.activeElement).toBe(replayButton);
  expect(transport.reads.length).toBe(2);
  await transport.fail(1);
  expect(document.activeElement).toBe(replayButton);
  expect(host.textContent).toContain("journal-200");
  await click("Retry replay");
  await transport.finish(2, page(201, 200), true);
  expect(document.activeElement).toBe(replayButton);
  expect(host.textContent).toContain("200 earlier events are outside the bounded display");
  expect(host.textContent).not.toContain("journal-1");
  await click("Replay more events");
  await transport.finish(3, page(401, 105), false);
  expect(host.textContent).toContain("Replay current");
  expect(host.textContent).not.toContain("More events available");
  expect(host.textContent).toContain("305 earlier events are outside the bounded display");
  expect(host.querySelectorAll("ol > li").length).toBe(200);
  expect(host.textContent).toContain("journal-505");
  await click("Replay new events");
  await transport.finish(4, "", false);
  expect(host.textContent).toContain("journal-505");
  expect(transport.reads.map((read) => read.cursor)).toEqual([0, 200, 200, 400, 505]);
  expect(transport.reads.every((read) => read.limit === 200)).toBe(true);
});

it("clears prior identity content and discards old page completions across client and invocation changes", async () => {
  const first = source();
  const second = source();
  await act(async () =>
    root.render(<EventTimelineWorkflow client={first.client} reference={reference} />),
  );
  await first.finish(0, page(1, 1, "old-identity"), true);
  await click("Replay more events");
  await act(async () =>
    root.render(<EventTimelineWorkflow client={second.client} reference={reference} />),
  );
  expect(host.textContent).not.toContain("old-identity");
  await first.finish(1, page(2, 1, "old-identity"), false);
  expect(host.textContent).not.toContain("old-identity");
  await second.finish(0, page(1, 1, "new-identity"), true);
  expect(host.textContent).toContain("new-identity-1");
  await click("Replay more events");
  const next = { ...reference, invocationId: "inv_00000000000000000002" };
  await act(async () =>
    root.render(<EventTimelineWorkflow client={second.client} reference={next} />),
  );
  expect(host.textContent).not.toContain("new-identity");
  await second.finish(1, page(2, 1, "new-identity"), false);
  expect(host.textContent).not.toContain("new-identity");
  await second.finish(2, page(1, 1, "next-invocation", next), false);
  expect(host.textContent).toContain("next-invocation-1");
  expect(second.reads.map((read) => read.cursor)).toEqual([0, 1, 0]);
});

it("ignores completion after unmounting and does not carry the old cursor into a new mount", async () => {
  const transport = source();
  await act(async () =>
    root.render(<EventTimelineWorkflow client={transport.client} reference={reference} />),
  );
  await act(async () => root.render(null));
  await transport.finish(0, page(1, 1, "unmounted"), true);
  expect(host.textContent).toBe("");
  await act(async () =>
    root.render(<EventTimelineWorkflow client={transport.client} reference={reference} />),
  );
  expect(transport.reads.map((read) => read.cursor)).toEqual([0, 0]);
  await transport.finish(1, page(1, 1, "mounted"), false);
  expect(host.textContent).not.toContain("unmounted");
  expect(host.textContent).toContain("mounted-1");
});
