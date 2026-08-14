import assert from "node:assert/strict";
import { describe, it } from "vitest";
import { createDataGroundClient } from "./client";

describe("createDataGroundClient", () => {
  it("adds an explicit bearer credential to typed requests", async () => {
    let observedRequest: Request | undefined;
    const client = createDataGroundClient("https://api.invalid", {
      bearerToken: "a".repeat(32),
      fetch: async (input, init) => {
        observedRequest = new Request(input, init);
        return new Response(
          JSON.stringify({
            error: {
              code: "AUTHENTICATION_REQUIRED",
              correlationId: "corr_test",
              message: "Authentication is required.",
              retryable: false,
            },
          }),
          { headers: { "content-type": "application/json" }, status: 401 },
        );
      },
    });

    await client.POST("/v1/isolation-domains/{isolationDomainId}/agent-services", {
      body: { name: "Test service" },
      params: {
        header: { "Idempotency-Key": "service:test" },
        path: { isolationDomainId: "iso_00000000000000000001" },
      },
    });

    assert.ok(observedRequest);
    assert.equal(
      observedRequest.url,
      "https://api.invalid/v1/isolation-domains/iso_00000000000000000001/agent-services",
    );
    assert.equal(observedRequest.headers.get("authorization"), `Bearer ${"a".repeat(32)}`);
  });
});
