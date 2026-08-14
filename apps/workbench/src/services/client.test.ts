import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { createAgentService } from "./client";

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
