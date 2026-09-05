import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { listServiceAliases, type ServiceAliasResource } from "./aliasClient";
import { type AliasDiscoveryState, aliasDiscoveryReducer } from "./discovery";

const scope = {
  isolationDomainId: "iso_00000000000000000001",
  serviceId: "svc_00000000000000000001",
};
const alias: ServiceAliasResource = {
  name: "canary",
  serviceId: scope.serviceId,
  revisionId: "rev_00000000000000000001",
  metadata: {
    id: "als_00000000000000000001",
    isolationDomainId: scope.isolationDomainId,
    createdBy: "operator",
    createdAt: "2026-09-05T12:00:00Z",
    updatedAt: "2026-09-05T12:00:00Z",
    version: 1,
    generation: 1,
  },
};
function clientFor(data: unknown, status = 200, error?: unknown) {
  return {
    GET: async () => ({ data, error, response: new Response(null, { status }) }),
  } as unknown as DataGroundClient;
}
describe("route discovery client", () => {
  it("requests bounded exact-scope pages and strips unknown resource fields", async () => {
    const calls: unknown[] = [];
    const client = {
      GET: async (path: string, options: unknown) => {
        calls.push({ path, options });
        return {
          data: {
            items: [
              { ...alias, native: "hidden", metadata: { ...alias.metadata, native: "hidden" } },
            ],
            nextCursor: "next",
          },
          response: new Response(null, { status: 200 }),
        };
      },
    } as unknown as DataGroundClient;
    const result = await listServiceAliases(client, scope, "previous");
    assert.deepEqual(calls, [
      {
        path: "/v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/aliases",
        options: { params: { path: scope, query: { limit: 50, cursor: "previous" } } },
      },
    ]);
    assert.deepEqual(result, { ok: true, page: { items: [alias], nextCursor: "next" } });
  });
  it("rejects malformed, unordered, duplicate, withdrawn, and out-of-scope pages", async () => {
    const changed = (fields: object) => ({ items: [{ ...alias, ...fields }] });
    for (const data of [
      undefined,
      {},
      { items: null },
      { items: Array(51).fill(alias) },
      changed({ name: "Bad" }),
      changed({ withdrawnAt: null }),
      changed({ serviceId: "svc_00000000000000000002" }),
      changed({ metadata: { ...alias.metadata, isolationDomainId: "iso_00000000000000000002" } }),
      { items: [alias, alias] },
      {
        items: [
          alias,
          {
            ...alias,
            name: "alpha",
            metadata: { ...alias.metadata, id: "als_00000000000000000002" },
          },
        ],
      },
      { items: [], nextCursor: "next" },
      { items: [alias], nextCursor: "previous" },
      { items: [alias], nextCursor: "bad+cursor" },
    ]) {
      const result = await listServiceAliases(clientFor(data), scope, "previous");
      assert.equal(result.ok, false, JSON.stringify(data));
    }
    assert.deepEqual(await listServiceAliases(clientFor({ items: [] }), scope), {
      ok: true,
      page: { items: [] },
    });
    assert.equal((await listServiceAliases(clientFor({ items: [alias] }, 202), scope)).ok, false);
  });
  it("does not send invalid scope or cursors and distinguishes denial from transport failure", async () => {
    let calls = 0;
    const client = {
      GET: async () => {
        calls++;
        throw new Error("private upstream content");
      },
    } as unknown as DataGroundClient;
    for (const cursor of ["", "bad+cursor", "a".repeat(513)])
      assert.equal((await listServiceAliases(client, scope, cursor)).ok, false);
    assert.equal((await listServiceAliases(client, { ...scope, serviceId: "bad" })).ok, false);
    assert.equal(calls, 0);
    const unavailable = await listServiceAliases(client, scope);
    assert.equal(unavailable.ok, false);
    if (!unavailable.ok) {
      assert.equal(unavailable.error.retryable, true);
      assert.ok(!unavailable.error.message.includes("private"));
    }
    const denied = await listServiceAliases(
      clientFor(undefined, 403, {
        error: {
          code: "ACCESS_DENIED",
          correlationId: "cor_denied",
          message: "Access denied.",
          retryable: false,
        },
      }),
      scope,
    );
    assert.equal(denied.ok, false);
    if (!denied.ok) {
      assert.equal(denied.error.status, 403);
      assert.equal(denied.error.retryable, false);
    }
  });
});

describe("route discovery state", () => {
  const client = clientFor({ items: [] });
  const state: AliasDiscoveryState = {
    client,
    scope: "scope",
    requestId: 1,
    loading: false,
    items: [alias],
    nextCursor: "next",
    seenCursors: ["next"],
  };
  const pending = () =>
    aliasDiscoveryReducer(state, {
      type: "requested",
      client,
      scope: "scope",
      requestId: 2,
      cursor: "next",
    });
  it("ignores stale results, preserves retry boundaries, and clears denied pages", () => {
    const current = pending();
    assert.equal(
      aliasDiscoveryReducer(current, {
        type: "received",
        requestId: 1,
        result: { ok: true, page: { items: [] } },
      }),
      current,
    );
    const failure = aliasDiscoveryReducer(current, {
      type: "received",
      requestId: 2,
      result: {
        ok: false,
        error: { code: "TEMPORARY_FAILURE", message: "Unavailable", retryable: true, status: 503 },
      },
    });
    assert.equal(failure.requestCursor, "next");
    assert.deepEqual(failure.items, [alias]);
    const denied = aliasDiscoveryReducer(current, {
      type: "received",
      requestId: 2,
      result: {
        ok: false,
        error: { code: "ACCESS_DENIED", message: "Denied", retryable: false, status: 403 },
      },
    });
    assert.deepEqual(denied.items, []);
    assert.equal(denied.nextCursor, undefined);
  });
  it("rejects cursor loops, duplicate identities, and regressing page order", () => {
    const next = {
      ...alias,
      name: "stable",
      metadata: { ...alias.metadata, id: "als_00000000000000000002" },
    };
    for (const page of [
      { items: [next], nextCursor: "next" },
      { items: [{ ...alias, name: "stable" }] },
      { items: [{ ...next, name: "alpha" }] },
    ]) {
      const result = aliasDiscoveryReducer(pending(), {
        type: "received",
        requestId: 2,
        result: { ok: true, page },
      });
      assert.equal(result.error?.code, "WORKBENCH_ALIAS_DISCOVERY_STALLED");
      assert.deepEqual(result.items, [alias]);
    }
    const result = aliasDiscoveryReducer(pending(), {
      type: "received",
      requestId: 2,
      result: { ok: true, page: { items: [next] } },
    });
    assert.deepEqual(result.items, [alias, next]);
    assert.equal(result.nextCursor, undefined);
  });
  it("resets routes and continuation state on refresh, scope or client changes", () => {
    for (const request of [
      { client, scope: "scope" },
      { client, scope: "other", cursor: "next" },
      { client: clientFor({ items: [] }), scope: "scope", cursor: "next" },
    ]) {
      const result = aliasDiscoveryReducer(state, { type: "requested", requestId: 2, ...request });
      assert.deepEqual(result.items, []);
      assert.deepEqual(result.seenCursors, []);
    }
  });
});
