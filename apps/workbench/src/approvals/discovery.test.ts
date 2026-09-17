import assert from "node:assert/strict";
import { it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { listInvocationApprovals } from "./client";

const reference = {
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};
const item = {
  schemaVersion: "dataground.invocation-approval/v2",
  id: "apr_00000000000000000001",
  ...reference,
  requestedAction: "workspace.change",
  state: "pending",
  version: 1,
  createdAt: "2026-09-17T10:00:00Z",
  updatedAt: "2026-09-17T10:00:00Z",
  expiresAt: "2026-09-17T10:05:00Z",
};
function clientFor(data: unknown): DataGroundClient {
  return {
    GET: async () => ({ data, response: new Response(null, { status: 200 }) }),
  } as unknown as DataGroundClient;
}
it("requests bounded discovery in the exact invocation and preserves empty pages", async () => {
  let observed: unknown;
  const client = {
    GET: async (path: string, options: unknown) => {
      observed = { path, options };
      return {
        data: { items: [item], nextCursor: "cursor-2" },
        response: new Response(null, { status: 200 }),
      };
    },
  } as unknown as DataGroundClient;
  const result = await listInvocationApprovals(client, reference, "cursor-1");
  assert.equal(result.ok, true);
  assert.deepEqual(observed, {
    path: "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/approvals",
    options: { params: { path: reference, query: { limit: 50, cursor: "cursor-1" } } },
  });
  assert.deepEqual(await listInvocationApprovals(clientFor({ items: [] }), reference), {
    ok: true,
    page: { items: [] },
  });
});
it("rejects substituted, malformed, duplicate and unbounded discovery responses", async () => {
  for (const data of [
    { items: [{ ...item, isolationDomainId: "iso_00000000000000000002" }] },
    { items: [{ ...item, invocationId: "inv_00000000000000000002" }] },
    { items: [{ ...item, id: "bad" }] },
    { items: [{ ...item, operationId: "private" }] },
    { items: [{ ...item, expiresAt: undefined }] },
    { items: [{ ...item, state: "new-state" }] },
    { items: [item, item] },
    { items: Array(51).fill(item) },
    { items: [item], nextCursor: "cursor-1" },
    { items: [item], nextCursor: "bad+cursor" },
    { items: [], nextCursor: "cursor-2" },
    { items: [item], nextCursor: "a".repeat(513) },
    { items: [item], internalRoute: "private" },
    { items: null },
    {},
  ])
    assert.equal((await listInvocationApprovals(clientFor(data), reference, "cursor-1")).ok, false);
});
it("validates references before access and withholds transport details", async () => {
  let calls = 0;
  const client = {
    GET: async () => {
      calls++;
      throw new Error("private token");
    },
  } as unknown as DataGroundClient;
  assert.equal(
    (await listInvocationApprovals(client, { ...reference, invocationId: "bad" })).ok,
    false,
  );
  for (const cursor of ["", "bad+cursor", "a".repeat(513)])
    assert.equal((await listInvocationApprovals(client, reference, cursor)).ok, false);
  assert.equal(calls, 0);
  const result = await listInvocationApprovals(client, reference);
  assert.equal(result.ok, false);
  assert.equal(JSON.stringify(result).includes("private token"), false);
});
it("preserves authorization denial and correlation", async () => {
  const problem = {
    code: "FORBIDDEN",
    correlationId: "cor_00000000000000000001",
    message: "Access denied.",
    retryable: false,
  };
  const client = {
    GET: async () => ({ error: { error: problem }, response: new Response(null, { status: 403 }) }),
  } as unknown as DataGroundClient;
  assert.deepEqual(await listInvocationApprovals(client, reference), {
    ok: false,
    error: { ...problem, status: 403 },
  });
});
