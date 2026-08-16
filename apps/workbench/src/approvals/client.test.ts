import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { readInvocationApproval, resolveInvocationApproval } from "./client";

const reference = {
  approvalId: "apr_00000000000000000001",
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};

const approval = {
  schemaVersion: "dataground.invocation-approval/v1" as const,
  id: reference.approvalId,
  isolationDomainId: reference.isolationDomainId,
  invocationId: reference.invocationId,
  requestedAction: "workspace.change" as const,
  state: "pending" as const,
  version: 1,
  createdAt: "2026-08-14T12:00:00Z",
  updatedAt: "2026-08-14T12:00:00Z",
};

describe("invocation approval client", () => {
  it("binds an authoritative read to the complete public path", async () => {
    let options: unknown;
    const client = {
      GET: async (_path: string, value: unknown) => {
        options = value;
        return { data: approval, response: new Response(null, { status: 200 }) };
      },
    } as unknown as DataGroundClient;

    const result = await readInvocationApproval(client, reference);

    assert.equal(result.ok, true);
    assert.deepEqual(options, { params: { path: reference } });
  });

  it("binds decision, path, and caller-owned idempotency key", async () => {
    let options: unknown;
    const client = {
      POST: async (_path: string, value: unknown) => {
        options = value;
        return {
          data: { ...approval, decision: "approve", state: "resolved", version: 2 },
          response: new Response(null, { status: 200 }),
        };
      },
    } as unknown as DataGroundClient;

    const result = await resolveInvocationApproval(
      client,
      reference,
      "approve",
      "approval:stable0001",
    );

    assert.equal(result.ok, true);
    assert.deepEqual(options, {
      body: { decision: "approve", expectedVersion: 1 },
      params: {
        header: { "Idempotency-Key": "approval:stable0001" },
        path: reference,
      },
    });
  });

  it("preserves safe API failures and hides thrown transport details", async () => {
    const apiClient = {
      GET: async () => ({
        error: {
          error: {
            code: "RESOURCE_NOT_FOUND",
            correlationId: "cor_00000000000000000001",
            message: "Invocation approval was not found.",
            retryable: false,
          },
        },
        response: new Response(null, { status: 404 }),
      }),
    } as unknown as DataGroundClient;
    const apiResult = await readInvocationApproval(apiClient, reference);
    assert.deepEqual(apiResult, {
      error: {
        code: "RESOURCE_NOT_FOUND",
        correlationId: "cor_00000000000000000001",
        message: "Invocation approval was not found.",
        retryable: false,
        status: 404,
      },
      ok: false,
    });

    const transportClient = {
      POST: async () => {
        throw new Error("secret upstream details");
      },
    } as unknown as DataGroundClient;
    const transportResult = await resolveInvocationApproval(
      transportClient,
      reference,
      "deny",
      "approval:stable0002",
    );
    assert.equal(transportResult.ok, false);
    if (!transportResult.ok) {
      assert.equal(transportResult.error.code, "WORKBENCH_NETWORK_UNAVAILABLE");
      assert.doesNotMatch(transportResult.error.message, /secret upstream details/u);
    }
  });

  it("rejects a successful response outside the requested scope", async () => {
    const client = {
      GET: async () => ({
        data: { ...approval, isolationDomainId: "iso_00000000000000000002" },
        response: new Response(null, { status: 200 }),
      }),
    } as unknown as DataGroundClient;

    const result = await readInvocationApproval(client, reference);

    assert.deepEqual(result, {
      error: {
        code: "WORKBENCH_SCOPE_MISMATCH",
        message: "DataGround returned approval data outside the requested scope.",
        retryable: false,
      },
      ok: false,
    });
  });

  it("rejects malformed references and idempotency keys before transport", async () => {
    let calls = 0;
    const client = {
      GET: async () => {
        calls++;
        return { data: approval, response: new Response(null, { status: 200 }) };
      },
      POST: async () => {
        calls++;
        return { data: approval, response: new Response(null, { status: 200 }) };
      },
    } as unknown as DataGroundClient;

    const invalidRead = await readInvocationApproval(client, {
      ...reference,
      approvalId: "native-approval-handle",
    });
    const invalidDecision = await resolveInvocationApproval(client, reference, "approve", "short");

    assert.equal(invalidRead.ok, false);
    assert.equal(invalidDecision.ok, false);
    assert.equal(calls, 0);
  });

  it("rejects malformed authoritative approval data", async () => {
    const client = {
      GET: async () => ({
        data: { ...approval, requestedAction: "runtime.native-action" },
        response: new Response(null, { status: 200 }),
      }),
    } as unknown as DataGroundClient;

    const result = await readInvocationApproval(client, reference);

    assert.equal(result.ok, false);
    if (!result.ok) {
      assert.equal(result.error.code, "WORKBENCH_SCOPE_MISMATCH");
    }
  });
});
