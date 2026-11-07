import assert from "node:assert/strict";
import { describe, it } from "vitest";
import { createDataGroundClient } from "../contracts/client";
import { requestWorkspaceRetentionRecovery } from "./workspaceRetentionRecovery";

const reference = {
  isolationDomainId: "iso_00000000000000000001",
  invocationId: "inv_00000000000000000001",
};
const attempt = { expectedVersion: 4, idempotencyKey: `retention-recovery:${"a".repeat(32)}` };
const receipt = {
  schemaVersion: "dataground.workspace-retention-recovery-receipt/v1",
  ...reference,
  state: "requested",
  invocationVersion: 5,
  deadlineAt: "2026-01-01T00:00:00Z",
  correlationId: "cor_00000000000000000001",
};
function response(value: unknown, status = 202) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("retention recovery command", () => {
  it("retries the exact request after a lost acknowledgement and accepts historical receipts", async () => {
    const requests: unknown[] = [];
    const client = createDataGroundClient("http://localhost", {
      fetch: async (input) => {
        const request = input as Request;
        requests.push({
          body: await request.json(),
          key: request.headers.get("Idempotency-Key"),
          method: request.method,
          url: request.url,
        });
        if (requests.length === 1) throw new TypeError("lost acknowledgement");
        return response({ ...receipt, privateContent: "not retained" });
      },
    });
    assert.deepEqual(await requestWorkspaceRetentionRecovery(client, reference, attempt), {
      ok: false,
      uncertain: true,
      message:
        "The recovery request outcome is unknown. Retry the exact request to check its acceptance.",
    });
    assert.deepEqual(await requestWorkspaceRetentionRecovery(client, reference, attempt), {
      ok: true,
      receipt,
    });
    assert.deepEqual(requests[0], requests[1]);
    assert.deepEqual(requests[0], {
      body: { expectedVersion: 4 },
      key: attempt.idempotencyKey,
      method: "POST",
      url: `http://localhost/v1/isolation-domains/${reference.isolationDomainId}/invocations/${reference.invocationId}/workspace-retention/recoveries`,
    });
  });
  it("treats malformed or foreign acceptance as uncertain", async () => {
    for (const changed of [
      { invocationId: "inv_00000000000000000002" },
      { isolationDomainId: "iso_00000000000000000002" },
      { invocationVersion: 6 },
      { state: "completed" },
      { deadlineAt: "invalid" },
      { correlationId: "private" },
    ]) {
      const client = createDataGroundClient("http://localhost", {
        fetch: async () => response({ ...receipt, ...changed }),
      });
      const result = await requestWorkspaceRetentionRecovery(client, reference, attempt);
      assert.equal(result.ok, false);
      if (!result.ok) assert.equal(result.uncertain, true);
    }
  });
  it("keeps explicit rejection separate from uncertain server failures", async () => {
    for (const status of [400, 401, 403, 409, 500, 503]) {
      const client = createDataGroundClient("http://localhost", {
        fetch: async () => response({ error: { message: "private upstream details" } }, status),
      });
      const result = await requestWorkspaceRetentionRecovery(client, reference, attempt);
      assert.equal(result.ok, false);
      if (!result.ok) assert.equal(result.uncertain, status >= 500);
      assert.doesNotMatch(JSON.stringify(result), /private upstream/u);
    }
  });
  it("does not send invalid versions or unstable keys", async () => {
    const client = createDataGroundClient("http://localhost", {
      fetch: async () => {
        assert.fail("invalid request reached the network");
      },
    });
    for (const changed of [
      { expectedVersion: 0 },
      { expectedVersion: 1.5 },
      { expectedVersion: Number.MAX_SAFE_INTEGER },
      { idempotencyKey: "" },
    ])
      assert.equal(
        (await requestWorkspaceRetentionRecovery(client, reference, { ...attempt, ...changed })).ok,
        false,
      );
  });
});
