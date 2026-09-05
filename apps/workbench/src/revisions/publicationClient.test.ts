import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import type { ServiceRevisionResource } from "./client";
import { observeServiceRevisionPublication, publishServiceRevision } from "./publicationClient";

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
    assert.ok(result.ok && result.revision);
    if (result.ok && result.revision) {
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

const operation = {
  metadata: { ...draft.metadata, id: "op_00000000000000000001" },
  resourceId: draft.metadata.id,
  kind: "service-publication",
  command: "publish",
  desiredState: "published",
  observedState: "queued",
  stateMachineVersion: 1,
  attempt: 0,
  correlationId: "cor_publication",
  terminalResult: { secret: "must be stripped" },
};
const reply = (data: unknown, status = 200) => ({ data, response: new Response(null, { status }) });
async function accept(data: unknown = operation) {
  return publishServiceRevision(
    { POST: async () => reply(data, 202) } as unknown as DataGroundClient,
    draft,
    "publication:async0001",
  );
}

describe("asynchronous publication observation", () => {
  it("accepts current and historical receipts without treating acceptance as publication", async () => {
    for (const data of [operation, { ...operation, resourceId: undefined }]) {
      const result = await accept(data);
      assert.ok(result.ok && result.operation);
      assert.equal(result.revision, undefined);
      assert.equal(result.operation.resourceId, data.resourceId);
      assert.equal("terminalResult" in result.operation, false);
    }
  });
  it("rejects malformed or substituted acceptance", async () => {
    for (const data of [
      { ...operation, kind: "invocation-execution" },
      { ...operation, resourceId: "rev_00000000000000000002" },
      { ...operation, observedState: "succeeded" },
      { ...operation, command: ["publish"] },
      { ...operation, desiredState: "cancelled" },
      { ...operation, stateMachineVersion: 2 },
      { ...operation, metadata: { ...operation.metadata, id: "invalid" } },
      {
        ...operation,
        metadata: { ...operation.metadata, isolationDomainId: "iso_00000000000000000002" },
      },
      { ...operation, metadata: { ...operation.metadata, version: 0 } },
    ])
      assert.equal((await accept(data)).ok, false);
  });
  it("requires a current exact resource binding, including for a legacy acceptance", async () => {
    const accepted = await accept({ ...operation, resourceId: undefined });
    assert.ok(accepted.ok && accepted.operation);
    for (const data of [
      { ...operation, resourceId: undefined },
      { ...operation, resourceId: "rev_00000000000000000002" },
      { ...operation, metadata: { ...operation.metadata, id: "op_00000000000000000002" } },
      { ...operation, metadata: { ...operation.metadata, createdAt: "2026-08-14T15:00:00Z" } },
    ]) {
      const result = await observeServiceRevisionPublication(
        { GET: async () => reply(data) } as unknown as DataGroundClient,
        draft,
        accepted.operation,
      );
      assert.ok(!result.ok);
      assert.equal(result.error.code, "WORKBENCH_PUBLICATION_OPERATION_MISMATCH");
    }
    const result = await observeServiceRevisionPublication(
      { GET: async () => reply(operation) } as unknown as DataGroundClient,
      draft,
      accepted.operation,
    );
    assert.ok(result.ok);
    assert.equal(result.operation.resourceId, draft.metadata.id);
    assert.equal(result.revision, undefined);
  });
  it("reads the exact revision only after published and preserves its durable generation", async () => {
    const accepted = await accept();
    assert.ok(accepted.ok && accepted.operation);
    const calls: Array<{ path: string; options: unknown }> = [];
    const result = await observeServiceRevisionPublication(
      {
        GET: async (path: string, options: unknown) => {
          calls.push({ path, options });
          return reply(
            path.includes("operations")
              ? { ...operation, observedState: "published" }
              : { ...published, metadata: { ...published.metadata, generation: 2 } },
          );
        },
      } as unknown as DataGroundClient,
      draft,
      accepted.operation,
    );
    assert.ok(result.ok && result.revision);
    assert.equal(result.revision.metadata.generation, 2);
    assert.equal(result.revision.metadata.version, 2);
    assert.deepEqual(
      calls.map((call) => call.options),
      [
        {
          params: {
            path: {
              isolationDomainId: draft.metadata.isolationDomainId,
              operationId: operation.metadata.id,
            },
          },
        },
        {
          params: {
            path: {
              isolationDomainId: draft.metadata.isolationDomainId,
              revisionId: draft.metadata.id,
            },
          },
        },
      ],
    );
    assert.equal("futureResponseField" in result.revision, false);
  });
  it("keeps failed, cancelled and repaired operations separate from a published revision", async () => {
    const accepted = await accept();
    assert.ok(accepted.ok && accepted.operation);
    for (const state of ["queued", "validating", "applying", "observing", "failed", "cancelled"]) {
      let calls = 0;
      const result = await observeServiceRevisionPublication(
        {
          GET: async () => {
            calls++;
            return reply({
              ...operation,
              command: state === "cancelled" ? "cancel" : "repair",
              desiredState: state === "cancelled" ? "cancelled" : "published",
              observedState: state,
            });
          },
        } as unknown as DataGroundClient,
        draft,
        accepted.operation,
      );
      assert.ok(result.ok);
      assert.equal(result.operation.state, state);
      assert.equal(result.revision, undefined);
      assert.equal(calls, 1);
    }
  });
  it("rejects regressed operation observations and malformed saved references", async () => {
    const accepted = await accept({
      ...operation,
      metadata: {
        ...operation.metadata,
        version: 3,
        generation: 2,
        updatedAt: "2026-08-14T16:02:00Z",
      },
    });
    assert.ok(accepted.ok && accepted.operation);
    for (const change of [
      { version: 2 },
      { generation: 1 },
      { updatedAt: "2026-08-14T16:01:00Z" },
    ]) {
      const result = await observeServiceRevisionPublication(
        {
          GET: async () =>
            reply({
              ...operation,
              metadata: {
                ...operation.metadata,
                version: 3,
                generation: 2,
                updatedAt: "2026-08-14T16:02:00Z",
                ...change,
              },
            }),
        } as unknown as DataGroundClient,
        draft,
        accepted.operation,
      );
      assert.ok(!result.ok);
    }
    const result = await observeServiceRevisionPublication({} as DataGroundClient, draft, {
      ...accepted.operation,
      createdAt: "invalid",
    });
    assert.ok(!result.ok);
    assert.equal(result.error.code, "WORKBENCH_INVALID_PUBLICATION_OBSERVATION");
  });
  it("withholds routing when the completed operation has a changed, retired or unreadable revision", async () => {
    const accepted = await accept();
    assert.ok(accepted.ok && accepted.operation);
    const exact = { ...published, metadata: { ...published.metadata, generation: 2 } };
    for (const data of [
      { ...exact, state: "retired", retiredAt: exact.publishedAt },
      { ...exact, runtimeProfile: "substituted/v1" },
      { ...exact, inputSchema: { type: "string" } },
      { ...exact, metadata: { ...exact.metadata, generation: 1 } },
      { ...exact, metadata: { ...exact.metadata, version: 3 } },
      { ...exact, serviceId: "svc_00000000000000000002" },
    ]) {
      const result = await observeServiceRevisionPublication(
        {
          GET: async (path: string) =>
            reply(
              path.includes("operations") ? { ...operation, observedState: "published" } : data,
            ),
        } as unknown as DataGroundClient,
        draft,
        accepted.operation,
      );
      assert.ok(!result.ok);
    }
    for (const denyRevision of [false, true]) {
      const result = await observeServiceRevisionPublication(
        {
          GET: async (path: string) =>
            denyRevision && path.includes("operations")
              ? reply({ ...operation, observedState: "published" })
              : {
                  response: new Response(null, { status: 403 }),
                  error: {
                    error: {
                      code: "REQUEST_DENIED",
                      message: "Access denied.",
                      correlationId: "cor_denied",
                      retryable: false,
                    },
                  },
                },
        } as unknown as DataGroundClient,
        draft,
        accepted.operation,
      );
      assert.ok(!result.ok);
      assert.equal(result.error.code, "REQUEST_DENIED");
    }
  });
  it("allows a read retry after transport failure without publishing again", async () => {
    const accepted = await accept();
    assert.ok(accepted.ok && accepted.operation);
    let calls = 0;
    const client = {
      GET: async () => {
        if (++calls === 1) throw new Error("secret");
        return reply(operation);
      },
    } as unknown as DataGroundClient;
    const first = await observeServiceRevisionPublication(client, draft, accepted.operation);
    assert.ok(!first.ok);
    assert.equal(first.error.retryable, true);
    assert.equal(first.error.message.includes("secret"), false);
    assert.ok((await observeServiceRevisionPublication(client, draft, accepted.operation)).ok);
    assert.equal(calls, 2);
  });
});
