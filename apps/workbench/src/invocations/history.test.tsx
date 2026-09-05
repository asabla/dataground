import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { type InvocationSummaryResource, listInvocations } from "./client";
import { type InvocationHistoryState, invocationHistoryReducer } from "./history";
import { InvocationHistoryWorkflow } from "./InvocationHistoryWorkflow";
import { InvocationInspectionWorkflow } from "./InvocationInspectionWorkflow";

const target = {
  isolationDomainId: "iso_00000000000000000001",
  serviceId: "svc_00000000000000000001",
};
const summary: InvocationSummaryResource = {
  metadata: {
    id: "inv_00000000000000000001",
    isolationDomainId: target.isolationDomainId,
    createdAt: "2026-09-01T12:00:00.123456Z",
    updatedAt: "2026-09-01T12:01:00Z",
    createdBy: "operator",
    generation: 1,
    version: 2,
  },
  alias: "old-route",
  serviceId: target.serviceId,
  revisionId: "rev_00000000000000000001",
  operationId: "op_00000000000000000001",
  correlationId: "cor_00000000000000000001",
  state: "waiting",
};
function clientFor(data: unknown) {
  return {
    GET: async () => ({ data, response: new Response(null, { status: 200 }) }),
  } as unknown as DataGroundClient;
}

describe("invocation history client", () => {
  it("fetches bounded exact-service pages and strips content and unknown metadata", async () => {
    let seen: unknown;
    const data = {
      items: [
        {
          ...summary,
          input: { secret: "hidden" },
          result: { secret: "hidden" },
          artifactIds: ["hidden"],
          metadata: { ...summary.metadata, nativeEndpoint: "hidden" },
        },
      ],
      nextCursor: "cursor-2",
    };
    const client = {
      GET: async (path: string, options: unknown) => {
        seen = { path, options };
        return { data, response: new Response(null, { status: 200 }) };
      },
    } as unknown as DataGroundClient;
    const result = await listInvocations(client, target, "cursor-1");
    assert.deepEqual(seen, {
      path: "/v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/invocations",
      options: { params: { path: target, query: { limit: 50, cursor: "cursor-1" } } },
    });
    assert.deepEqual(result, { ok: true, page: { items: [summary], nextCursor: "cursor-2" } });
  });
  it("rejects scope collisions, malformed pages and stalled continuation", async () => {
    for (const data of [
      { items: [{ ...summary, serviceId: "svc_00000000000000000002" }] },
      {
        items: [
          {
            ...summary,
            metadata: { ...summary.metadata, isolationDomainId: "iso_00000000000000000002" },
          },
        ],
      },
      { items: [{ ...summary, completedAt: "invalid" }] },
      { items: [{ ...summary, operationId: "native-id" }] },
      { items: [summary, summary] },
      { items: Array(51).fill(summary) },
      { items: [summary], nextCursor: "previous" },
      { items: [], nextCursor: "next" },
      { items: [summary], nextCursor: "x".repeat(513) },
    ]) {
      assert.equal((await listInvocations(clientFor(data), target, "previous")).ok, false);
    }
  });
  it("rejects invalid requests before transport and retains safe denial correlation", async () => {
    let called = false;
    const client = {
      GET: async () => {
        called = true;
        throw Error("private transport diagnostics");
      },
    } as unknown as DataGroundClient;
    assert.equal((await listInvocations(client, { ...target, serviceId: "wrong" })).ok, false);
    assert.equal((await listInvocations(client, target, "")).ok, false);
    assert.equal(called, false);
    const unavailable = await listInvocations(client, target);
    assert.equal(unavailable.ok, false);
    assert.doesNotMatch(JSON.stringify(unavailable), /private transport/u);
    const denied = {
      error: {
        code: "AUTHORIZATION_DENIED",
        message: "Not allowed.",
        correlationId: "cor-request",
        retryable: false,
      },
    };
    const result = await listInvocations(
      {
        GET: async () => ({ error: denied, response: new Response(null, { status: 403 }) }),
      } as unknown as DataGroundClient,
      target,
    );
    assert.deepEqual(result, { ok: false, error: { ...denied.error, status: 403 } });
    assert.deepEqual(await listInvocations(clientFor({ items: [] }), target), {
      ok: true,
      page: { items: [] },
    });
  });
});

const client = {} as DataGroundClient;
const loaded: InvocationHistoryState = {
  client,
  scope: "scope-1",
  requestId: 1,
  loading: false,
  items: [summary],
  nextCursor: "cursor-1",
  seenCursors: ["cursor-1"],
};
describe("invocation history recovery", () => {
  it("clears history on refresh, scope and client changes and ignores late responses", () => {
    for (const action of [
      { type: "requested" as const, client, scope: loaded.scope, requestId: 2 },
      { type: "requested" as const, client, scope: "scope-2", requestId: 2, cursor: "cursor-1" },
      {
        type: "requested" as const,
        client: {} as DataGroundClient,
        scope: loaded.scope,
        requestId: 2,
        cursor: "cursor-1",
      },
    ]) {
      const pending = invocationHistoryReducer(loaded, action);
      assert.deepEqual(pending.items, []);
      assert.equal(
        invocationHistoryReducer(pending, {
          type: "received",
          requestId: 1,
          result: { ok: true, page: { items: [summary] } },
        }),
        pending,
      );
    }
  });
  it("preserves earlier pages on transient failure, clears them on denial, and rejects cycles", () => {
    const pending = invocationHistoryReducer(loaded, {
      type: "requested",
      client,
      scope: loaded.scope,
      requestId: 2,
      cursor: "cursor-1",
    });
    const fail = (status: number) =>
      invocationHistoryReducer(pending, {
        type: "received",
        requestId: 2,
        result: {
          ok: false,
          error: { code: "UNAVAILABLE", message: "Failed.", status, retryable: status === 503 },
        },
      });
    assert.deepEqual(fail(503).items, loaded.items);
    assert.equal(fail(503).requestCursor, "cursor-1");
    assert.deepEqual(fail(403).items, []);
    const other = { ...summary, metadata: { ...summary.metadata, id: "inv_00000000000000000002" } };
    for (const page of [
      { items: [other], nextCursor: "cursor-1" },
      { items: [summary], nextCursor: "cursor-2" },
    ]) {
      const stalled = invocationHistoryReducer(pending, {
        type: "received",
        requestId: 2,
        result: { ok: true, page },
      });
      assert.equal(stalled.error?.code, "WORKBENCH_INVOCATION_HISTORY_STALLED");
      assert.deepEqual(stalled.items, loaded.items);
      assert.equal(stalled.nextCursor, undefined);
    }
    const complete = invocationHistoryReducer(pending, {
      type: "received",
      requestId: 2,
      result: { ok: true, page: { items: [other] } },
    });
    assert.deepEqual(complete.items, [summary, other]);
    assert.equal(complete.nextCursor, undefined);
  });
  it("renders explicit loading and inspects an invocation without a current alias selection", () => {
    const history = renderToStaticMarkup(
      <InvocationHistoryWorkflow client={client} target={target} />,
    );
    assert.match(history, /Loading invocation history/u);
    assert.doesNotMatch(history, /No invocations yet/u);
    const noop = () => undefined;
    const inspection = renderToStaticMarkup(
      <InvocationInspectionWorkflow
        client={client}
        canCancelInvocation={false}
        canResolveApproval={false}
        reference={{
          isolationDomainId: target.isolationDomainId,
          invocationId: summary.metadata.id,
        }}
        onCloseApproval={noop}
        onCloseArtifact={noop}
        onInspectApproval={noop}
        onInspectArtifact={noop}
      />,
    );
    assert.doesNotMatch(inspection, /active alias route|Scope mismatch/u);
    assert.match(inspection, /Loading/u);
  });
});
