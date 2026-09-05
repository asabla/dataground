import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import type { PublishedServiceRevisionResource } from "../revisions/publicationClient";
import { assignServiceAlias, readServiceAlias, type ServiceAliasResource } from "./aliasClient";

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

const readScope = {
  isolationDomainId: revision.metadata.isolationDomainId,
  serviceId: revision.serviceId,
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
  it("reads the exact alias without requiring it to target the newest publication", async () => {
    const calls: Array<{ options: unknown; path: string }> = [];
    const result = await readServiceAlias(
      {
        GET: async (path: string, options: unknown) => {
          calls.push({ options, path });
          return { data: current, response: new Response(null, { status: 200 }) };
        },
      } as unknown as DataGroundClient,
      readScope,
      "stable",
    );

    assert.equal(result.ok, true);
    assert.deepEqual(calls, [
      {
        options: {
          params: {
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
      assert.equal(result.alias?.revisionId, current.revisionId);
      assert.equal(result.alias?.metadata.version, current.metadata.version);
    }
  });

  it("treats only the stable alias-not-found response as observed absence", async () => {
    const missing = await readServiceAlias(
      {
        GET: async () => ({
          error: {
            error: {
              code: "SERVICE_ALIAS_NOT_FOUND",
              correlationId: "cor_alias_read_0001",
              message: "Service alias was not found.",
              retryable: false,
            },
          },
          response: new Response(null, { status: 404 }),
        }),
      } as unknown as DataGroundClient,
      readScope,
      "stable",
    );
    assert.deepEqual(missing, { ok: true });

    const missingService = await readServiceAlias(
      {
        GET: async () => ({
          error: {
            error: {
              code: "RESOURCE_NOT_FOUND",
              correlationId: "cor_alias_read_0002",
              message: "Agent service was not found.",
              retryable: false,
            },
          },
          response: new Response(null, { status: 404 }),
        }),
      } as unknown as DataGroundClient,
      readScope,
      "stable",
    );
    assert.equal(missingService.ok, false);
    if (!missingService.ok) assert.equal(missingService.error.code, "RESOURCE_NOT_FOUND");
  });

  it("rejects invalid and substituted alias reads safely", async () => {
    let requested = false;
    const client = {
      GET: async () => {
        requested = true;
        return { data: current, response: new Response(null, { status: 200 }) };
      },
    } as unknown as DataGroundClient;
    assert.equal((await readServiceAlias(client, readScope, "Stable")).ok, false);
    assert.equal(
      (await readServiceAlias(client, { ...readScope, serviceId: "native-service" }, "stable")).ok,
      false,
    );
    assert.equal(requested, false);

    for (const responseAlias of [
      { ...createdAlias, withdrawnAt: "2026-09-05T12:00:00Z" },
      { ...current, serviceId: "svc_00000000000000000002" },
      { ...current, name: "candidate" },
      { ...current, revisionId: "native-revision" },
      {
        ...current,
        metadata: {
          ...current.metadata,
          isolationDomainId: "iso_00000000000000000002",
        },
      },
    ]) {
      const result = await readServiceAlias(
        {
          GET: async () => ({
            data: responseAlias,
            response: new Response(null, { status: 200 }),
          }),
        } as unknown as DataGroundClient,
        readScope,
        "stable",
      );
      assert.equal(result.ok, false);
      if (!result.ok) assert.equal(result.error.code, "WORKBENCH_ALIAS_READ_SCOPE_MISMATCH");
    }
  });

  it("reports alias-read transport failure without upstream detail", async () => {
    const result = await readServiceAlias(
      {
        GET: async () => {
          throw new Error("secret upstream diagnostic");
        },
      } as unknown as DataGroundClient,
      readScope,
      "stable",
    );
    assert.equal(result.ok, false);
    if (!result.ok) {
      assert.equal(result.error.code, "WORKBENCH_ALIAS_READ_UNAVAILABLE");
      assert.equal(result.error.retryable, true);
      assert.doesNotMatch(result.error.message, /secret|upstream/u);
    }
  });

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

  it("accepts an absent alias recreated with retained identity and a later version", async () => {
    const result = await assignServiceAlias(
      {
        PUT: async () => ({
          data: {
            ...createdAlias,
            metadata: { ...createdAlias.metadata, generation: 3, version: 3 },
          },
          response: new Response(null, { status: 200 }),
        }),
      } as unknown as DataGroundClient,
      revision,
      "stable",
      undefined,
      "alias:recreated0001",
    );
    assert.equal(result.ok, true);
    if (result.ok) assert.equal(result.alias.metadata.version, 3);
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
