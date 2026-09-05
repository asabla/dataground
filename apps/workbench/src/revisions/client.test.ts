import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { createServiceRevision, listServiceRevisions, readServiceRevision } from "./client";

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
const publishedRevision = {
  ...revision,
  metadata: {
    ...revision.metadata,
    generation: 2,
    id: "rev_00000000000000000002",
    updatedAt: "2026-08-14T16:02:00Z",
    version: 2,
  },
  publishedAt: "2026-08-14T16:02:00Z",
  revisionNumber: 2,
  state: "published",
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

  it("lists newest authoritative revisions and strips unknown fields", async () => {
    const calls: Array<{ options: unknown; path: string }> = [];
    const client = {
      GET: async (path: string, options: unknown) => {
        calls.push({ options, path });
        return {
          data: {
            futurePageField: "strip",
            items: [publishedRevision, revision],
            nextCursor: "eyJyZXZpc2lvbk51bWJlciI6MSwiaWQiOiJyZXZfMDAwMDAwMDAwMDAwMDAwMDAwMDEifQ",
          },
          response: new Response(null, { status: 200 }),
        };
      },
    } as unknown as DataGroundClient;

    const result = await listServiceRevisions(client, isolationDomainId, serviceId);

    assert.equal(result.ok, true);
    assert.deepEqual(calls, [
      {
        options: {
          params: {
            path: { isolationDomainId, serviceId },
            query: { limit: 50 },
          },
        },
        path: "/v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/revisions",
      },
    ]);
    if (result.ok) {
      const first = result.page.items[0];
      assert.ok(first);
      assert.deepEqual(
        result.page.items.map((item) => [item.revisionNumber, item.state]),
        [
          [2, "published"],
          [1, "draft"],
        ],
      );
      assert.equal("futureResponseField" in first, false);
      assert.equal("labels" in first.metadata, false);
    }
  });

  it("rejects malformed, cross-scope, duplicate, and unordered revision pages", async () => {
    const malformedPages = [
      { items: [{ ...publishedRevision, serviceId: "svc_00000000000000000002" }] },
      {
        items: [
          publishedRevision,
          { ...revision, metadata: { ...revision.metadata, id: publishedRevision.metadata.id } },
        ],
      },
      { items: [revision, publishedRevision] },
      { items: [{ ...publishedRevision, publishedAt: undefined }] },
      { items: [], nextCursor: "next" },
    ];
    for (const page of malformedPages) {
      const result = await listServiceRevisions(
        {
          GET: async () => ({ data: page, response: new Response(null, { status: 200 }) }),
        } as unknown as DataGroundClient,
        isolationDomainId,
        serviceId,
      );
      assert.equal(result.ok, false);
      if (!result.ok) {
        assert.equal(result.error.code, "WORKBENCH_REVISION_LIST_SCOPE_MISMATCH");
      }
    }
  });

  it("rejects invalid list inputs and a stalled cursor", async () => {
    let requested = false;
    const client = {
      GET: async () => {
        requested = true;
        return { data: { items: [] }, response: new Response(null, { status: 200 }) };
      },
    } as unknown as DataGroundClient;
    for (const [domain, service, cursor] of [
      ["native-domain", serviceId, undefined],
      [isolationDomainId, "native-service", undefined],
      [isolationDomainId, serviceId, "not+a+cursor"],
    ] as const) {
      assert.equal((await listServiceRevisions(client, domain, service, cursor)).ok, false);
    }
    assert.equal(requested, false);

    const cursor = "eyJyZXZpc2lvbk51bWJlciI6MiwiaWQiOiJyZXZfMDAwMDAwMDAwMDAwMDAwMDAwMDIifQ";
    const stalled = await listServiceRevisions(
      {
        GET: async () => ({
          data: { items: [publishedRevision], nextCursor: cursor },
          response: new Response(null, { status: 200 }),
        }),
      } as unknown as DataGroundClient,
      isolationDomainId,
      serviceId,
      cursor,
    );
    assert.equal(stalled.ok, false);
    if (!stalled.ok) assert.equal(stalled.error.code, "WORKBENCH_REVISION_LIST_CURSOR_STALLED");
  });
});

describe("exact revision client", () => {
  const scope = { isolationDomainId, serviceId, revisionId: publishedRevision.metadata.id };
  it("reads an exact scoped revision and strips unknown fields", async () => {
    const calls: unknown[] = [];
    const result = await readServiceRevision(
      {
        GET: async (path: string, options: unknown) => {
          calls.push({ path, options });
          return { data: publishedRevision, response: new Response(null, { status: 200 }) };
        },
      } as unknown as DataGroundClient,
      scope,
    );
    assert.deepEqual(calls, [
      {
        path: "/v1/isolation-domains/{isolationDomainId}/service-revisions/{revisionId}",
        options: { params: { path: { isolationDomainId, revisionId: scope.revisionId } } },
      },
    ]);
    assert.equal(result.ok, true);
    if (result.ok) {
      assert.equal(result.revision.metadata.id, scope.revisionId);
      assert.equal(result.revision.state, "published");
      assert.ok(!JSON.stringify(result.revision).includes("must be stripped"));
    }
  });
  it("rejects substituted identifiers, scopes, malformed state and successful non-200 statuses", async () => {
    for (const data of [
      revision,
      { ...publishedRevision, serviceId: "svc_00000000000000000002" },
      {
        ...publishedRevision,
        metadata: { ...publishedRevision.metadata, isolationDomainId: "iso_00000000000000000002" },
      },
      { ...publishedRevision, publishedAt: undefined },
      { ...publishedRevision, state: "unknown" },
      undefined,
    ]) {
      const result = await readServiceRevision(
        {
          GET: async () => ({ data, response: new Response(null, { status: 200 }) }),
        } as unknown as DataGroundClient,
        scope,
      );
      assert.equal(result.ok, false);
    }
    const result = await readServiceRevision(
      {
        GET: async () => ({
          data: publishedRevision,
          response: new Response(null, { status: 202 }),
        }),
      } as unknown as DataGroundClient,
      scope,
    );
    assert.equal(result.ok, false);
  });
  it("does not send invalid scope and returns safe denial and network failures", async () => {
    let called = false;
    const offline = {
      GET: async () => {
        called = true;
        throw new Error("private provider details");
      },
    } as unknown as DataGroundClient;
    for (const changed of [
      { ...scope, isolationDomainId: "bad" },
      { ...scope, serviceId: "bad" },
      { ...scope, revisionId: "bad" },
    ])
      assert.equal((await readServiceRevision(offline, changed)).ok, false);
    assert.equal(called, false);
    const failed = await readServiceRevision(offline, scope);
    assert.equal(failed.ok, false);
    if (!failed.ok) {
      assert.equal(failed.error.retryable, true);
      assert.ok(!failed.error.message.includes("private"));
    }
    const denied = await readServiceRevision(
      {
        GET: async () => ({
          response: new Response(null, { status: 403 }),
          error: {
            error: {
              code: "ACCESS_DENIED",
              message: "Access denied.",
              correlationId: "cor_denied",
              retryable: false,
            },
          },
        }),
      } as unknown as DataGroundClient,
      scope,
    );
    assert.equal(denied.ok, false);
    if (!denied.ok) {
      assert.equal(denied.error.status, 403);
      assert.equal(denied.error.retryable, false);
    }
  });
});
