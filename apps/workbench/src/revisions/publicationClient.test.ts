import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import type { ServiceRevisionResource } from "./client";
import { publishServiceRevision } from "./publicationClient";

const draft: ServiceRevisionResource = {
  inputSchema: { properties: { prompt: { type: "string" } }, type: "object" },
  metadata: {
    createdAt: "2026-08-14T16:00:00Z",
    createdBy: "reference-runtime",
    generation: 1,
    id: "rev_00000000000000000001",
    isolationDomainId: "iso_00000000000000000001",
    updatedAt: "2026-08-14T16:00:00Z",
    version: 1,
  },
  requiredCapabilities: ["tool", "usage"],
  revisionNumber: 2,
  runtimeProfile: "reference/v1",
  serviceId: "svc_00000000000000000001",
  state: "draft",
};

const published = {
  futureResponseField: "must be stripped",
  inputSchema: { type: "object", properties: { prompt: { type: "string" } } },
  metadata: {
    ...draft.metadata,
    labels: { native: "must be stripped" },
    updatedAt: "2026-08-14T16:01:00Z",
    version: 2,
  },
  publishedAt: "2026-08-14T16:01:00Z",
  requiredCapabilities: ["tool", "usage"],
  revisionNumber: 2,
  runtimeProfile: "reference/v1",
  serviceId: draft.serviceId,
  state: "published",
};

describe("service revision publication client", () => {
  it("publishes the exact draft version and strips ungoverned response fields", async () => {
    const calls: Array<{ options: unknown; path: string }> = [];
    const client = {
      POST: async (path: string, options: unknown) => {
        calls.push({ options, path });
        return { data: published, response: new Response(null, { status: 200 }) };
      },
    } as unknown as DataGroundClient;

    const result = await publishServiceRevision(client, draft, "publication:request0001");

    assert.equal(result.ok, true);
    assert.deepEqual(calls, [
      {
        options: {
          body: { expectedVersion: 1 },
          params: {
            header: { "Idempotency-Key": "publication:request0001" },
            path: {
              isolationDomainId: draft.metadata.isolationDomainId,
              revisionId: draft.metadata.id,
            },
          },
        },
        path: "/v1/isolation-domains/{isolationDomainId}/service-revisions/{revisionId}/actions/publish",
      },
    ]);
    if (result.ok) {
      assert.equal("futureResponseField" in result.revision, false);
      assert.equal("labels" in result.revision.metadata, false);
      assert.equal(result.revision.state, "published");
      assert.equal(result.revision.metadata.version, 2);
    }
  });

  it("rejects invalid draft scope and identifiers before transport", async () => {
    let requested = false;
    const client = {
      POST: async () => {
        requested = true;
        return { data: published, response: new Response(null, { status: 200 }) };
      },
    } as unknown as DataGroundClient;

    const attempts: Array<[ServiceRevisionResource, string]> = [
      [{ ...draft, metadata: { ...draft.metadata, id: "native-revision" } }, "publication:0002"],
      [
        {
          ...draft,
          metadata: { ...draft.metadata, isolationDomainId: "native-domain" },
        },
        "publication:0003",
      ],
      [{ ...draft, serviceId: "native-service" }, "publication:0004"],
      [{ ...draft, requiredCapabilities: ["tool", "tool"] }, "publication:0005"],
      [{ ...draft, inputSchema: [] as unknown as Record<string, unknown> }, "publication:0006"],
      [
        {
          ...draft,
          inputSchema: { invalid: () => "not JSON" },
        },
        "publication:0007",
      ],
      [{ ...draft, runtimeProfile: " reference/v1" }, "publication:0008"],
      [draft, "short"],
    ];
    for (const [revision, key] of attempts) {
      assert.equal((await publishServiceRevision(client, revision, key)).ok, false);
    }
    assert.equal(requested, false);
  });

  it("rejects substituted or non-published responses", async () => {
    for (const responseRevision of [
      {
        ...published,
        metadata: {
          ...published.metadata,
          isolationDomainId: "iso_00000000000000000002",
        },
      },
      { ...published, serviceId: "svc_00000000000000000002" },
      { ...published, runtimeProfile: "substituted/v1" },
      { ...published, requiredCapabilities: ["usage", "tool"] },
      { ...published, state: "draft", publishedAt: undefined },
      { ...published, metadata: { ...published.metadata, version: 3 } },
      { ...published, publishedAt: "2026-08-14T15:59:00Z" },
    ]) {
      const result = await publishServiceRevision(
        {
          POST: async () => ({
            data: responseRevision,
            response: new Response(null, { status: 200 }),
          }),
        } as unknown as DataGroundClient,
        draft,
        "publication:request0006",
      );
      assert.equal(result.ok, false);
      if (!result.ok) assert.equal(result.error.code, "WORKBENCH_PUBLICATION_SCOPE_MISMATCH");
    }
  });

  it("fails closed when asynchronous operation state is not bound to the revision", async () => {
    const result = await publishServiceRevision(
      {
        POST: async () => ({
          data: {
            command: "publish",
            metadata: {
              ...draft.metadata,
              id: "op_00000000000000000001",
            },
          },
          response: new Response(null, { status: 202 }),
        }),
      } as unknown as DataGroundClient,
      draft,
      "publication:request0007",
    );

    assert.equal(result.ok, false);
    if (!result.ok) {
      assert.equal(result.error.code, "WORKBENCH_PUBLICATION_OPERATION_UNBOUND");
      assert.equal(result.error.outcomeUnknown, true);
      assert.equal(result.error.retryable, false);
    }
  });

  it("classifies thrown transport as an uncertain retryable outcome", async () => {
    const result = await publishServiceRevision(
      {
        POST: async () => {
          throw new Error("secret upstream diagnostic");
        },
      } as unknown as DataGroundClient,
      draft,
      "publication:request0008",
    );

    assert.equal(result.ok, false);
    if (!result.ok) {
      assert.equal(result.error.code, "WORKBENCH_PUBLICATION_UNCONFIRMED");
      assert.equal(result.error.outcomeUnknown, true);
      assert.equal(result.error.retryable, true);
      assert.doesNotMatch(result.error.message, /secret|upstream/u);
    }
  });
});
