import { expect, it, vi } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { listSavedResources, type SavedResourceRequest, setSavedResource } from "./client";

const scope = "iso_00000000000000000001",
  id = "wsp_00000000000000000001";
const resource = {
  kind: "workspace",
  name: "Saved research",
  metadata: { id, isolationDomainId: scope, createdAt: "2026-10-29T08:00:00Z" },
};
const saved = {
  kind: "workspace",
  resourceId: id,
  isolationDomainId: scope,
  savedAt: "2026-10-29T10:00:00.000002Z",
  resource,
};
const request: SavedResourceRequest = { kind: "workspace", resourceId: id, saved: true };
const receipt = { ...request, isolationDomainId: scope, updatedAt: "2026-10-29T10:00:00Z" };
const api = (data: unknown, status = 200) =>
  ({
    GET: vi.fn(async () => ({ data, response: new Response(null, { status }) })),
    POST: vi.fn(async () => ({ data, response: new Response(null, { status }) })),
  }) as unknown as DataGroundClient;

it("reads a bounded private collection and strips content and owner fields", async () => {
  const client = api({
    items: [
      {
        ...saved,
        principalId: "private-owner",
        contents: "private-content",
        resource: { ...resource, input: "private-input" },
      },
    ],
    nextCursor: "next",
  });
  const result = await listSavedResources(client, scope);
  expect(result).toEqual({
    ok: true,
    page: { items: [{ ...saved, metadata: { id } }], nextCursor: "next" },
  });
  expect(client.GET).toHaveBeenCalledExactlyOnceWith(
    "/v1/isolation-domains/{isolationDomainId}/saved-resources",
    { params: { path: { isolationDomainId: scope }, query: { limit: 50 } } },
  );
  expect(
    (await listSavedResources(api({ items: [{ ...saved, resource: undefined }] }), scope)).ok,
  ).toBe(true);
});
it("rejects substituted targets, cross-scope metadata, duplicate rows and stalled pages", async () => {
  for (const items of [
    [{ ...saved, isolationDomainId: "iso_00000000000000000002" }],
    [{ ...saved, kind: "artifact" }],
    [
      {
        ...saved,
        resource: {
          ...resource,
          metadata: { ...resource.metadata, id: "wsp_00000000000000000002" },
        },
      },
    ],
    [
      {
        ...saved,
        resource: {
          ...resource,
          metadata: { ...resource.metadata, isolationDomainId: "iso_00000000000000000002" },
        },
      },
    ],
    [{ ...saved, resource: null }],
    [{ ...saved, savedAt: "invalid" }],
    [saved, saved],
    Array.from({ length: 51 }, () => saved),
  ])
    expect((await listSavedResources(api({ items }), scope)).ok).toBe(false);
  for (const value of [
    { items: [saved], nextCursor: "current" },
    { items: [], nextCursor: "next" },
    { items: [saved], nextCursor: "!" },
  ])
    expect((await listSavedResources(api(value), scope, "current")).ok).toBe(false);
  const older = {
    ...saved,
    resource: undefined,
    resourceId: "wsp_00000000000000000002",
    savedAt: "2026-10-29T10:00:00.000001Z",
  };
  expect((await listSavedResources(api({ items: [saved, older] }), scope)).ok).toBe(true);
  expect((await listSavedResources(api({ items: [older, saved] }), scope)).ok).toBe(false);
});
it("sends an exact bounded save command and accepts only its matching receipt", async () => {
  const client = api({ ...receipt, principalId: "private-owner" });
  expect(await setSavedResource(client, scope, request, "saved-exact-request")).toEqual({
    ok: true,
    state: receipt,
  });
  expect(client.POST).toHaveBeenCalledExactlyOnceWith(
    "/v1/isolation-domains/{isolationDomainId}/saved-resources",
    {
      params: {
        path: { isolationDomainId: scope },
        header: { "Idempotency-Key": "saved-exact-request" },
      },
      body: request,
    },
  );
  for (const value of [
    { ...receipt, saved: false },
    { ...receipt, resourceId: "wsp_00000000000000000002" },
    { ...receipt, isolationDomainId: "iso_00000000000000000002" },
    { ...receipt, updatedAt: "invalid" },
  ]) {
    const result = await setSavedResource(api(value), scope, request, "saved-exact-request");
    expect(result).toMatchObject({ ok: false, error: { outcomeUnknown: true } });
  }
});
it("keeps ambiguous transport failures distinct from an authorized denial", async () => {
  const broken = {
    POST: vi.fn(async () => {
      throw new Error("private response");
    }),
  } as unknown as DataGroundClient;
  expect(await setSavedResource(broken, scope, request, "saved-exact-request")).toMatchObject({
    ok: false,
    error: { outcomeUnknown: true, retryable: true },
  });
  const error = {
    error: {
      code: "ACCESS_DENIED",
      message: "Access denied.",
      correlationId: "cor_test",
      retryable: false,
    },
  };
  for (const status of [403, 503]) {
    const client = {
      POST: async () => ({ error, response: new Response(null, { status }) }),
    } as unknown as DataGroundClient;
    const result = await setSavedResource(client, scope, request, "saved-exact-request");
    expect(result.ok).toBe(false);
    if (!result.ok) expect(!!result.error.outcomeUnknown).toBe(status === 503);
  }
});
it("rejects malformed scopes and references before transport", async () => {
  const client = api(receipt);
  await listSavedResources(client, "bad");
  await listSavedResources(client, scope, "!");
  await setSavedResource(
    client,
    scope,
    { ...request, resourceId: "svc_00000000000000000001" },
    "saved-exact-request",
  );
  await setSavedResource(client, scope, request, "!");
  expect(client.GET).not.toHaveBeenCalled();
  expect(client.POST).not.toHaveBeenCalled();
});
