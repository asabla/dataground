import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import type { ServiceRevisionHistoryResource } from "../revisions/client";
import { ServiceRevisionHistoryWorkflow } from "../revisions/ServiceRevisionHistoryWorkflow";
import { ResourceAuditWorkflow } from "./ResourceAuditWorkflow";
import { auditPage, reference } from "./testFixtures";

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
  const calls: {
    path: string;
    query: { cursor?: string };
    params: Record<string, string>;
    resolve: (result: unknown) => void;
  }[] = [];
  const client = {
    GET: (
      path: string,
      options: { params: { path: Record<string, string>; query: { cursor?: string } } },
    ) =>
      new Promise((resolve) =>
        calls.push({ path, query: options.params.query, params: options.params.path, resolve }),
      ),
  } as unknown as DataGroundClient;
  function call(index: number) {
    const request = calls[index];
    if (!request) throw new Error("Expected pending audit request");
    return request;
  }
  return {
    client,
    calls,
    call,
    finish: async (index: number, data: unknown = auditPage(), status = 200) => {
      await act(async () =>
        call(index).resolve({ data, error: data, response: new Response(null, { status }) }),
      );
    },
  };
}
it("reads only on request, blocks overlapping reads, and discards hidden late responses", async () => {
  const source = pendingClient();
  await act(async () =>
    root.render(<ResourceAuditWorkflow client={source.client} reference={reference} />),
  );
  expect(source.calls).toHaveLength(0);
  await click("Show audit");
  expect(source.calls).toHaveLength(1);
  await click("Hide audit");
  await click("Show audit");
  expect(source.calls).toHaveLength(2);
  await source.finish(0);
  expect(host.textContent).not.toContain("private-actor");
  expect(host.textContent).toContain("Loading audit records");
  await source.finish(1, auditPage(reference, 2));
  expect(host.textContent).toContain("private-actor-2");
  await click("Hide audit");
  expect(host.textContent).not.toContain("private-actor");
  expect(host.textContent).not.toContain("Read receipt");
});
it("clears records on client and resource changes without starting another read", async () => {
  const first = pendingClient(),
    second = pendingClient();
  await act(async () =>
    root.render(<ResourceAuditWorkflow client={first.client} reference={reference} />),
  );
  await click("Show audit");
  await first.finish(0);
  expect(host.textContent).toContain("private-actor-1");
  await act(async () =>
    root.render(<ResourceAuditWorkflow client={second.client} reference={reference} />),
  );
  expect(host.textContent).not.toContain("private-actor");
  expect(second.calls).toHaveLength(0);
  await click("Show audit");
  const other = { ...reference, resourceId: "inv_00000000000000000002" };
  await act(async () =>
    root.render(<ResourceAuditWorkflow client={second.client} reference={other} />),
  );
  await second.finish(0);
  expect(host.textContent).not.toContain("private-actor");
  expect(second.calls).toHaveLength(1);
  await click("Show audit");
  await second.finish(1, auditPage(other, 3));
  expect(host.textContent).toContain("private-actor-3");
  await act(async () =>
    root.render(
      <ResourceAuditWorkflow
        client={second.client}
        reference={{ ...other, isolationDomainId: "iso_00000000000000000002" }}
      />,
    ),
  );
  expect(host.textContent).not.toContain("private-actor");
  expect(second.calls).toHaveLength(2);
});
it("replaces pages, refreshes from the start, and clears prior evidence on denial or unavailability", async () => {
  const source = pendingClient();
  await act(async () =>
    root.render(<ResourceAuditWorkflow client={source.client} reference={reference} />),
  );
  await click("Show audit");
  await source.finish(0, auditPage(reference, 1, 50, true));
  expect(host.querySelectorAll("li")).toHaveLength(50);
  await click("Next audit page");
  expect(document.activeElement?.textContent).toBe("Hide audit");
  expect(source.call(1).query.cursor).toBe("arr_00000000000000000001");
  expect(host.querySelectorAll("li")).toHaveLength(0);
  await source.finish(1, auditPage(reference, 51));
  expect(host.querySelectorAll("li")).toHaveLength(1);
  expect(host.textContent).toContain("End of this audit snapshot");
  await click("Refresh audit");
  expect(document.activeElement?.textContent).toBe("Hide audit");
  expect(source.call(2).query.cursor).toBeUndefined();
  await source.finish(
    2,
    { error: { message: "private-upstream", correlationId: "cor_00000000000000000001" } },
    403,
  );
  expect(host.querySelector("[role=alert]")?.textContent).toContain("You do not have access");
  expect(document.activeElement?.textContent).toBe("Retry audit read");
  expect(host.textContent).not.toContain("private");
  expect(host.textContent).not.toContain("Read receipt");
  await click("Retry audit read");
  await source.finish(3, {}, 503);
  expect(host.textContent).toContain("Audit records are unavailable for this connection");
  expect(host.textContent).not.toContain("No audit records");
});
it("rejects a repeated page even if the server returns a fresh receipt", async () => {
  const source = pendingClient();
  await act(async () =>
    root.render(<ResourceAuditWorkflow client={source.client} reference={reference} />),
  );
  await click("Show audit");
  await source.finish(0, auditPage(reference, 1, 50, true));
  await click("Next audit page");
  await source.finish(1, { ...auditPage(), receiptId: "arr_00000000000000000099" });
  expect(host.textContent).toContain("did not advance");
  expect(host.textContent).not.toContain("private-actor");
});
it("allows retired revision audit inspection and closes it when the connection changes", async () => {
  const source = pendingClient(),
    other = pendingClient();
  const revision: ServiceRevisionHistoryResource = {
    metadata: {
      id: "rev_00000000000000000001",
      isolationDomainId: reference.isolationDomainId,
      createdAt: "2026-09-17T12:00:00Z",
      updatedAt: "2026-09-17T12:00:00Z",
      createdBy: "creator",
      version: 2,
      generation: 1,
    },
    serviceId: "svc_00000000000000000001",
    revisionNumber: 1,
    state: "retired",
    requiredCapabilities: [],
    runtimeProfile: "reference/v1",
  };
  const props = {
    isolationDomainId: reference.isolationDomainId,
    serviceId: revision.serviceId,
    isLoading: false,
    isLoadingMore: false,
    revisions: [revision],
    onRetry: () => {},
    onLoadMore: () => {},
    onOpen: () => {},
  };
  await act(async () =>
    root.render(<ServiceRevisionHistoryWorkflow {...props} client={source.client} />),
  );
  expect(host.textContent).not.toContain("Open revision 1");
  await click("Inspect audit for revision 1");
  expect(source.calls).toHaveLength(0);
  await click("Show audit");
  expect(source.call(0).params.revisionId).toBe(revision.metadata.id);
  await source.finish(
    0,
    auditPage({
      isolationDomainId: reference.isolationDomainId,
      resourceType: "service-revision",
      resourceId: revision.metadata.id,
    }),
  );
  expect(host.textContent).toContain("private-actor");
  await click("Close revision audit");
  expect(document.activeElement?.id).toBe("revision-history-title");
  await click("Inspect audit for revision 1");
  await click("Show audit");
  await act(async () =>
    root.render(<ServiceRevisionHistoryWorkflow {...props} client={other.client} />),
  );
  expect(host.textContent).not.toContain("private-actor");
  expect(host.textContent).not.toContain("Close revision audit");
  expect(other.calls).toHaveLength(0);
  await act(async () =>
    root.render(
      <ServiceRevisionHistoryWorkflow
        {...props}
        serviceId="svc_00000000000000000002"
        client={other.client}
      />,
    ),
  );
  await click("Inspect audit for revision 1");
  expect(host.textContent).not.toContain("Close revision audit");
  expect(other.calls).toHaveLength(0);
});
