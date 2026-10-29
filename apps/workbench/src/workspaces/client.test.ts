import assert from "node:assert/strict";
import { it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { createAgentService, listAgentServices, readAgentService } from "../services/client";
import { createWorkspace, listWorkspaces, readWorkspace } from "./client";

const scope = "iso_00000000000000000001",
  id = "wsp_00000000000000000001";
const workspace = {
  name: "Research",
  metadata: {
    id,
    isolationDomainId: scope,
    createdAt: "2026-10-29T10:00:00Z",
    updatedAt: "2026-10-29T10:00:00Z",
    createdBy: "fixture",
    generation: 1,
    version: 1,
  },
  extra: "strip",
};
function client(data: unknown, status = 200) {
  return {
    GET: async () => ({ data, response: new Response(null, { status }) }),
    POST: async () => ({ data, response: new Response(null, { status }) }),
  } as unknown as DataGroundClient;
}
it("validates bounded workspace pages and projects public fields", async () => {
  const calls: unknown[] = [];
  const api = {
    GET: async (path: unknown, options: unknown) => {
      calls.push({ path, options });
      return {
        data: { items: [workspace], nextCursor: "next" },
        response: new Response(null, { status: 200 }),
      };
    },
  } as unknown as DataGroundClient;
  const result = await listWorkspaces(api, scope);
  assert.equal(result.ok, true);
  if (result.ok) {
    assert.equal(result.page.items[0]?.metadata.id, id);
    assert.equal("extra" in (result.page.items[0] ?? {}), false);
  }
  assert.deepEqual(calls, [
    {
      path: "/v1/isolation-domains/{isolationDomainId}/workspaces",
      options: { params: { path: { isolationDomainId: scope }, query: { limit: 50 } } },
    },
  ]);
  for (const page of [
    { items: [workspace, workspace] },
    {
      items: [
        {
          ...workspace,
          metadata: { ...workspace.metadata, isolationDomainId: "iso_00000000000000000002" },
        },
      ],
    },
    { items: [], nextCursor: "next" },
    { items: [workspace], nextCursor: "current" },
    { items: [{ ...workspace, name: "\0" }] },
  ])
    assert.equal((await listWorkspaces(client(page), scope, "current")).ok, false);
  assert.equal(
    (await readWorkspace(client(workspace), scope, "wsp_00000000000000000002")).ok,
    false,
  );
  assert.equal((await readWorkspace(client(workspace, 201), scope, id)).ok, false);
});
it("keeps the exact workspace creation request available after an unknown outcome", async () => {
  const attempts: unknown[] = [];
  let fail = true;
  const api = {
    POST: async (path: unknown, options: unknown) => {
      attempts.push({ path, options });
      if (fail) {
        fail = false;
        throw new Error("lost acknowledgement");
      }
      return { data: workspace, response: new Response(null, { status: 201 }) };
    },
  } as unknown as DataGroundClient;
  const first = await createWorkspace(api, scope, { name: " Research " }, "workspace-exact-retry");
  assert.equal(first.ok, false);
  if (!first.ok) assert.equal(first.error.outcomeUnknown, true);
  assert.equal(
    (await createWorkspace(api, scope, { name: " Research " }, "workspace-exact-retry")).ok,
    true,
  );
  assert.deepEqual(attempts[0], attempts[1]);
  const invalid = await createWorkspace(
    client({ ...workspace, name: "Other" }, 201),
    scope,
    { name: "Research" },
    "workspace-exact-retry",
  );
  assert.equal(invalid.ok, false);
  if (!invalid.ok) assert.equal(invalid.error.outcomeUnknown, true);
});
it("rejects invalid workspace input before transport and preserves denial", async () => {
  const never = {
    GET: () => {
      throw new Error("unexpected");
    },
    POST: () => {
      throw new Error("unexpected");
    },
  } as unknown as DataGroundClient;
  assert.equal((await listWorkspaces(never, "bad")).ok, false);
  const invalid = await createWorkspace(never, scope, { name: "\0" }, "workspace-valid-key");
  assert.equal(invalid.ok, false);
  if (!invalid.ok) assert.equal(invalid.error.outcomeUnknown, undefined);
  const denied = {
    GET: async () => ({
      error: {
        error: {
          code: "ACCESS_DENIED",
          message: "Workspace discovery is denied.",
          correlationId: "cor-fixture",
          retryable: false,
        },
      },
      response: new Response(null, { status: 403 }),
    }),
  } as unknown as DataGroundClient;
  const result = await listWorkspaces(denied, scope);
  assert.equal(result.ok, false);
  if (!result.ok) assert.equal(result.error.status, 403);
});
it("binds workspace service creation and discovery without losing exact service identity", async () => {
  const service = {
    ...workspace,
    workspaceId: id,
    metadata: { ...workspace.metadata, id: "svc_00000000000000000001" },
  };
  const calls: unknown[] = [];
  const api = {
    POST: async (path: unknown, options: unknown) => {
      calls.push({ path, options });
      return { data: service, response: new Response(null, { status: 201 }) };
    },
    GET: async () => ({
      data: { items: [service] },
      response: new Response(null, { status: 200 }),
    }),
  } as unknown as DataGroundClient;
  const made = await createAgentService(
    api,
    scope,
    { name: "Research" },
    "workspace-service-key",
    id,
  );
  assert.equal(made.ok, true);
  assert.deepEqual(calls, [
    {
      path: "/v1/isolation-domains/{isolationDomainId}/workspaces/{workspaceId}/agent-services",
      options: {
        body: { name: "Research" },
        params: {
          path: { isolationDomainId: scope, workspaceId: id },
          header: { "Idempotency-Key": "workspace-service-key" },
        },
      },
    },
  ]);
  assert.equal((await listAgentServices(api, scope, undefined, id)).ok, true);
  assert.equal(
    (await listAgentServices(api, scope, undefined, "wsp_00000000000000000002")).ok,
    false,
  );
  assert.equal(
    (
      await createAgentService(
        client(service, 201),
        scope,
        { name: "Research" },
        "workspace-service-key",
      )
    ).ok,
    false,
  );
  const exact = await readAgentService(client(service), scope, service.metadata.id);
  assert.equal(exact.ok, true);
  if (exact.ok) assert.equal(exact.service.workspaceId, id);
});

it("retains exact creation recovery when success or server-error bodies cannot be verified", async () => {
  for (const status of [201, 503]) {
    const result = await createWorkspace(
      client(undefined, status),
      scope,
      { name: "Research" },
      "workspace-request-key",
    );
    assert.equal(result.ok, false);
    if (!result.ok) {
      assert.equal(result.error.outcomeUnknown, true);
      assert.equal(result.error.retryable, true);
    }
    const serviceResult = await createAgentService(
      client(undefined, status),
      scope,
      { name: "Research" },
      "workspace-service-key",
      id,
    );
    assert.equal(serviceResult.ok, false);
    if (!serviceResult.ok) assert.equal(serviceResult.error.outcomeUnknown, true);
  }
});

it("sends bounded literal workspace search with each continuation", async () => {
  const queries: unknown[] = [];
  const api = {
    GET: async (_path: string, options: { params: { query: unknown } }) => {
      queries.push(options.params.query);
      return { data: { items: [] }, response: new Response(null, { status: 200 }) };
    },
  } as unknown as DataGroundClient;
  assert.equal((await listWorkspaces(api, scope, "next_cursor", "North%_\\")).ok, true);
  assert.deepEqual(queries, [{ limit: 50, cursor: "next_cursor", q: "North%_\\" }]);
  for (const q of ["x".repeat(129), "\n", "\u0085", "\ud800"])
    assert.equal((await listWorkspaces(api, scope, undefined, q)).ok, false);
  assert.equal(queries.length, 1);
  assert.equal((await listWorkspaces(api, scope, undefined, "界".repeat(128))).ok, true);
});
