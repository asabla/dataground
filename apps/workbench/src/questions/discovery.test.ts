import assert from "node:assert/strict";
import { it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { listInvocationQuestions } from "./client";

const reference = {
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};
const item = {
  schemaVersion: "dataground.invocation-question-summary/v1",
  id: "qst_00000000000000000001",
  ...reference,
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
  const result = await listInvocationQuestions(client, reference, "cursor-1");
  assert.equal(result.ok, true);
  assert.deepEqual(observed, {
    path: "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/questions",
    options: {
      params: { path: reference, query: { limit: 50, cursor: "cursor-1" } },
      cache: "no-store",
    },
  });
  assert.deepEqual(await listInvocationQuestions(clientFor({ items: [] }), reference), {
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
    { items: [{ ...item, questions: ["private prompt"] }] },
    { items: [{ ...item, answers: ["private answer"] }] },
    { items: [{ ...item, answeredBy: "private actor" }] },
    { items: [{ ...item, version: 2 }] },
    { items: [{ ...item, state: "constructor" }] },
    { items: [{ ...item, updatedAt: "2020-01-01T00:00:00Z" }] },
    { items: [{ ...item, expiresAt: item.createdAt }] },
    { items: [{ ...item, expiresAt: "2026-09-17T11:00:00Z" }] },
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
    assert.equal((await listInvocationQuestions(clientFor(data), reference, "cursor-1")).ok, false);
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
    (await listInvocationQuestions(client, { ...reference, invocationId: "bad" })).ok,
    false,
  );
  for (const cursor of ["", "bad+cursor", "a".repeat(513)])
    assert.equal((await listInvocationQuestions(client, reference, cursor)).ok, false);
  assert.equal(calls, 0);
  const result = await listInvocationQuestions(client, reference);
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
  assert.deepEqual(await listInvocationQuestions(client, reference), {
    ok: false,
    error: {
      ...problem,
      message: "You are not authorized to discover questions for this invocation.",
      status: 403,
    },
  });
});

it("accepts each retained lifecycle state only at its supported versions", async () => {
  for (const [state, versions] of Object.entries({
    pending: [1],
    answered: [2],
    delivering: [3],
    delivered: [4],
    delivery_unknown: [4],
    closed: [2, 3],
    expired: [2, 3],
  })) {
    for (const version of versions)
      assert.equal(
        (
          await listInvocationQuestions(
            clientFor({ items: [{ ...item, state, version }] }),
            reference,
          )
        ).ok,
        true,
      );
  }
});
