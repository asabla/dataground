import assert from "node:assert/strict";
import { it } from "vitest";
import { createDataGroundClient, type DataGroundClient } from "../contracts/client";
import { type ResourceAuditReference, readResourceAudit } from "./client";
import { auditPage, reference } from "./testFixtures";

it("uses the exact public resource path, bounded query, and no-store cache", async () => {
  for (const scope of [
    reference,
    {
      ...reference,
      resourceType: "service-revision",
      resourceId: "rev_00000000000000000001",
    } as ResourceAuditReference,
  ]) {
    let request: Request | undefined;
    const client = createDataGroundClient("https://dataground.invalid", {
      fetch: async (input) => {
        request = input as Request;
        return Response.json(auditPage(scope));
      },
    });
    assert.equal((await readResourceAudit(client, scope, "arr_00000000000000000002")).ok, true);
    assert.equal(request?.cache, "no-store");
    const url = new URL(request?.url ?? "");
    assert.equal(
      url.pathname,
      `/v1/isolation-domains/${scope.isolationDomainId}/${scope.resourceType === "invocation" ? "invocations" : "service-revisions"}/${scope.resourceId}/audit`,
    );
    assert.equal(url.searchParams.get("limit"), "50");
    assert.equal(url.searchParams.get("cursor"), "arr_00000000000000000002");
  }
});
function clientReturning(data: unknown, status = 200): DataGroundClient {
  return {
    GET: async () => ({ data, error: data, response: new Response(null, { status }) }),
  } as unknown as DataGroundClient;
}
function first<T>(items: T[]): T {
  const item = items[0];
  assert.ok(item);
  return item;
}

type MalformedPage = Record<string, unknown> & { items: Record<string, unknown>[] };

it("rejects mismatched scope, unbounded pages, repeated identifiers, unsafe fields, and invalid provenance", async () => {
  for (const change of [
    (p: MalformedPage) => {
      p.isolationDomainId = "iso_00000000000000000002";
    },
    (p: MalformedPage) => {
      p.resourceId = "inv_00000000000000000002";
    },
    (p: MalformedPage) => {
      p.resourceType = "service-revision";
    },
    (p: MalformedPage) => {
      p.schemaVersion = "dataground.resource-audit-page/v2";
    },
    (p: MalformedPage) => {
      p.items = Array(51).fill(first(p.items));
    },
    (p: MalformedPage) => {
      p.items.push(first(p.items));
    },
    (p: MalformedPage) => {
      first(p.items).safeMetadata = { secret: "private" };
    },
    (p: MalformedPage) => {
      p.snapshot = "private";
    },
    (p: MalformedPage) => {
      first(p.items).recordedAt = "not-a-date";
    },
    (p: MalformedPage) => {
      first(p.items).outcome = "invented";
    },
    (p: MalformedPage) => {
      first(p.items).actorId = "private\nactor";
    },
    (p: MalformedPage) => {
      p.nextCursor = "arr_00000000000000000002";
    },
    (p: MalformedPage) => {
      p.nextCursor = p.receiptId;
    },
    (p: MalformedPage) => {
      first(p.items).source = "invocation-authorization";
    },
  ]) {
    const page = structuredClone(auditPage()) as unknown as MalformedPage;
    change(page);
    const result = await readResourceAudit(clientReturning(page), reference);
    assert.equal(result.ok, false);
    assert.doesNotMatch(JSON.stringify(result), /private/);
  }
  assert.equal(
    (await readResourceAudit(clientReturning(auditPage()), reference, auditPage().receiptId)).ok,
    false,
  );
  const page = auditPage();
  page.items[0] = {
    ...first(page.items),
    id: `ard_${"a".repeat(32)}`,
    source: "invocation-authorization",
    action: "run",
    outcome: "allowed",
    operationId: "op_00000000000000000001",
    policySetId: "reviewed-policy",
    policyDigest: `sha256:${"a".repeat(64)}`,
  };
  assert.equal((await readResourceAudit(clientReturning(page), reference)).ok, true);
  first(page.items).phase = "entry";
  assert.equal((await readResourceAudit(clientReturning(page), reference)).ok, false);
  const revision = {
    ...reference,
    resourceType: "service-revision" as const,
    resourceId: "rev_00000000000000000001",
  };
  Object.assign(page, revision);
  Object.assign(first(page.items), { source: "publication-authorization", action: "publish" });
  assert.equal((await readResourceAudit(clientReturning(page), revision)).ok, true);
});
it("returns safe denial and unavailable messages without upstream content", async () => {
  for (const status of [400, 401, 403, 404, 503, 500]) {
    const result = await readResourceAudit(
      clientReturning(
        {
          error: {
            code: "private-code",
            message: "private-response",
            correlationId: "cor_00000000000000000001",
          },
        },
        status,
      ),
      reference,
    );
    assert.equal(result.ok, false);
    assert.doesNotMatch(JSON.stringify(result), /private/);
    if (!result.ok) assert.equal(result.error.correlationId, "cor_00000000000000000001");
  }
  let calls = 0;
  const client = {
    GET: async () => {
      calls++;
      throw new Error("private-failure");
    },
  } as unknown as DataGroundClient;
  assert.equal((await readResourceAudit(client, { ...reference, resourceId: "bad" })).ok, false);
  assert.equal((await readResourceAudit(client, reference, "bad")).ok, false);
  assert.equal(calls, 0);
  assert.doesNotMatch(JSON.stringify(await readResourceAudit(client, reference)), /private/);
});
