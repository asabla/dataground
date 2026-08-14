import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { createServiceRevision } from "./client";

const isolationDomainId = "iso_00000000000000000001";
const serviceId = "svc_00000000000000000001";
const request = {
  inputSchema: { properties: { prompt: { type: "string" } }, type: "object" },
  outputSchema: { properties: { answer: { type: "string" } }, type: "object" },
  requiredCapabilities: ["tool", "usage"],
  runtimeProfile: "reference/v1",
};
const revision = {
  futureResponseField: "must be stripped",
  inputSchema: { type: "object", properties: { prompt: { type: "string" } } },
  metadata: {
    createdAt: "2026-08-14T16:00:00Z",
    createdBy: "reference-runtime",
    generation: 1,
    id: "rev_00000000000000000001",
    isolationDomainId,
    labels: { native: "must be stripped" },
    updatedAt: "2026-08-14T16:00:00Z",
    version: 1,
  },
  outputSchema: { type: "object", properties: { answer: { type: "string" } } },
  requiredCapabilities: ["tool", "usage"],
  revisionNumber: 1,
  runtimeProfile: "reference/v1",
  serviceId,
  state: "draft",
};

describe("service revision client", () => {
  it("submits the exact scoped command and strips ungoverned response fields", async () => {
    const calls: Array<{ options: unknown; path: string }> = [];
    const client = {
      POST: async (path: string, options: unknown) => {
        calls.push({ options, path });
        return { data: revision, response: new Response(null, { status: 201 }) };
      },
    } as unknown as DataGroundClient;

    const result = await createServiceRevision(
      client,
      isolationDomainId,
      serviceId,
      {
        ...request,
        requiredCapabilities: [" tool ", "usage"],
        runtimeProfile: " reference/v1 ",
      },
      "revision:create0001",
    );

    assert.equal(result.ok, true);
    assert.deepEqual(calls, [
      {
        options: {
          body: request,
          params: {
            header: { "Idempotency-Key": "revision:create0001" },
            path: { isolationDomainId, serviceId },
          },
        },
        path: "/v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/revisions",
      },
    ]);
    if (result.ok) {
      assert.equal("futureResponseField" in result.revision, false);
      assert.equal("labels" in result.revision.metadata, false);
      assert.deepEqual(result.revision.inputSchema, request.inputSchema);
    }
  });

  it("rejects invalid scope, definition, and identifiers before transport", async () => {
    let requested = false;
    const client = {
      POST: async () => {
        requested = true;
        return { data: revision, response: new Response(null, { status: 201 }) };
      },
    } as unknown as DataGroundClient;

    const attempts: Array<[string, string, typeof request, string]> = [
      ["native-domain", serviceId, request, "revision:create0002"],
      [isolationDomainId, "native-service", request, "revision:create0003"],
      [isolationDomainId, serviceId, { ...request, runtimeProfile: "   " }, "revision:create0004"],
      [
        isolationDomainId,
        serviceId,
        { ...request, requiredCapabilities: ["tool", "tool"] },
        "revision:create0005",
      ],
      [isolationDomainId, serviceId, request, "short"],
    ];
    for (const [domain, service, definition, key] of attempts) {
      assert.equal(
        (await createServiceRevision(client, domain, service, definition, key)).ok,
        false,
      );
    }
    assert.equal(requested, false);
  });

  it("rejects substituted and non-draft responses", async () => {
    for (const responseRevision of [
      {
        ...revision,
        metadata: { ...revision.metadata, isolationDomainId: "iso_00000000000000000002" },
      },
      { ...revision, serviceId: "svc_00000000000000000002" },
      { ...revision, runtimeProfile: "substituted/v1" },
      { ...revision, requiredCapabilities: ["usage", "tool"] },
      { ...revision, inputSchema: { type: "array" } },
      { ...revision, state: "published", publishedAt: "2026-08-14T16:01:00Z" },
    ]) {
      const result = await createServiceRevision(
        {
          POST: async () => ({
            data: responseRevision,
            response: new Response(null, { status: 201 }),
          }),
        } as unknown as DataGroundClient,
        isolationDomainId,
        serviceId,
        request,
        "revision:create0007",
      );

      assert.equal(result.ok, false);
      if (!result.ok) assert.equal(result.error.code, "WORKBENCH_REVISION_SCOPE_MISMATCH");
    }
  });

  it("classifies thrown transport as an uncertain retryable outcome", async () => {
    const result = await createServiceRevision(
      {
        POST: async () => {
          throw new Error("secret upstream diagnostic");
        },
      } as unknown as DataGroundClient,
      isolationDomainId,
      serviceId,
      request,
      "revision:create0008",
    );

    assert.equal(result.ok, false);
    if (!result.ok) {
      assert.equal(result.error.code, "WORKBENCH_REVISION_CREATION_UNCONFIRMED");
      assert.equal(result.error.outcomeUnknown, true);
      assert.equal(result.error.retryable, true);
      assert.doesNotMatch(result.error.message, /secret|upstream/u);
    }
  });
});
