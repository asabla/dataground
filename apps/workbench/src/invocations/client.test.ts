import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { cancelInvocation, invokeAgentService, readInvocationStatus } from "./client";

const reference = {
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};

const invocation = {
  alias: "stable",
  artifactIds: ["art_00000000000000000001"],
  correlationId: "cor_00000000000000000001",
  input: { prompt: "must not reach presentation" },
  metadata: {
    createdAt: "2026-08-14T12:00:00Z",
    createdBy: "reference-runtime",
    generation: 1,
    id: reference.invocationId,
    isolationDomainId: reference.isolationDomainId,
    nativeEndpoint: "must be stripped",
    updatedAt: "2026-08-14T12:00:01Z",
    version: 1,
  },
  operationId: "op_00000000000000000001",
  result: { secret: "must not reach presentation" },
  revisionId: "rev_00000000000000000001",
  serviceId: "svc_00000000000000000001",
  state: "running" as const,
  usage: { inputTokens: 12, outputTokens: 8, totalTokens: 20 },
};

const operation = {
  attempt: 1,
  command: "invoke",
  correlationId: invocation.correlationId,
  desiredState: "succeeded",
  kind: "invocation-execution",
  lease: { owner: "must be stripped" },
  metadata: { ...invocation.metadata, id: invocation.operationId },
  observedState: "running",
  stateMachineVersion: 2,
  terminalResult: { secret: "must be stripped" },
};

function successClient(invocationValue: unknown = invocation, operationValue: unknown = operation) {
  return {
    GET: async (path: string) => ({
      data: path.includes("operations") ? operationValue : invocationValue,
      response: new Response(null, { status: 200 }),
    }),
  } as unknown as DataGroundClient;
}

describe("invocation status client", () => {
  it("creates an invocation in the exact service scope and strips submitted input", async () => {
    const calls: Array<{ options: unknown; path: string }> = [];
    const target = {
      isolationDomainId: reference.isolationDomainId,
      serviceId: invocation.serviceId,
    };
    const client = {
      GET: async (path: string, options: unknown) => {
        calls.push({ options, path });
        return { data: operation, response: new Response(null, { status: 200 }) };
      },
      POST: async (path: string, options: unknown) => {
        calls.push({ options, path });
        return {
          data: { ...invocation, state: "accepted" },
          response: new Response(null, { status: 202 }),
        };
      },
    } as unknown as DataGroundClient;

    const result = await invokeAgentService(
      client,
      target,
      "stable",
      { prompt: "governed prompt" },
      "invoke:stable0001",
    );

    assert.equal(result.ok, true);
    assert.deepEqual(calls[0], {
      options: {
        body: { alias: "stable", input: { prompt: "governed prompt" } },
        params: { header: { "Idempotency-Key": "invoke:stable0001" }, path: target },
      },
      path: "/v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/invocations",
    });
    if (result.ok) assert.equal("input" in result.invocation, false);
  });

  it("preserves false, zero, and empty strings in the submitted JSON", async () => {
    let submitted: unknown;
    const client = {
      POST: async (_path: string, options: { body: unknown }) => {
        submitted = options.body;
        return {
          data: { ...invocation, state: "accepted" },
          response: new Response(null, { status: 202 }),
        };
      },
      GET: async () => ({ data: operation, response: new Response(null, { status: 200 }) }),
    } as unknown as DataGroundClient;
    const result = await invokeAgentService(
      client,
      { isolationDomainId: reference.isolationDomainId, serviceId: invocation.serviceId },
      "stable",
      { count: 0, enabled: false, mode: "", ratio: -0.5 },
      "invoke:typed0001",
    );
    assert.equal(result.ok, true);
    assert.deepEqual(submitted, {
      alias: "stable",
      input: { count: 0, enabled: false, mode: "", ratio: -0.5 },
    });
  });

  it("rejects oversized serialized bodies and unrepresentable numbers before transport", async () => {
    let calls = 0;
    const client = {
      POST: async () => {
        calls++;
        throw new Error("unexpected transport");
      },
    } as unknown as DataGroundClient;
    for (const input of [
      { value: "😀".repeat(262144) },
      { value: "\n".repeat(524288) },
      { value: Number.NaN },
      { value: Number.POSITIVE_INFINITY },
      { value: Number.MAX_SAFE_INTEGER + 1 },
    ]) {
      const result = await invokeAgentService(
        client,
        { isolationDomainId: reference.isolationDomainId, serviceId: invocation.serviceId },
        "stable",
        input,
        "invoke:typed0002",
      );
      assert.equal(result.ok, false);
      if (!result.ok) {
        assert.equal(result.error.code, "WORKBENCH_INVALID_INVOCATION_REQUEST");
        assert.equal(result.error.retryable, false);
        assert.doesNotMatch(result.error.message, /😀|NaN|Infinity/);
      }
    }
    assert.equal(calls, 0);
  });

  it("rejects invalid create requests and cross-service responses before operation reads", async () => {
    let calls = 0;
    const target = {
      isolationDomainId: reference.isolationDomainId,
      serviceId: invocation.serviceId,
    };
    const client = {
      GET: async () => {
        calls++;
        return { data: operation, response: new Response(null, { status: 200 }) };
      },
      POST: async () => {
        calls++;
        return {
          data: { ...invocation, serviceId: "svc_00000000000000000002" },
          response: new Response(null, { status: 202 }),
        };
      },
    } as unknown as DataGroundClient;

    const invalid = await invokeAgentService(client, target, "INVALID", { prompt: "x" }, "short");
    const mismatched = await invokeAgentService(
      client,
      target,
      "stable",
      { prompt: "x" },
      "invoke:stable0002",
    );

    assert.equal(invalid.ok, false);
    assert.equal(mismatched.ok, false);
    assert.equal(calls, 1);
    if (!mismatched.ok) assert.equal(mismatched.error.code, "WORKBENCH_INVOCATION_SCOPE_MISMATCH");
  });

  it("classifies thrown invocation transport as an uncertain retryable outcome", async () => {
    const result = await invokeAgentService(
      {
        POST: async () => {
          throw new Error("secret upstream diagnostic");
        },
      } as unknown as DataGroundClient,
      { isolationDomainId: reference.isolationDomainId, serviceId: invocation.serviceId },
      "stable",
      { prompt: "secret input" },
      "invoke:stable0003",
    );

    assert.equal(result.ok, false);
    if (!result.ok) {
      assert.equal(result.error.code, "WORKBENCH_INVOCATION_UNCONFIRMED");
      assert.equal(result.error.outcomeUnknown, true);
      assert.doesNotMatch(result.error.message, /secret|upstream/u);
    }
  });

  it("binds complete scoped paths and strips content and native operation fields", async () => {
    const calls: Array<{ options: unknown; path: string }> = [];
    const client = {
      GET: async (path: string, options: unknown) => {
        calls.push({ options, path });
        return {
          data: path.includes("operations") ? operation : invocation,
          response: new Response(null, { status: 200 }),
        };
      },
    } as unknown as DataGroundClient;

    const result = await readInvocationStatus(client, reference);

    assert.equal(result.ok, true);
    assert.deepEqual(calls, [
      {
        options: { params: { path: reference } },
        path: "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}",
      },
      {
        options: {
          params: {
            path: {
              isolationDomainId: reference.isolationDomainId,
              operationId: invocation.operationId,
            },
          },
        },
        path: "/v1/isolation-domains/{isolationDomainId}/operations/{operationId}",
      },
    ]);
    if (result.ok) {
      assert.equal("input" in result.invocation, false);
      assert.equal("result" in result.invocation, false);
      assert.equal("nativeEndpoint" in result.invocation.metadata, false);
      assert.equal("lease" in (result.operation ?? {}), false);
      assert.equal("terminalResult" in (result.operation ?? {}), false);
    }
  });

  it("preserves safe future invocation and operation states explicitly", async () => {
    const result = await readInvocationStatus(
      successClient(
        { ...invocation, state: "quarantined" },
        { ...operation, desiredState: "quarantined", observedState: "inspecting" },
      ),
      reference,
    );

    assert.equal(result.ok, true);
    if (result.ok) {
      assert.equal(result.invocation.state, "quarantined");
      assert.equal(result.operation?.desiredState, "quarantined");
      assert.equal(result.operation?.observedState, "inspecting");
    }
  });

  it("accepts authoritative usage totals without inventing an arithmetic invariant", async () => {
    const result = await readInvocationStatus(
      successClient({
        ...invocation,
        usage: { inputTokens: 12, outputTokens: 8, totalTokens: 25 },
      }),
      reference,
    );

    assert.equal(result.ok, true);
    if (result.ok) {
      assert.equal(result.invocation.usage?.totalTokens, 25);
    }
  });

  it("rejects cross-scope invocation responses before reading their operation", async () => {
    let calls = 0;
    const client = {
      GET: async () => {
        calls++;
        return {
          data: {
            ...invocation,
            metadata: { ...invocation.metadata, isolationDomainId: "iso_00000000000000000002" },
          },
          response: new Response(null, { status: 200 }),
        };
      },
    } as unknown as DataGroundClient;

    const result = await readInvocationStatus(client, reference);

    assert.equal(result.ok, false);
    assert.equal(calls, 1);
    if (!result.ok) {
      assert.equal(result.error.code, "WORKBENCH_INVOCATION_SCOPE_MISMATCH");
    }
  });

  it("retains valid invocation state when operation state is unavailable or mismatched", async () => {
    const unavailable = await readInvocationStatus(
      {
        GET: async (path: string) =>
          path.includes("operations")
            ? {
                error: {
                  error: {
                    code: "RESOURCE_NOT_FOUND",
                    correlationId: "cor_00000000000000000002",
                    message: "Operation was not found.",
                    retryable: false,
                  },
                },
                response: new Response(null, { status: 404 }),
              }
            : { data: invocation, response: new Response(null, { status: 200 }) },
      } as unknown as DataGroundClient,
      reference,
    );
    const mismatched = await readInvocationStatus(
      successClient(invocation, { ...operation, correlationId: "other-correlation" }),
      reference,
    );

    assert.equal(unavailable.ok, true);
    assert.equal(mismatched.ok, true);
    if (unavailable.ok && mismatched.ok) {
      assert.equal(unavailable.operation, undefined);
      assert.equal(unavailable.operationError?.code, "RESOURCE_NOT_FOUND");
      assert.equal(mismatched.operation, undefined);
      assert.equal(mismatched.operationError?.code, "WORKBENCH_OPERATION_SCOPE_MISMATCH");
    }
  });

  it("fails closed before transport for invalid invocation references", async () => {
    let requested = false;
    const client = {
      GET: async () => {
        requested = true;
        return { data: invocation, response: new Response(null, { status: 200 }) };
      },
    } as unknown as DataGroundClient;

    const result = await readInvocationStatus(client, { ...reference, invocationId: "native-id" });

    assert.equal(result.ok, false);
    assert.equal(requested, false);
  });

  it("submits cancellation with an empty body and exact idempotency key", async () => {
    const calls: Array<{ options: unknown; path: string }> = [];
    const client = {
      GET: async (path: string, options: unknown) => {
        calls.push({ options, path });
        return { data: operation, response: new Response(null, { status: 200 }) };
      },
      POST: async (path: string, options: unknown) => {
        calls.push({ options, path });
        return {
          data: { ...invocation, state: "cancelling" },
          response: new Response(null, { status: 202 }),
        };
      },
    } as unknown as DataGroundClient;

    const result = await cancelInvocation(client, reference, "cancel:stable0001");

    assert.equal(result.ok, true);
    assert.deepEqual(calls[0], {
      options: {
        body: {},
        params: {
          header: { "Idempotency-Key": "cancel:stable0001" },
          path: reference,
        },
      },
      path: "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/actions/cancel",
    });
  });

  it("classifies thrown cancellation transport as an uncertain retryable outcome", async () => {
    const client = {
      POST: async () => {
        throw new Error("secret upstream diagnostic");
      },
    } as unknown as DataGroundClient;

    const result = await cancelInvocation(client, reference, "cancel:stable0002");

    assert.equal(result.ok, false);
    if (!result.ok) {
      assert.equal(result.error.code, "WORKBENCH_CANCELLATION_UNCONFIRMED");
      assert.equal(result.error.outcomeUnknown, true);
      assert.equal(result.error.retryable, true);
      assert.doesNotMatch(result.error.message, /upstream/u);
    }
  });

  it("rejects malformed errors and invalid cancellation keys safely", async () => {
    const malformedClient = {
      GET: async () => ({
        error: {
          error: {
            code: "secret-code",
            correlationId: "cor_00000000000000000002",
            message: "s".repeat(513),
            retryable: false,
          },
        },
        response: new Response(null, { status: 503 }),
      }),
    } as unknown as DataGroundClient;
    const malformed = await readInvocationStatus(malformedClient, reference);
    const invalidKey = await cancelInvocation(
      {
        POST: async () => assert.fail("transport must not be reached"),
      } as unknown as DataGroundClient,
      reference,
      "bad key",
    );

    assert.equal(malformed.ok, false);
    assert.equal(invalidKey.ok, false);
    if (!malformed.ok && !invalidKey.ok) {
      assert.equal(malformed.error.code, "WORKBENCH_INVALID_RESPONSE");
      assert.equal(invalidKey.error.code, "WORKBENCH_INVALID_REFERENCE");
    }
  });
});
