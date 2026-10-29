import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { DataGroundClient } from "../contracts/client";
import { ArtifactDiscoveryWorkflow } from "./ArtifactDiscoveryWorkflow";

const reference = {
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};
const firstID = "art_00000000000000000001",
  secondID = "art_00000000000000000002";
function artifact(id = firstID) {
  return {
    metadata: {
      id,
      isolationDomainId: reference.isolationDomainId,
      createdBy: "operator",
      createdAt: "2026-10-29T08:00:00Z",
      updatedAt: "2026-10-29T08:00:00Z",
      generation: 1,
      version: 1,
    },
    invocationId: reference.invocationId,
    name: id === firstID ? "Quarterly report.csv" : "Analysis notes.txt",
    digest: `sha256:${"a".repeat(64)}`,
    mediaType: "text/plain",
    sizeBytes: 20,
    kind: "file",
    sensitive: true,
    state: "available",
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
  return Array.from(host.querySelectorAll("button")).find(
    (value) => (value.getAttribute("aria-label") ?? value.textContent) === text,
  );
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((complete) => {
    resolve = complete;
  });
  return { promise, resolve };
}

it("pages metadata and selects an artifact with the keyboard without reading content", async () => {
  const more = deferred<ReturnType<typeof response>>();
  const reads = vi.fn(
    async (_path: string, options: { params: { query?: { cursor?: string } } }) =>
      options.params.query?.cursor
        ? more.promise
        : response({ items: [artifact()], nextCursor: "next-1" }),
  );
  const inspect = vi.fn();
  const client = { GET: reads } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <ArtifactDiscoveryWorkflow
        client={client}
        reference={reference}
        onInspectArtifact={inspect}
      />,
    ),
  );
  const load = button("Load more artifacts");
  await act(async () => {
    load?.click();
    load?.click();
  });
  expect(reads).toHaveBeenCalledTimes(2);
  expect(button("Inspect Quarterly report.csv")?.disabled).toBe(true);
  await act(async () => more.resolve(response({ items: [artifact(secondID)] })));
  const open = button("Inspect Analysis notes.txt");
  await act(async () =>
    userEvent.click(
      open?.closest('[role="row"]')?.querySelector('[role="rowheader"]') as HTMLElement,
    ),
  );
  await act(async () => userEvent.keyboard("{End}"));
  expect(document.activeElement).toBe(open);
  await act(async () => userEvent.keyboard("{Enter}"));
  expect(inspect).toHaveBeenCalledWith({ ...reference, artifactId: secondID });
  expect(reads).toHaveBeenCalledTimes(2);
  const search = host.querySelector('input[type="search"]') as HTMLInputElement;
  await act(async () => userEvent.fill(search, "Quarterly"));
  expect(button("Inspect Analysis notes.txt")).toBeUndefined();
  expect(host.textContent).toContain("1 of 2 loaded artifacts");
});
it("clears metadata on access denial and rejects stale actions and repeated pages", async () => {
  let denied = false,
    repeated = false;
  const inspect = vi.fn();
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
        : response({ items: [artifact()], nextCursor: repeated ? "next-2" : "next-1" }),
  } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <ArtifactDiscoveryWorkflow
        client={client}
        reference={reference}
        onInspectArtifact={inspect}
      />,
    ),
  );
  const old = button("Inspect Quarterly report.csv");
  denied = true;
  await act(async () => button("Load more artifacts")?.click());
  expect(host.textContent).toContain("Access denied.");
  expect(button("Inspect Quarterly report.csv")).toBeUndefined();
  await act(async () => old?.click());
  expect(inspect).not.toHaveBeenCalled();
  denied = false;
  await act(async () => button("Refresh artifacts")?.click());
  repeated = true;
  await act(async () => button("Load more artifacts")?.click());
  expect(host.textContent).toContain("discovery did not advance");
  expect(button("Inspect Quarterly report.csv")).toBeUndefined();
});
it("ignores metadata returned after identity and run changes", async () => {
  const pending = deferred<ReturnType<typeof response>>();
  const client = { GET: () => pending.promise } as unknown as DataGroundClient;
  await act(async () =>
    root.render(<ArtifactDiscoveryWorkflow client={client} reference={reference} />),
  );
  const replacement = { GET: async () => response({ items: [] }) } as unknown as DataGroundClient;
  await act(async () =>
    root.render(
      <ArtifactDiscoveryWorkflow
        client={replacement}
        reference={{ ...reference, invocationId: "inv_00000000000000000002" }}
      />,
    ),
  );
  await act(async () => pending.resolve(response({ items: [artifact()] })));
  expect(host.textContent).toContain("No artifacts recorded");
  expect(host.textContent).not.toContain("Quarterly report.csv");
});
