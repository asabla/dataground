import assert from "node:assert/strict";
import { it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { readAgentService } from "./client";

const isolationDomainId = "iso_00000000000000000001";
const serviceId = "svc_00000000000000000001";
const service = {
  name: "Exact service",
  internal: "must not be returned",
  metadata: {
    id: serviceId,
    isolationDomainId,
    createdBy: "operator",
    createdAt: "2026-10-29T08:00:00Z",
    updatedAt: "2026-10-29T08:00:00Z",
    version: 1,
    generation: 1,
  },
};

it("reads exactly one scoped service and strips ungoverned fields", async () => {
  const calls: unknown[] = [];
  const client = {
    GET: async (path: string, options: unknown) => {
      calls.push({ path, options });
      return { data: service, response: new Response(null, { status: 200 }) };
    },
  } as unknown as DataGroundClient;
  const result = await readAgentService(client, isolationDomainId, serviceId);
  assert.equal(result.ok, true);
  assert.deepEqual(calls, [
    {
      path: "/v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}",
      options: { params: { path: { isolationDomainId, serviceId } } },
    },
  ]);
  if (result.ok) assert.equal("internal" in result.service, false);
});

it("rejects wrong identity, scope, metadata and response status", async () => {
  for (const [data, status] of [
    [{ ...service, metadata: { ...service.metadata, id: "svc_00000000000000000002" } }, 200],
    [
      {
        ...service,
        metadata: { ...service.metadata, isolationDomainId: "iso_00000000000000000002" },
      },
      200,
    ],
    [{ ...service, metadata: { ...service.metadata, version: 0 } }, 200],
    [service, 201],
  ] as const) {
    const client = {
      GET: async () => ({ data, response: new Response(null, { status }) }),
    } as unknown as DataGroundClient;
    const result = await readAgentService(client, isolationDomainId, serviceId);
    assert.equal(result.ok, false);
    if (!result.ok) assert.equal(result.error.code, "WORKBENCH_SERVICE_READ_SCOPE_MISMATCH");
  }
});

it("rejects malformed targets before transport and preserves safe denial", async () => {
  let count = 0;
  const error = {
    code: "ACTION_FORBIDDEN",
    message: "Service read is not permitted.",
    retryable: false,
    correlationId: "cor_service",
  };
  const client = {
    GET: async () => {
      count++;
      return { error: { error }, response: new Response(null, { status: 403 }) };
    },
  } as unknown as DataGroundClient;
  for (const [scope, id] of [
    ["bad", serviceId],
    [isolationDomainId, "../service"],
  ] as const) {
    assert.equal((await readAgentService(client, scope, id)).ok, false);
  }
  assert.equal(count, 0);
  const result = await readAgentService(client, isolationDomainId, serviceId);
  assert.deepEqual(result, { ok: false, error: { ...error, status: 403 } });
});

it("reports a retryable read failure without leaking transport errors", async () => {
  const client = {
    GET: async () => {
      throw new Error("secret transport detail");
    },
  } as unknown as DataGroundClient;
  const result = await readAgentService(client, isolationDomainId, serviceId);
  assert.equal(result.ok, false);
  if (!result.ok) {
    assert.equal(result.error.retryable, true);
    assert.doesNotMatch(result.error.message, /secret/);
  }
});
