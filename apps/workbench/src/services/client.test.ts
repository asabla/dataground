import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { createAgentService, listAgentServices } from "./client";

const isolationDomainId = "iso_00000000000000000001";
const service = {
  description: "A governed service.",
  futureResponseField: "must be stripped",
  metadata: {
    createdAt: "2026-08-14T12:00:00Z",
    createdBy: "reference-runtime",
    generation: 1,
    id: "svc_00000000000000000001",
    isolationDomainId,
    labels: { native: "must be stripped" },
    updatedAt: "2026-08-14T12:00:00Z",
    version: 1,
  },
  name: "Reference service",
};

describe("agent service client", () => {
  it("lists one bounded scoped service page and strips ungoverned fields", async () => {
    const calls: Array<{ options: unknown; path: string }> = [];
    const client = {
      GET: async (path: string, options: unknown) => {
        calls.push({ options, path });
        return {
          data: { items: [service], nextCursor: "opaque_cursor_01", total: 99 },
          response: new Response(null, { status: 200 }),
        };
      },
    } as unknown as DataGroundClient;

    const result = await listAgentServices(client, isolationDomainId, "opaque_cursor_00");

    assert.equal(result.ok, true);
    assert.deepEqual(calls, [
      {
        options: {
          params: {
            path: { isolationDomainId },
            query: { cursor: "opaque_cursor_00", limit: 50 },
          },
        },
        path: "/v1/isolation-domains/{isolationDomainId}/agent-services",
      },
    ]);
    if (result.ok) {
      assert.equal(result.page.nextCursor, "opaque_cursor_01");
      const [listedService] = result.page.items;
      assert.ok(listedService);
      assert.equal("futureResponseField" in listedService, false);
      assert.equal("labels" in listedService.metadata, false);
      assert.equal("total" in result.page, false);
    }
  });

  it("rejects malformed, duplicate, and cross-domain service pages", async () => {
    for (const page of [
      {
        items: [
          {
            ...service,
            metadata: { ...service.metadata, isolationDomainId: "iso_00000000000000000002" },
          },
        ],
      },
      { items: [service, service] },
      {
        items: [
          service,
          {
            ...service,
            metadata: {
              ...service.metadata,
              createdAt: "2026-08-15T12:00:00Z",
              id: "svc_00000000000000000002",
              updatedAt: "2026-08-15T12:00:00Z",
            },
          },
        ],
      },
      { items: [], nextCursor: "cursor_without_items" },
    ]) {
      const result = await listAgentServices(
        {
          GET: async () => ({ data: page, response: new Response(null, { status: 200 }) }),
        } as unknown as DataGroundClient,
        isolationDomainId,
      );
      assert.equal(result.ok, false);
      if (!result.ok) assert.equal(result.error.code, "WORKBENCH_SERVICE_LIST_SCOPE_MISMATCH");
    }
  });

  it("rejects a non-advancing cursor without retrying transport", async () => {
    let requests = 0;
    const result = await listAgentServices(
      {
        GET: async () => {
          requests++;
          return {
            data: { items: [service], nextCursor: "opaque_cursor_00" },
            response: new Response(null, { status: 200 }),
          };
        },
      } as unknown as DataGroundClient,
      isolationDomainId,
      "opaque_cursor_00",
    );

    assert.equal(requests, 1);
    assert.equal(result.ok, false);
    if (!result.ok) assert.equal(result.error.code, "WORKBENCH_SERVICE_LIST_CURSOR_STALLED");
  });

  it("submits the exact scoped command and strips ungoverned response fields", async () => {
    const calls: Array<{ options: unknown; path: string }> = [];
    const client = {
      POST: async (path: string, options: unknown) => {
        calls.push({ options, path });
        return { data: service, response: new Response(null, { status: 201 }) };
      },
    } as unknown as DataGroundClient;

    const result = await createAgentService(
      client,
      isolationDomainId,
      { description: "A governed service.", name: "  Reference service  " },
      "service:create0001",
    );

    assert.equal(result.ok, true);
    assert.deepEqual(calls, [
      {
        options: {
          body: { description: "A governed service.", name: "Reference service" },
          params: {
            header: { "Idempotency-Key": "service:create0001" },
            path: { isolationDomainId },
          },
        },
        path: "/v1/isolation-domains/{isolationDomainId}/agent-services",
      },
    ]);
    if (result.ok) {
      assert.equal("futureResponseField" in result.service, false);
      assert.equal("labels" in result.service.metadata, false);
    }
  });

  it("rejects invalid scope, fields, and identifiers before transport", async () => {
    let requested = false;
    const client = {
      POST: async () => {
        requested = true;
        return { data: service, response: new Response(null, { status: 201 }) };
      },
    } as unknown as DataGroundClient;

    for (const [domain, request, key] of [
      ["native-domain", { name: "Service" }, "service:create0002"],
      [isolationDomainId, { name: "   " }, "service:create0003"],
      [isolationDomainId, { name: "Service", description: "x".repeat(2049) }, "service:create0004"],
      [isolationDomainId, { name: "Service" }, "short"],
    ] as const) {
      assert.equal((await createAgentService(client, domain, request, key)).ok, false);
    }
    assert.equal(requested, false);
  });

  it("rejects cross-domain and substituted service responses", async () => {
    for (const responseService of [
      {
        ...service,
        metadata: { ...service.metadata, isolationDomainId: "iso_00000000000000000002" },
      },
      { ...service, name: "Substituted service" },
      { ...service, description: "Substituted description" },
    ]) {
      const result = await createAgentService(
        {
          POST: async () => ({
            data: responseService,
            response: new Response(null, { status: 201 }),
          }),
        } as unknown as DataGroundClient,
        isolationDomainId,
        { description: service.description, name: "Reference service" },
        "service:create0005",
      );

      assert.equal(result.ok, false);
      if (!result.ok) assert.equal(result.error.code, "WORKBENCH_SERVICE_SCOPE_MISMATCH");
    }
  });

  it("classifies thrown transport as an uncertain retryable outcome", async () => {
    const result = await createAgentService(
      {
        POST: async () => {
          throw new Error("secret upstream diagnostic");
        },
      } as unknown as DataGroundClient,
      isolationDomainId,
      { name: "Reference service" },
      "service:create0006",
    );

    assert.equal(result.ok, false);
    if (!result.ok) {
      assert.equal(result.error.code, "WORKBENCH_SERVICE_CREATION_UNCONFIRMED");
      assert.equal(result.error.outcomeUnknown, true);
      assert.equal(result.error.retryable, true);
      assert.doesNotMatch(result.error.message, /secret|upstream/u);
    }
  });
});
