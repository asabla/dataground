import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import type { PublishedServiceRevisionResource } from "../revisions/publicationClient";
import { assignServiceAlias, type ServiceAliasResource } from "./aliasClient";

const revision: PublishedServiceRevisionResource = {
  metadata: {
    createdAt: "2026-08-14T16:00:00Z",
    createdBy: "reference-runtime",
    generation: 1,
    id: "rev_00000000000000000001",
    isolationDomainId: "iso_00000000000000000001",
    updatedAt: "2026-08-14T16:01:00Z",
    version: 2,
  },
  publishedAt: "2026-08-14T16:01:00Z",
  requiredCapabilities: ["tool", "usage"],
  revisionNumber: 2,
  runtimeProfile: "reference/v1",
  serviceId: "svc_00000000000000000001",
  state: "published",
};

const createdAlias = {
  futureResponseField: "must be stripped",
  metadata: {
    createdAt: "2026-08-14T16:02:00Z",
    createdBy: "reference-runtime",
    generation: 1,
    id: "als_00000000000000000001",
    isolationDomainId: revision.metadata.isolationDomainId,
    labels: { native: "must be stripped" },
    updatedAt: "2026-08-14T16:02:00Z",
    version: 1,
  },
  name: "stable",
  revisionId: revision.metadata.id,
  serviceId: revision.serviceId,
};

const current: ServiceAliasResource = {
  metadata: {
    createdAt: "2026-08-14T15:00:00Z",
    createdBy: "reference-runtime",
    generation: 4,
    id: "als_00000000000000000002",
    isolationDomainId: revision.metadata.isolationDomainId,
    updatedAt: "2026-08-14T15:30:00Z",
    version: 4,
  },
  name: "stable",
  revisionId: "rev_00000000000000000002",
  serviceId: revision.serviceId,
};

describe("service alias client", () => {
  it("creates a new alias with an explicit zero-version precondition", async () => {
    const calls: Array<{ options: unknown; path: string }> = [];
    const client = {
      PUT: async (path: string, options: unknown) => {
        calls.push({ options, path });
        return {
          data: createdAlias,
          response: new Response(null, { status: 200 }),
        };
      },
    } as unknown as DataGroundClient;

    const result = await assignServiceAlias(
      client,
      revision,
      "stable",
      undefined,
      "alias:request0001",
    );

    assert.equal(result.ok, true);
    assert.deepEqual(calls, [
      {
        options: {
          body: { expectedVersion: 0, revisionId: revision.metadata.id },
          params: {
            header: { "Idempotency-Key": "alias:request0001" },
            path: {
              alias: "stable",
              isolationDomainId: revision.metadata.isolationDomainId,
              serviceId: revision.serviceId,
            },
          },
        },
        path: "/v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/aliases/{alias}",
      },
    ]);
    if (result.ok) {
      assert.equal("futureResponseField" in result.alias, false);
      assert.equal("labels" in result.alias.metadata, false);
      assert.equal(result.alias.metadata.version, 1);
    }
  });

  it("moves an observed alias using its exact optimistic version", async () => {
    const moved = {
      ...createdAlias,
      metadata: {
        ...current.metadata,
        generation: 5,
        updatedAt: "2026-08-14T16:02:00Z",
        version: 5,
      },
    };
    let body: unknown;
    const result = await assignServiceAlias(
      {
        PUT: async (_path: string, options: { body: unknown }) => {
          body = options.body;
          return { data: moved, response: new Response(null, { status: 200 }) };
        },
      } as unknown as DataGroundClient,
      revision,
      "stable",
      current,
      "alias:request0002",
    );

    assert.equal(result.ok, true);
    assert.deepEqual(body, {
      expectedVersion: 4,
      revisionId: revision.metadata.id,
    });
    if (result.ok) {
      assert.equal(result.alias.metadata.id, current.metadata.id);
      assert.equal(result.alias.metadata.generation, 5);
      assert.equal(result.alias.metadata.version, 5);
      assert.equal(result.alias.revisionId, revision.metadata.id);
    }
  });

  it("rejects invalid scope and stale alias snapshots before transport", async () => {
    let requested = false;
    const client = {
      PUT: async () => {
        requested = true;
        return {
          data: createdAlias,
          response: new Response(null, { status: 200 }),
        };
      },
    } as unknown as DataGroundClient;
    const attempts: Array<
      [PublishedServiceRevisionResource, string, ServiceAliasResource | undefined, string]
    > = [
      [{ ...revision, serviceId: "native-service" }, "stable", undefined, "alias:request0003"],
      [
        {
          ...revision,
          metadata: { ...revision.metadata, id: "native-revision" },
        },
        "stable",
        undefined,
        "alias:request0004",
      ],
      [revision, "Stable", undefined, "alias:request0005"],
      [
        revision,
        "stable",
        { ...current, serviceId: "svc_00000000000000000002" },
        "alias:request0006",
      ],
      [revision, "stable", { ...current, name: "candidate" }, "alias:request0007"],
      [revision, "stable", { ...current, revisionId: revision.metadata.id }, "alias:request0010"],
      [
        { ...revision, requiredCapabilities: ["tool", "tool"] },
        "stable",
        undefined,
        "alias:request0011",
      ],
      [revision, "stable", undefined, "short"],
    ];
    for (const [target, name, observed, key] of attempts) {
      assert.equal((await assignServiceAlias(client, target, name, observed, key)).ok, false);
    }
    assert.equal(requested, false);
  });

  it("rejects substituted or impossible alias responses", async () => {
    for (const responseAlias of [
      { ...createdAlias, serviceId: "svc_00000000000000000002" },
      { ...createdAlias, name: "candidate" },
      { ...createdAlias, revisionId: "rev_00000000000000000002" },
      {
        ...createdAlias,
        metadata: {
          ...createdAlias.metadata,
          isolationDomainId: "iso_00000000000000000002",
        },
      },
      {
        ...createdAlias,
        metadata: { ...createdAlias.metadata, generation: 2 },
      },
      { ...createdAlias, metadata: { ...createdAlias.metadata, version: 2 } },
    ]) {
      const result = await assignServiceAlias(
        {
          PUT: async () => ({
            data: responseAlias,
            response: new Response(null, { status: 200 }),
          }),
        } as unknown as DataGroundClient,
        revision,
        "stable",
        undefined,
        "alias:request0008",
      );
      assert.equal(result.ok, false);
      if (!result.ok) assert.equal(result.error.code, "WORKBENCH_ALIAS_SCOPE_MISMATCH");
    }
  });

  it("classifies thrown transport as an uncertain retryable outcome", async () => {
    const result = await assignServiceAlias(
      {
        PUT: async () => {
          throw new Error("secret upstream diagnostic");
        },
      } as unknown as DataGroundClient,
      revision,
      "stable",
      current,
      "alias:request0009",
    );

    assert.equal(result.ok, false);
    if (!result.ok) {
      assert.equal(result.error.code, "WORKBENCH_ALIAS_ASSIGNMENT_UNCONFIRMED");
      assert.equal(result.error.outcomeUnknown, true);
      assert.equal(result.error.retryable, true);
      assert.doesNotMatch(result.error.message, /secret|upstream/u);
    }
  });
});
