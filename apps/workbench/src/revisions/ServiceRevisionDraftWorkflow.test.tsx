import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import {
  createRevisionIdempotencyKey,
  ServiceRevisionDraftWorkflow,
  validateServiceRevisionDraft,
} from "./ServiceRevisionDraftWorkflow";

describe("ServiceRevisionDraftWorkflow", () => {
  it("creates stable revision command identifiers", () => {
    assert.equal(
      createRevisionIdempotencyKey(() => "00000000-0000-4000-8000-000000000001"),
      "revision:00000000000040008000000000000001",
    );
  });

  it("normalizes capabilities and parses optional JSON object schemas", () => {
    assert.deepEqual(
      validateServiceRevisionDraft({
        inputSchema: '{"type":"object"}',
        outputSchema: "",
        requiredCapabilities: " tool, usage\nquestion ",
        runtimeProfile: " reference/v1 ",
      }),
      {
        errors: {},
        request: {
          inputSchema: { type: "object" },
          requiredCapabilities: ["tool", "usage", "question"],
          runtimeProfile: "reference/v1",
        },
      },
    );
  });

  it("rejects invalid runtime, duplicate capabilities, and non-object schemas", () => {
    const validation = validateServiceRevisionDraft({
      inputSchema: "[]",
      outputSchema: "not-json",
      requiredCapabilities: "tool,tool",
      runtimeProfile: "   ",
    });

    assert.equal(validation.request, undefined);
    assert.deepEqual(validation.errors, {
      inputSchema: "Schema must be a JSON object.",
      outputSchema: "Schema must contain valid JSON.",
      requiredCapabilities: "Capability names must be unique.",
      runtimeProfile: "Runtime profile is required.",
    });
  });

  it("keeps optional schemas and capabilities empty and enforces the request boundary", () => {
    assert.deepEqual(
      validateServiceRevisionDraft({
        inputSchema: "",
        outputSchema: "   ",
        requiredCapabilities: "",
        runtimeProfile: "reference/v1",
      }),
      {
        errors: {},
        request: { requiredCapabilities: [], runtimeProfile: "reference/v1" },
      },
    );

    const oversized = validateServiceRevisionDraft({
      inputSchema: JSON.stringify({ description: "x".repeat((1 << 20) + 1) }),
      outputSchema: "",
      requiredCapabilities: "",
      runtimeProfile: "reference/v1",
    });
    assert.equal(oversized.request, undefined);
    assert.equal(oversized.errors.inputSchema, "Schema exceeds the API request limit.");
  });

  it("renders observer scope without enabling revision creation", () => {
    const markup = renderToStaticMarkup(
      <ServiceRevisionDraftWorkflow
        canCreateRevision={false}
        client={{} as DataGroundClient}
        disabledReason="Only service operators may create revisions."
        isolationDomainId="iso_00000000000000000001"
        serviceId="svc_00000000000000000001"
      />,
    );

    assert.match(markup, /Observer access only/u);
    assert.match(markup, /iso_00000000000000000001/u);
    assert.match(markup, /svc_00000000000000000001/u);
    assert.match(markup, /disabled/u);
  });
});
