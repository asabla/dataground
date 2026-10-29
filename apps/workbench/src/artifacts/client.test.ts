import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { listInvocationArtifacts, listScopeArtifacts, readInvocationArtifact } from "./client";

const reference = {
  artifactId: "art_00000000000000000001",
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};

const artifact = {
  digest: "sha256:1b93a4b13f9917ba7e33ebf29560b17d50593f23bc1dfeeec961ae0cfabcb9e6",
  invocationId: reference.invocationId,
  kind: "structured-output" as const,
  mediaType: "application/json",
  metadata: {
    createdAt: "2026-08-14T12:00:00Z",
    createdBy: "reference-runtime",
    generation: 1,
    id: reference.artifactId,
    isolationDomainId: reference.isolationDomainId,
    provenance: {
      requestCorrelationId: "cor_00000000000000000001",
      sourceRevision: "rev_00000000000000000001",
    },
    updatedAt: "2026-08-14T12:00:00.001Z",
    version: 1,
  },
  name: "result.json",
  sensitive: true,
  sizeBytes: 2_097_152,
  state: "available" as const,
};

describe("invocation artifact client", () => {
  it("binds an authoritative metadata read to the complete public path", async () => {
    let options: unknown;
    const client = {
      GET: async (_path: string, value: unknown) => {
        options = value;
        return { data: artifact, response: new Response(null, { status: 200 }) };
      },
    } as unknown as DataGroundClient;

    const result = await readInvocationArtifact(client, reference);

    assert.equal(result.ok, true);
    assert.deepEqual(options, { params: { path: reference } });
  });

  it("preserves bounded future state and kind values for explicit presentation", async () => {
    const client = {
      GET: async () => ({
        data: { ...artifact, kind: "future-kind", state: "quarantined" },
        response: new Response(null, { status: 200 }),
      }),
    } as unknown as DataGroundClient;

    const result = await readInvocationArtifact(client, reference);

    assert.equal(result.ok, true);
    if (result.ok) {
      assert.equal(result.artifact.kind, "future-kind");
      assert.equal(result.artifact.state, "quarantined");
    }
  });

  it("rejects cross-scope and malformed successful responses", async () => {
    for (const data of [
      { ...artifact, invocationId: "inv_00000000000000000002" },
      { ...artifact, digest: "sha256:invalid" },
      { ...artifact, sizeBytes: Number.MAX_SAFE_INTEGER + 1 },
      { ...artifact, metadata: { ...artifact.metadata, provenance: { nativeEndpoint: "secret" } } },
    ]) {
      const client = {
        GET: async () => ({ data, response: new Response(null, { status: 200 }) }),
      } as unknown as DataGroundClient;
      const result = await readInvocationArtifact(client, reference);

      assert.equal(result.ok, false);
      if (!result.ok) {
        assert.equal(result.error.code, "WORKBENCH_ARTIFACT_SCOPE_MISMATCH");
      }
    }
  });

  it("fails closed before transport for invalid references", async () => {
    let requested = false;
    const client = {
      GET: async () => {
        requested = true;
        return { data: artifact, response: new Response(null, { status: 200 }) };
      },
    } as unknown as DataGroundClient;

    const result = await readInvocationArtifact(client, { ...reference, artifactId: "native-id" });

    assert.equal(result.ok, false);
    assert.equal(requested, false);
    if (!result.ok) {
      assert.equal(result.error.code, "WORKBENCH_INVALID_REFERENCE");
    }
  });

  it("preserves safe API failures and hides transport details", async () => {
    const apiClient = {
      GET: async () => ({
        error: {
          error: {
            code: "RESOURCE_NOT_FOUND",
            correlationId: "cor_00000000000000000002",
            message: "Invocation artifact was not found.",
            retryable: false,
          },
        },
        response: new Response(null, { status: 404 }),
      }),
    } as unknown as DataGroundClient;
    const apiResult = await readInvocationArtifact(apiClient, reference);
    assert.equal(apiResult.ok, false);
    if (!apiResult.ok) {
      assert.equal(apiResult.error.code, "RESOURCE_NOT_FOUND");
      assert.equal(apiResult.error.status, 404);
    }

    const malformedClient = {
      GET: async () => ({
        error: {
          error: {
            code: "UPSTREAM_SECRET",
            correlationId: "cor_00000000000000000002",
            message: "s".repeat(513),
            retryable: false,
          },
        },
        response: new Response(null, { status: 503 }),
      }),
    } as unknown as DataGroundClient;
    const malformedResult = await readInvocationArtifact(malformedClient, reference);
    assert.equal(malformedResult.ok, false);
    if (!malformedResult.ok) {
      assert.equal(malformedResult.error.code, "WORKBENCH_INVALID_RESPONSE");
      assert.doesNotMatch(malformedResult.error.message, /ssssssss/u);
    }

    const transportClient = {
      GET: async () => {
        throw new Error("secret upstream object-store details");
      },
    } as unknown as DataGroundClient;
    const transportResult = await readInvocationArtifact(transportClient, reference);
    assert.equal(transportResult.ok, false);
    if (!transportResult.ok) {
      assert.equal(transportResult.error.code, "WORKBENCH_NETWORK_UNAVAILABLE");
      assert.doesNotMatch(transportResult.error.message, /object-store/u);
    }
  });
});

describe("invocation artifact discovery client", () => {
  const listReference = {
    isolationDomainId: reference.isolationDomainId,
    invocationId: reference.invocationId,
  };
  it("requests bounded metadata without requesting content", async () => {
    const calls: unknown[] = [];
    const client = {
      GET: async (path: string, options: unknown) => {
        calls.push({ path, options });
        return {
          data: { items: [{ ...artifact, content: "must not escape" }], nextCursor: "next-1" },
          response: new Response(null, { status: 200 }),
        };
      },
    } as unknown as DataGroundClient;
    const result = await listInvocationArtifacts(client, listReference, "prior-1");
    assert.deepEqual(calls, [
      {
        path: "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/artifacts",
        options: { params: { path: listReference, query: { limit: 50, cursor: "prior-1" } } },
      },
    ]);
    assert.equal(result.ok, true);
    if (result.ok) {
      assert.equal(result.page.items[0]?.name, artifact.name);
      assert.equal("content" in (result.page.items[0] ?? {}), false);
    }
  });
  it("rejects malformed, unbounded, duplicate and cross-scope pages", async () => {
    for (const data of [
      { items: [artifact, artifact] },
      { items: Array(51).fill(artifact) },
      { items: [{ ...artifact, invocationId: "inv_00000000000000000002" }] },
      {
        items: [
          {
            ...artifact,
            metadata: { ...artifact.metadata, isolationDomainId: "iso_00000000000000000002" },
          },
        ],
      },
      { items: [artifact], nextCursor: "prior-1" },
      { items: [], nextCursor: "next-1" },
      { items: [artifact], nextCursor: "invalid cursor" },
      { items: [null] },
    ]) {
      const client = {
        GET: async () => ({ data, response: new Response(null, { status: 200 }) }),
      } as unknown as DataGroundClient;
      const result = await listInvocationArtifacts(client, listReference, "prior-1");
      assert.equal(result.ok, false);
      if (!result.ok) assert.equal(result.error.code, "WORKBENCH_ARTIFACT_PAGE_INVALID");
    }
  });
  it("rejects invalid references before transport and preserves safe failures", async () => {
    let reads = 0;
    const client = {
      GET: async () => {
        reads++;
        throw new Error("secret transport detail");
      },
    } as unknown as DataGroundClient;
    for (const [scope, cursor] of [
      [{ ...listReference, invocationId: "bad" }, undefined],
      [listReference, "bad cursor"],
    ] as const)
      assert.equal((await listInvocationArtifacts(client, scope, cursor)).ok, false);
    assert.equal(reads, 0);
    const result = await listInvocationArtifacts(client, listReference);
    assert.equal(result.ok, false);
    if (!result.ok) {
      assert.equal(result.error.retryable, true);
      assert.doesNotMatch(result.error.message, /secret/u);
    }
    const denied = await listInvocationArtifacts(
      {
        GET: async () => ({
          error: { error: { code: "FORBIDDEN", message: "Access denied.", retryable: false } },
          response: new Response(null, { status: 403 }),
        }),
      } as unknown as DataGroundClient,
      listReference,
    );
    assert.equal(denied.ok, false);
    if (!denied.ok) assert.equal(denied.error.status, 403);
  });
});

describe("scope artifact discovery", () => {
  const item = {
    ...artifact,
    serviceId: "svc_00000000000000000001",
    serviceName: "Analysis assistant",
    revisionId: "rev_00000000000000000001",
  };
  it("uses one bounded query and excludes storage and content fields", async () => {
    const calls: unknown[] = [];
    const client = {
      GET: async (path: string, options: unknown) => {
        calls.push({ path, options });
        return {
          data: { items: [{ ...item, storageKey: "private", content: "private" }] },
          response: new Response(null, { status: 200 }),
        };
      },
    } as unknown as DataGroundClient;
    const result = await listScopeArtifacts(client, reference.isolationDomainId, "available");
    assert.equal(result.ok, true);
    assert.deepEqual(calls, [
      {
        path: "/v1/isolation-domains/{isolationDomainId}/artifacts",
        options: {
          params: {
            path: { isolationDomainId: reference.isolationDomainId },
            query: { limit: 50, state: "available" },
          },
        },
      },
    ]);
    if (result.ok) {
      assert.ok(result.page.items[0]);
      assert.equal(result.page.items[0].serviceId, item.serviceId);
      assert.equal("storageKey" in result.page.items[0], false);
      assert.equal("content" in result.page.items[0], false);
    }
  });
  it("rejects foreign scope, duplicate items, invalid provenance and stalled pages", async () => {
    for (const page of [
      {
        items: [
          {
            ...item,
            metadata: { ...item.metadata, isolationDomainId: "iso_00000000000000000002" },
          },
        ],
      },
      { items: [item, item] },
      { items: [{ ...item, invocationId: "bad" }] },
      { items: [{ ...item, serviceId: "bad" }] },
      { items: [{ ...item, revisionId: "bad" }] },
      { items: [{ ...item, serviceName: "" }] },
      { items: [{ ...item, state: "failed" }] },
      { items: [item], nextCursor: "page-1" },
      { items: [], nextCursor: "page-2" },
    ]) {
      const client = {
        GET: async () => ({ data: page, response: new Response(null, { status: 200 }) }),
      } as unknown as DataGroundClient;
      assert.equal(
        (await listScopeArtifacts(client, reference.isolationDomainId, "available", "page-1")).ok,
        false,
      );
    }
  });
  it("preserves bounded future states when no state filter is requested", async () => {
    const client = {
      GET: async () => ({
        data: { items: [{ ...item, state: "quarantined" }] },
        response: new Response(null, { status: 200 }),
      }),
    } as unknown as DataGroundClient;
    const result = await listScopeArtifacts(client, reference.isolationDomainId);
    assert.equal(result.ok, true);
    if (result.ok) assert.equal(result.page.items[0]?.state, "quarantined");
  });
});
