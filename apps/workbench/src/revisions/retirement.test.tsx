import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { retireServiceRevision, type ServiceRevisionHistoryResource } from "./client";
import {
  prepareRetirementAttempt,
  ServiceRevisionRetireWorkflow,
} from "./ServiceRevisionRetireWorkflow";

const published: ServiceRevisionHistoryResource = {
  metadata: {
    createdAt: "2026-08-31T08:00:00Z",
    createdBy: "usr_00000000000000000001",
    generation: 2,
    id: "rev_00000000000000000001",
    isolationDomainId: "iso_00000000000000000001",
    updatedAt: "2026-08-31T08:05:00Z",
    version: 2,
  },
  inputSchema: { type: "object", properties: { prompt: { type: "string" } } },
  publishedAt: "2026-08-31T08:05:00Z",
  requiredCapabilities: ["tool"],
  revisionNumber: 2,
  runtimeProfile: "reference/v1",
  serviceId: "svc_00000000000000000001",
  state: "published",
};
function retired() {
  return {
    ...structuredClone(published),
    state: "retired",
    metadata: {
      ...published.metadata,
      generation: 3,
      version: 3,
      updatedAt: "2026-08-31T09:00:00Z",
    },
  };
}
const key = "retirement:00000000000040008000000000000001";

describe("revision retirement", () => {
  it("recovers a lost acknowledgement with the original immutable command", async () => {
    const source = structuredClone(published);
    const attempt = prepareRetirementAttempt(source, () => "00000000-0000-4000-8000-000000000001");
    source.metadata.version = 99;
    source.requiredCapabilities.push("changed");
    source.inputSchema = { type: "string" };
    const calls: unknown[] = [];
    const client = {
      POST: async (path: string, options: unknown) => {
        calls.push({ path, options });
        if (calls.length === 1) throw new Error("private upstream contents");
        return {
          data: { ...retired(), privateField: "not public" },
          response: new Response(null, { status: 200 }),
        };
      },
    } as unknown as DataGroundClient;
    const first = await retireServiceRevision(client, attempt.revision, attempt.idempotencyKey);
    assert.equal(first.ok, false);
    if (!first.ok) {
      assert.equal(first.error.outcomeUnknown, true);
      assert.equal(first.error.retryable, true);
      assert.doesNotMatch(first.error.message, /private upstream/);
    }
    const replay = await retireServiceRevision(client, attempt.revision, attempt.idempotencyKey);
    assert.equal(replay.ok, true);
    if (replay.ok) assert.equal("privateField" in replay.revision, false);
    assert.deepEqual(calls[0], calls[1]);
    assert.deepEqual(calls[0], {
      path: "/v1/isolation-domains/{isolationDomainId}/service-revisions/{revisionId}/actions/retire",
      options: {
        params: {
          path: {
            isolationDomainId: published.metadata.isolationDomainId,
            revisionId: published.metadata.id,
          },
          header: { "Idempotency-Key": key },
        },
        body: { expectedVersion: 2 },
      },
    });
    assert.deepEqual(attempt.revision, published);
  });

  it("rejects invalid scope or state before transport", async () => {
    let calls = 0;
    const client = {
      POST: async () => {
        calls++;
        throw new Error("unexpected");
      },
    } as unknown as DataGroundClient;
    for (const revision of [
      undefined,
      { ...published, state: "retired" },
      { ...published, serviceId: "../secret" },
      { ...published, metadata: { ...published.metadata, isolationDomainId: "foreign" } },
    ]) {
      const result = await retireServiceRevision(
        client,
        revision as ServiceRevisionHistoryResource,
        key,
      );
      assert.equal(result.ok, false);
      if (!result.ok) assert.equal(result.error.retryable, false);
    }
    assert.equal((await retireServiceRevision(client, published, "invalid key")).ok, false);
    assert.equal(calls, 0);
  });

  it("requires exact scope, transition, and immutable definition in successful responses", async () => {
    const mutations: Array<(value: ReturnType<typeof retired>) => void> = [
      (value) => {
        value.metadata.isolationDomainId = "iso_00000000000000000002";
      },
      (value) => {
        value.serviceId = "svc_00000000000000000002";
      },
      (value) => {
        value.metadata.id = "rev_00000000000000000002";
      },
      (value) => {
        value.metadata.version++;
      },
      (value) => {
        value.metadata.generation++;
      },
      (value) => {
        value.state = "published";
      },
      (value) => {
        value.runtimeProfile = "other/v1";
      },
      (value) => {
        value.requiredCapabilities = [];
      },
      (value) => {
        value.inputSchema = { type: "string" };
      },
      (value) => {
        value.metadata.createdBy = "different";
      },
      (value) => {
        value.publishedAt = "2026-08-31T08:06:00Z";
      },
    ];
    for (const mutate of mutations) {
      const data = retired();
      mutate(data);
      const client = {
        POST: async () => ({ data, response: new Response(null, { status: 200 }) }),
      } as unknown as DataGroundClient;
      const result = await retireServiceRevision(client, published, key);
      assert.equal(result.ok, false);
      if (!result.ok) {
        assert.equal(result.error.outcomeUnknown, true);
        assert.equal(result.error.retryable, true);
      }
    }
  });

  it("distinguishes authoritative refusals from uncertain transport and server outcomes", async () => {
    for (const [status, code] of [
      [409, "REVISION_STILL_ROUTED"],
      [409, "REVISION_STILL_ACTIVE"],
      [403, "FORBIDDEN"],
      [503, "UNAVAILABLE"],
    ] as const) {
      const client = {
        POST: async () => ({
          error: {
            error: {
              code,
              message: "Safe refusal.",
              correlationId: "corr-retirement",
              retryable: false,
            },
          },
          response: new Response(null, { status }),
        }),
      } as unknown as DataGroundClient;
      const result = await retireServiceRevision(client, published, key);
      assert.equal(result.ok, false);
      if (!result.ok) {
        assert.equal(result.error.code, code);
        assert.equal(result.error.correlationId, "corr-retirement");
        assert.equal(result.error.outcomeUnknown === true, status >= 500);
        assert.equal(result.error.retryable, status >= 500);
      }
    }
  });

  it("requires explicit confirmation and hides the command without permission", () => {
    const client = {
      POST: () => {
        throw new Error("must not run during render");
      },
    } as unknown as DataGroundClient;
    const render = (canRetire: boolean) =>
      renderToStaticMarkup(
        <ServiceRevisionRetireWorkflow
          client={client}
          revision={published}
          canRetire={canRetire}
          onClose={() => undefined}
          onRetired={() => undefined}
        />,
      );
    const markup = render(true);
    assert.match(markup, /Confirm retirement/);
    assert.match(markup, /rev_00000000000000000001/);
    assert.match(markup, /Version 2/);
    assert.match(markup, /Move every alias away/);
    assert.doesNotMatch(render(false), /Confirm retirement/);
    assert.match(render(false), /role="alert"/);
  });
});
